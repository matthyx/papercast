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
	"time"
)

// Pipeline manages the GStreamer capture child process.
type Pipeline struct {
	pwFile      *os.File
	nodeID      uint32
	fps         int
	bitrateKbps int
	qp          int

	cmd    *exec.Cmd
	cancel context.CancelFunc
	mu     sync.Mutex
	done   chan struct{}
}

func NewPipeline(pwFile *os.File, nodeID uint32, fps int, bitrateKbps int, qp int) *Pipeline {
	if qp <= 0 {
		qp = 26
	}
	return &Pipeline{
		pwFile:      pwFile,
		nodeID:      nodeID,
		fps:         fps,
		bitrateKbps: bitrateKbps,
		qp:          qp,
		done:        make(chan struct{}),
	}
}

// Start launches the capture pipeline and streams complete Access Units to onFrame.
func (p *Pipeline) Start(onFrame func(frame []byte, isKey bool)) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	type encTier struct {
		name    string
		encoder string
	}

	encoders := []encTier{
		// 1. VA-API hardware encode (zero latency)
		{
			name:    "vaapi (hardware)",
			encoder: fmt.Sprintf("vaapih264enc rate-control=cqp init-qp=%d max-bframes=0 keyframe-period=%d", p.qp, p.fps),
		},
		// 2. Pure software fallback
		{
			name:    "x264enc (software)",
			encoder: fmt.Sprintf("x264enc tune=zerolatency speed-preset=ultrafast bitrate=%d key-int-max=%d", p.bitrateKbps, p.fps),
		},
	}

	var lastErr error
	for _, tier := range encoders {
		log.Printf("[capture] Trying encoder: %s", tier.name)

		pipelineStr := fmt.Sprintf(
			"pipewiresrc fd=3 path=%d always-copy=true do-timestamp=true keepalive-time=1000 ! "+
				"videorate drop-only=true max-rate=%d ! "+
				"videoconvert ! "+
				"video/x-raw,format=NV12 ! "+
				"queue max-size-buffers=2 leaky=downstream ! "+
				"%s ! "+
				"video/x-h264,profile=constrained-baseline ! "+
				"h264parse config-interval=-1 ! "+
				"video/x-h264,stream-format=byte-stream,alignment=au ! "+
				"fdsink fd=1 sync=false",
			p.nodeID, p.fps, tier.encoder,
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
			log.Printf("[capture] Failed to start pipeline with %s: %v", tier.name, err)
			continue
		}

		// Verify pipeline survives preroll before declaring success
		exitChan := make(chan error, 1)
		go func() {
			exitChan <- cmd.Wait()
		}()

		select {
		case exitErr := <-exitChan:
			cancel()
			stderrMsg := strings.TrimSpace(stderr.String())
			lastErr = fmt.Errorf("%s failed to preroll: %v (stderr: %s)", tier.name, exitErr, stderrMsg)
			log.Printf("[capture] %s failed to preroll: %s", tier.name, stderrMsg)
			continue
		case <-time.After(400 * time.Millisecond):
			// Pipeline successfully prerolled and is streaming
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

			if stderr.Len() > 0 {
				log.Printf("[capture] GStreamer output: %s", strings.TrimSpace(stderr.String()))
			}
		}(tier.name)

		log.Printf("[capture] Capture pipeline running with %s", tier.name)
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
