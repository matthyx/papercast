package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
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

	// 3. Request desktop screen share authorization via xdg-desktop-portal
	log.Printf("[portal] Requesting ScreenCast permission from desktop...")
	session, err := portal.RequestScreenCast(uint32(*cursorMode), 60*time.Second)
	if err != nil {
		log.Fatalf("[portal] Portal error: %v", err)
	}
	defer session.Close()

	// 4. Start GStreamer capture & encoding pipeline
	pipeline := capture.NewPipeline(session.PWFile, session.NodeID, *fps, *bitrate, *qp)
	err = pipeline.Start(func(frame []byte, isKey bool) {
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
