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

// SessionProvider provides a live PipeWire remote file and node ID.
// If the session died (e.g. portal crash), it re-acquires a valid session seamlessly.
type SessionProvider func() (*os.File, uint32, error)

// Pipeline manages the GStreamer capture child process.
type Pipeline struct {
	sessionProvider SessionProvider
	fps             int
	bitrateKbps     int
	qp              int

	cmd         *exec.Cmd
	cancel      context.CancelFunc
	processDone chan struct{}
	quit        chan struct{}
	mu          sync.Mutex
	stopped     bool
	done        chan struct{}
}

func NewPipeline(sessionProvider SessionProvider, fps int, bitrateKbps int, qp int) *Pipeline {
	if qp <= 0 {
		qp = 26
	}
	return &Pipeline{
		sessionProvider: sessionProvider,
		fps:             fps,
		bitrateKbps:     bitrateKbps,
		qp:              qp,
		processDone:     make(chan struct{}, 1),
		quit:            make(chan struct{}),
		done:            make(chan struct{}),
	}
}

// Start launches the capture pipeline and runs a supervisor loop that auto-revives it if killed.
func (p *Pipeline) Start(onFrame func(frame []byte, isKey bool)) error {
	p.mu.Lock()
	p.stopped = false
	p.mu.Unlock()

	// Initial start
	err := p.launchOnce(onFrame)
	if err != nil {
		return err
	}

	// Supervisor goroutine: auto-restarts GStreamer with a fresh PipeWire connection if interrupted
	go func() {
		for {
			select {
			case <-p.quit:
				return
			case <-p.processDone:
				p.mu.Lock()
				if p.stopped {
					p.mu.Unlock()
					return
				}
				p.mu.Unlock()

				log.Printf("[capture] Stream interrupted (fullscreen/resolution/portal reset). Reviving pipeline in 150ms...")
				time.Sleep(150 * time.Millisecond)

				p.mu.Lock()
				if !p.stopped {
					if rErr := p.launchOnce(onFrame); rErr != nil {
						log.Printf("[capture] Revive failed: %v, will retry in 500ms...", rErr)
						time.Sleep(500 * time.Millisecond)
						select {
						case p.processDone <- struct{}{}:
						default:
						}
					}
				}
				p.mu.Unlock()
			}
		}
	}()

	return nil
}

func (p *Pipeline) launchOnce(onFrame func(frame []byte, isKey bool)) error {
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

		var (
			pipelineStr string
			extraFiles  []*os.File
			pwFile      *os.File
			nodeID      uint32
		)

		if p.sessionProvider != nil {
			// Request a fresh PipeWire remote FD and live node ID
			var err error
			pwFile, nodeID, err = p.sessionProvider()
			if err != nil {
				return fmt.Errorf("failed to obtain PipeWire remote connection: %w", err)
			}
			extraFiles = []*os.File{pwFile}

			// Stable PipeWire screen capture pipeline:
			// - always-copy=true: releases PipeWire compositor buffer immediately, preventing buffer starvation in cosmic-comp
			// - min-buffers=1 max-buffers=2: enforces strict double-buffering so cosmic-comp never receives concurrent create_frame requests
			// - do-timestamp=true: assign stream timestamps to buffers
			// - videorate drop-only=true skip-to-first=true: cleanly caps framerate at p.fps without frame duplication
			// - videoconvert: standardizes any compositor buffer format into NV12
			// - queue leaky=downstream: decouples capture from hardware encoding
			pipelineStr = fmt.Sprintf(
				"pipewiresrc fd=3 path=%d always-copy=true min-buffers=1 max-buffers=2 do-timestamp=true ! "+
					"videorate drop-only=true skip-to-first=true max-rate=%d ! "+
					"videoconvert ! "+
					"video/x-raw,format=NV12 ! "+
					"queue max-size-buffers=2 leaky=downstream ! "+
					"%s ! "+
					"video/x-h264,profile=constrained-baseline ! "+
					"h264parse config-interval=-1 ! "+
					"video/x-h264,stream-format=byte-stream,alignment=au ! "+
					"fdsink fd=1 sync=false",
				nodeID, p.fps, tier.encoder,
			)
		} else {
			// Autonomous synthetic test pattern pipeline (no desktop capture / portal needed)
			pipelineStr = fmt.Sprintf(
				"videotestsrc is-live=true pattern=smpte ! "+
					"video/x-raw,framerate=%d/1,width=1920,height=1080 ! "+
					"videoconvert ! "+
					"video/x-raw,format=NV12 ! "+
					"queue max-size-buffers=2 leaky=downstream ! "+
					"%s ! "+
					"video/x-h264,profile=constrained-baseline ! "+
					"h264parse config-interval=-1 ! "+
					"video/x-h264,stream-format=byte-stream,alignment=au ! "+
					"fdsink fd=1 sync=false",
				p.fps, tier.encoder,
			)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "gst-launch-1.0", strings.Fields(pipelineStr)...)
		cmd.ExtraFiles = extraFiles

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			if pwFile != nil {
				_ = pwFile.Close()
			}
			cancel()
			lastErr = err
			continue
		}

		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		err = cmd.Start()
		if pwFile != nil {
			_ = pwFile.Close()
		}

		if err != nil {
			cancel()
			lastErr = err
			log.Printf("[capture] Failed to start pipeline with %s: %v", tier.name, err)
			continue
		}

		p.cmd = cmd
		p.cancel = cancel

		// Read loop goroutine
		childDone := make(chan struct{})
		go func(encSelected string, stderrBuf *bytes.Buffer) {
			defer func() {
				_ = cmd.Wait()
				if stderrBuf.Len() > 0 {
					log.Printf("[capture] GStreamer output: %s", strings.TrimSpace(stderrBuf.String()))
				}
				close(childDone)
				// Signal supervisor that process has terminated
				select {
				case p.processDone <- struct{}{}:
				default:
				}
			}()

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
		}(tier.name, &stderr)

		// Verify pipeline survives preroll before declaring success
		select {
		case <-childDone:
			cancel()
			stderrMsg := strings.TrimSpace(stderr.String())
			lastErr = fmt.Errorf("%s failed to preroll: %s", tier.name, stderrMsg)
			log.Printf("[capture] %s failed to preroll: %s", tier.name, stderrMsg)
			continue
		case <-time.After(350 * time.Millisecond):
			// Pipeline successfully prerolled and is streaming
		}

		log.Printf("[capture] Capture pipeline running with %s", tier.name)
		return nil
	}

	return fmt.Errorf("failed to start any capture pipeline: %v", lastErr)
}

// Stop shuts down the capture pipeline.
func (p *Pipeline) Stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	close(p.quit)

	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.mu.Unlock()
}

// Wait blocks until the capture pipeline terminates.
func (p *Pipeline) Wait() {
	<-p.done
}
