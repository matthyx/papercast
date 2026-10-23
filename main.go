package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/matthyx/papercast/internal/adb"
	"github.com/matthyx/papercast/internal/capture"
	"github.com/matthyx/papercast/internal/portal"
	"github.com/matthyx/papercast/internal/server"
)

var (
	version = "0.1.0"
)

func main() {
	host := flag.String("host", "0.0.0.0", "Host address to bind HTTP/WebSocket server")
	port := flag.Int("port", 1701, "Port to serve on")
	fps := flag.Int("fps", 30, "Target capture framerate")
	bitrate := flag.Int("bitrate", 3000, "Encoding bitrate in kbps")
	qp := flag.Int("qp", 26, "VA-API Quantization Parameter (lower = higher quality, higher = lower bitrate, default 26)")
	cursorMode := flag.Uint("cursor", uint(portal.CursorModeEmbedded), "Cursor mode (1: hidden, 2: embedded)")
	autoOpen := flag.Bool("open", false, "Automatically open Chrome on connected Android tablet")
	testMode := flag.Bool("test", false, "Run in test mode with synthetic video pattern (no desktop screen authorization needed)")
	useGst := flag.Bool("gst", false, "Use legacy GStreamer capture pipeline instead of native libpipewire")
	showVersion := flag.Bool("version", false, "Print Papercast version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Papercast v%s (Go)\n", version)
		return
	}

	log.Printf("==> Papercast v%s: Ultra-low latency desktop streamer", version)

	// 1. Setup ADB reverse port forwarding for USB connection
	if err := adb.SetupReverseForwarding(*port); err != nil {
		log.Printf("[adb] Warning: %v", err)
	}

	// 2. Start Web & WebSocket server
	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := server.NewServer(addr)
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("[server] Fatal server error: %v", err)
		}
	}()

	type streamPipeline interface {
		Start(onFrame func(frame []byte, isKey bool)) error
		Stop()
	}

	var pipeline streamPipeline
	if *testMode {
		log.Printf("[capture] Running in autonomous test mode with SMPTE test pattern (GStreamer)")
		pipeline = capture.NewPipeline(nil, *fps, *bitrate, *qp)
	} else if *useGst {
		log.Printf("[capture] Using legacy GStreamer capture pipeline")
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

		pipeline = capture.NewPipeline(sessionProvider, *fps, *bitrate, *qp)
	} else {
		// Native libpipewire + hardware VA-API capture engine (default)
		log.Printf("[capture] Initializing native libpipewire + VA-API zero-latency capture engine...")
		sess, err := portal.RequestScreenCast(uint32(*cursorMode), 10*time.Second)
		if err != nil {
			log.Fatalf("[portal] Portal authorization error: %v", err)
		}
		defer sess.Close()

		pwFile, err := sess.OpenPipeWireRemote()
		if err != nil {
			log.Fatalf("[portal] OpenPipeWireRemote error: %v", err)
		}

		pipeline = capture.NewNativePipeline(pwFile, sess.NodeID, *fps, *qp)
	}

	// 4. Start capture & encoding pipeline
	err := pipeline.Start(func(frame []byte, isKey bool) {
		srv.BroadcastFrame(frame, isKey)
	})
	if err != nil {
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
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Printf("\n[papercast] Shutting down...")
	pipeline.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	log.Printf("[papercast] Stopped cleanly.")
}
