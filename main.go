package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/matthyx/papercast/internal/adb"
	"github.com/matthyx/papercast/internal/capture"
	"github.com/matthyx/papercast/internal/portal"
	"github.com/matthyx/papercast/internal/server"
	"github.com/matthyx/papercast/internal/vdisplay"
)

var (
	version = "0.2.0"
)

func resolveResolution(res string) (int, int, error) {
	trimmed := strings.ToLower(strings.TrimSpace(res))
	if trimmed == "" || trimmed == "auto" {
		if w, h, ok := adb.DetectTabletResolution(); ok {
			return w, h, nil
		}
		return 1600, 1200, nil
	}
	parts := strings.Split(trimmed, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid resolution %q (expected auto or WIDTHxHEIGHT, e.g. 1600x1200)", res)
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("invalid resolution dimensions %q", res)
	}
	return w, h, nil
}

func main() {
	host := flag.String("host", "0.0.0.0", "Host address to bind HTTP/WebSocket server")
	port := flag.Int("port", 1701, "Port to serve on")
	fps := flag.Int("fps", 30, "Target capture framerate")
	qp := flag.Int("qp", 26, "VA-API Quantization Parameter (lower = higher quality, higher = lower bitrate, default 26)")
	cursorMode := flag.Uint("cursor", uint(portal.CursorModeEmbedded), "Cursor mode (1: hidden, 2: embedded)")
	virtual := flag.Bool("virtual", true, "Create a dedicated virtual display at native tablet resolution (use -virtual=false to mirror existing display)")
	res := flag.String("res", "auto", "Virtual display resolution (auto, or WIDTHxHEIGHT e.g. 1600x1200, 1200x1600)")
	scale := flag.Float64("scale", 1.25, "Pixel scale factor for virtual display (e.g. 1.25 or 1.5 for larger fonts)")
	output := flag.String("output", "", "Specific display output to capture (e.g. eDP-1, DP-2, HDMI-A-1)")
	mirrorOn := flag.String("mirror-on", "", "Mirror the virtual display onto a physical output (e.g. eDP-1)")
	autoOpen := flag.Bool("open", false, "Automatically open Chrome on connected Android tablet")
	showVersion := flag.Bool("version", false, "Print Papercast version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Papercast v%s (Go)\n", version)
		return
	}

	log.Printf("==> Papercast v%s: Ultra-low latency desktop streamer", version)

	// Register signal handler early so SIGINT/SIGTERM during startup still cleans up virtual displays
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// 1. Setup ADB reverse port forwarding for USB connection
	if err := adb.SetupReverseForwarding(*port); err != nil {
		log.Printf("[adb] Warning: %v", err)
	}

	// 2. Provision hardware-accelerated virtual display if enabled
	var vdisp *vdisplay.VirtualDisplay
	targetOut := strings.TrimSpace(*output)

	if *virtual {
		w, h, err := resolveResolution(*res)
		if err != nil {
			log.Fatalf("[vdisplay] %v", err)
		}
		vdisp, err = vdisplay.Enable(vdisplay.Config{
			Connector: targetOut,
			Width:     w,
			Height:    h,
			Scale:     *scale,
			MirrorOn:  strings.TrimSpace(*mirrorOn),
		})
		if err != nil {
			log.Printf("[vdisplay] Warning: could not create virtual display (%v); falling back to existing screen", err)
		} else {
			targetOut = vdisp.Connector
			defer func() {
				_ = vdisp.Close()
			}()
		}
	}

	if targetOut != "" {
		portal.SetTargetOutput(targetOut)
		log.Printf("[portal] Targeting display output: %s", targetOut)
	}

	// 3. Start Web & WebSocket server
	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := server.NewServer(addr)
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("[server] Fatal server error: %v", err)
		}
	}()

	var (
		currentSession *portal.Session
		sessMu         sync.Mutex
	)

	sessionProvider := func() (*os.File, uint32, error) {
		sessMu.Lock()
		defer sessMu.Unlock()

		if currentSession != nil {
			_ = currentSession.Close()
			currentSession = nil
		}

		log.Printf("[portal] Acquiring fresh ScreenCast session via restore token...")
		sess, err := portal.RequestScreenCast(uint32(*cursorMode), 10*time.Second)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to acquire portal session: %w", err)
		}
		currentSession = sess

		pwFile, err := sess.OpenPipeWireRemote()
		if err != nil {
			return nil, 0, err
		}
		return pwFile, sess.NodeID, nil
	}

	defer func() {
		sessMu.Lock()
		if currentSession != nil {
			_ = currentSession.Close()
		}
		sessMu.Unlock()
	}()

	// 4. Start native capture & encoding pipeline (libpipewire + VA-API)
	log.Printf("[capture] Initializing native libpipewire + VA-API zero-latency capture engine...")
	pipeline := capture.NewPipeline(sessionProvider, *fps, *qp)
	err := pipeline.Start(func(frame []byte, isKey bool) {
		srv.BroadcastFrame(frame, isKey)
	})
	if err != nil {
		if vdisp != nil {
			_ = vdisp.Close()
		}
		log.Fatalf("[capture] Pipeline error: %v", err)
	}
	defer pipeline.Stop()

	log.Printf("==================================================================")
	log.Printf(" ==> STREAM READY! Open http://127.0.0.1:%d on your HannsNote2 tablet.", *port)
	log.Printf("==================================================================")

	if *autoOpen {
		if err := adb.LaunchTabletBrowser(*port); err != nil {
			log.Printf("[adb] Could not launch browser on tablet: %v", err)
		}
	}

	// Wait for termination signal
	<-sigChan

	log.Printf("\n[papercast] Shutting down...")
	pipeline.Stop()
	if vdisp != nil {
		_ = vdisp.Close()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	log.Printf("[papercast] Stopped cleanly.")
}
