package capture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Pipeline manages the GStreamer capture child process.
type Pipeline struct {
	pwFile      *os.File
	nodeID      uint32
	fps         int
	bitrateKbps int

	cmd    *exec.Cmd
	cancel context.CancelFunc
	mu     sync.Mutex
	done   chan struct{}
}

func NewPipeline(pwFile *os.File, nodeID uint32, fps int, bitrateKbps int) *Pipeline {
	return &Pipeline{
		pwFile:      pwFile,
		nodeID:      nodeID,
		fps:         fps,
		bitrateKbps: bitrateKbps,
		done:        make(chan struct{}),
	}
}

// Start launches the capture pipeline and streams complete Access Units to onFrame.
func (p *Pipeline) Start(onFrame func(frame []byte, isKey bool)) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Available encoders in order of preference:
	// 1. Intel VA-API zero-latency CQP hardware encoder
	// 2. x264enc ultrafast software encoder
	encoders := []string{
		fmt.Sprintf("vaapih264enc rate-control=cqp init-qp=24 max-bframes=0 keyframe-period=%d", p.fps),
		fmt.Sprintf("x264enc tune=zerolatency speed-preset=ultrafast bitrate=%d key-int-max=%d", p.bitrateKbps, p.fps),
	}

	var lastErr error
	for _, enc := range encoders {
		encName := strings.Fields(enc)[0]
		log.Printf("[capture] Trying encoder: %s", encName)

		pipelineStr := fmt.Sprintf(
			"pipewiresrc fd=3 path=%d do-timestamp=true keepalive-time=1000 ! "+
				"videoconvert ! "+
				"video/x-raw,format=NV12,framerate=%d/1 ! "+
				"%s ! "+
				"video/x-h264,profile=constrained-baseline ! "+
				"h264parse config-interval=-1 ! "+
				"video/x-h264,stream-format=byte-stream,alignment=au ! "+
				"fdsink fd=1 sync=false",
			p.nodeID, p.fps, enc,
		)

		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "gst-launch-1.0", strings.Fields(pipelineStr)...)
		cmd.ExtraFiles = []*os.File{p.pwFile}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		err = cmd.Start()
		if err != nil {
			cancel()
			lastErr = err
			log.Printf("[capture] Failed to start pipeline with %s: %v", encName, err)
			continue
		}

		p.cmd = cmd
		p.cancel = cancel

		// Read loop goroutine
		go func(encSelected string) {
			defer close(p.done)
			splitter := NewAUSplitter()
			buf := make([]byte, 64*1024)
			var frameCount int

			for {
				n, readErr := stdout.Read(buf)
				if n > 0 {
					frames := splitter.Push(buf[:n])
					for _, frame := range frames {
						frameCount++
						if frameCount == 1 {
							log.Printf("[capture] First frame encoded (%d bytes) with encoder: %s", len(frame), encSelected)
						}
						onFrame(frame, IsKeyframe(frame))
					}
				}
				if readErr != nil {
					if readErr != io.EOF && !strings.Contains(readErr.Error(), "file already closed") {
						log.Printf("[capture] Stream read error: %v", readErr)
					}
					break
				}
			}

			_ = cmd.Wait()
			if stderr.Len() > 0 {
				log.Printf("[capture] GStreamer output: %s", strings.TrimSpace(stderr.String()))
			}
		}(encName)

		log.Printf("[capture] Capture pipeline running with %s", encName)
		return nil
	}

	return fmt.Errorf("failed to start any capture pipeline: %v", lastErr)
}

// Stop shuts down the capture pipeline.
func (p *Pipeline) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// Wait blocks until the capture pipeline terminates.
func (p *Pipeline) Wait() {
	<-p.done
}
