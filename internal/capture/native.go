package capture

/*
#cgo CFLAGS: -I/usr/include/pipewire-0.3 -I/usr/include/spa-0.2
#cgo LDFLAGS: -lpipewire-0.3 -lavcodec -lswscale -lavutil -lva -lva-drm
#include "native_capture.h"
*/
import "C"
import (
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var (
	nativeHandleSeq uintptr
	nativePipelines sync.Map // map[uintptr]*NativePipeline
)

//export goOnNativeFrame
func goOnNativeFrame(data *C.uint8_t, size C.size_t, isKey C.bool, goHandle C.uintptr_t) {
	val, ok := nativePipelines.Load(uintptr(goHandle))
	if !ok {
		return
	}
	pipeline := val.(*NativePipeline)
	if pipeline.onFrame != nil {
		buf := C.GoBytes(unsafe.Pointer(data), C.int(size))
		pipeline.onFrame(buf, bool(isKey))
	}
}

//export goOnNativeState
func goOnNativeState(oldState C.int, newState C.int, goHandle C.uintptr_t) {
	val, ok := nativePipelines.Load(uintptr(goHandle))
	if !ok {
		return
	}
	pipeline := val.(*NativePipeline)
	pipeline.handleStateChange(int(oldState), int(newState))
}

// NativePipeline manages direct PipeWire stream capture and hardware VA-API encoding without GStreamer.
type NativePipeline struct {
	sessionProvider SessionProvider
	pwFile          *os.File
	nodeID          uint32
	fps             int
	qp              int
	handle          uintptr
	cCap            *C.native_capture_t
	onFrame         func(frame []byte, isKey bool)
	mu              sync.Mutex
	stopped         bool

	pauseMu    sync.Mutex
	pauseTimer *time.Timer
	reviveChan chan struct{}
	quit       chan struct{}
}

// NewNativePipeline creates a new native capture pipeline using libpipewire and libavcodec.
func NewNativePipeline(sessionProvider SessionProvider, fps int, qp int) *NativePipeline {
	if qp <= 0 {
		qp = 26
	}
	if fps <= 0 {
		fps = 30
	}
	handle := atomic.AddUintptr(&nativeHandleSeq, 1)
	p := &NativePipeline{
		sessionProvider: sessionProvider,
		fps:             fps,
		qp:              qp,
		handle:          handle,
		reviveChan:      make(chan struct{}, 1),
		quit:            make(chan struct{}),
	}
	nativePipelines.Store(handle, p)

	// Supervisor loop: auto-recovers if compositor pauses or detaches the stream node
	go p.supervisorLoop()

	return p
}

func (p *NativePipeline) handleStateChange(oldState, newState int) {
	// PipeWire states: -1 = ERROR, 0 = UNCONNECTED, 1 = CONNECTING, 2 = PAUSED, 3 = STREAMING
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()

	if newState == 3 {
		// Streaming active, cancel any pending revive
		if p.pauseTimer != nil {
			p.pauseTimer.Stop()
			p.pauseTimer = nil
		}
		return
	}

	if newState <= 1 {
		// Fatal loss of PipeWire connection or portal node destroyed -> revive immediately
		if p.pauseTimer != nil {
			p.pauseTimer.Stop()
			p.pauseTimer = nil
		}
		select {
		case p.reviveChan <- struct{}{}:
		default:
		}
		return
	}

	if oldState == 3 && newState == 2 {
		// Stream paused by compositor (e.g. fullscreen transition or direct scanout switch).
		// Wait 100ms: if stream doesn't resume on its own, trigger revive.
		if p.pauseTimer != nil {
			p.pauseTimer.Stop()
		}
		p.pauseTimer = time.AfterFunc(100*time.Millisecond, func() {
			select {
			case p.reviveChan <- struct{}{}:
			default:
			}
		})
	}
}

func (p *NativePipeline) supervisorLoop() {
	for {
		select {
		case <-p.quit:
			return
		case <-p.reviveChan:
			p.mu.Lock()
			if p.stopped {
				p.mu.Unlock()
				return
			}
			log.Printf("[native] ScreenCast stream suspended or terminated. Reviving session via restore token...")

			if p.cCap != nil {
				C.native_capture_stop(p.cCap)
				C.native_capture_free(p.cCap)
				p.cCap = nil
			}
			if p.pwFile != nil {
				_ = p.pwFile.Close()
				p.pwFile = nil
			}

			if p.sessionProvider != nil {
				pwFile, nodeID, err := p.sessionProvider()
				if err != nil {
					log.Printf("[native] Revive session acquisition error: %v, retrying in 300ms...", err)
					p.mu.Unlock()
					time.Sleep(300 * time.Millisecond)
					select {
					case p.reviveChan <- struct{}{}:
					default:
					}
					continue
				}

				p.pwFile = pwFile
				p.nodeID = nodeID
				fd := C.int(p.pwFile.Fd())
				cCap := C.native_capture_create(fd, C.uint32_t(p.nodeID), C.int(p.fps), C.int(p.qp), C.uintptr_t(p.handle))
				if cCap == nil || C.native_capture_start(cCap) != 0 {
					log.Printf("[native] Revive native_capture_start failed, retrying...")
					if cCap != nil {
						C.native_capture_free(cCap)
					}
					p.mu.Unlock()
					time.Sleep(300 * time.Millisecond)
					select {
					case p.reviveChan <- struct{}{}:
					default:
					}
					continue
				}
				p.cCap = cCap
				log.Printf("[native] Successfully revived native capture on node %d", p.nodeID)
			}
			p.mu.Unlock()
		}
	}
}

// Start activates the native capture and encoding loop.
func (p *NativePipeline) Start(onFrame func(frame []byte, isKey bool)) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.onFrame = onFrame
	p.stopped = false

	if p.sessionProvider != nil && p.pwFile == nil {
		pwFile, nodeID, err := p.sessionProvider()
		if err != nil {
			return fmt.Errorf("failed to obtain ScreenCast session: %w", err)
		}
		p.pwFile = pwFile
		p.nodeID = nodeID
	}

	if p.pwFile == nil {
		return fmt.Errorf("no PipeWire file descriptor available")
	}

	fd := C.int(p.pwFile.Fd())
	nodeID := C.uint32_t(p.nodeID)
	fps := C.int(p.fps)
	qp := C.int(p.qp)
	handle := C.uintptr_t(p.handle)

	p.cCap = C.native_capture_create(fd, nodeID, fps, qp, handle)
	if p.cCap == nil {
		return fmt.Errorf("failed to create native capture instance")
	}

	ret := C.native_capture_start(p.cCap)
	if ret != 0 {
		C.native_capture_free(p.cCap)
		p.cCap = nil
		return fmt.Errorf("failed to start native capture (code %d)", int(ret))
	}

	return nil
}

// Stop gracefully shuts down the capture pipeline.
func (p *NativePipeline) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return
	}
	p.stopped = true
	close(p.quit)

	p.pauseMu.Lock()
	if p.pauseTimer != nil {
		p.pauseTimer.Stop()
		p.pauseTimer = nil
	}
	p.pauseMu.Unlock()

	if p.cCap != nil {
		C.native_capture_stop(p.cCap)
		C.native_capture_free(p.cCap)
		p.cCap = nil
	}

	nativePipelines.Delete(p.handle)

	if p.pwFile != nil {
		_ = p.pwFile.Close()
		p.pwFile = nil
	}
}
