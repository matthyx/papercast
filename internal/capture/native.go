package capture

/*
#cgo CFLAGS: -I/usr/include/pipewire-0.3 -I/usr/include/spa-0.2
#cgo LDFLAGS: -lpipewire-0.3 -lavcodec -lswscale -lavutil -lva -lva-drm
#include "native_capture.h"
*/
import "C"
import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
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

// NativePipeline manages direct PipeWire stream capture and hardware VA-API encoding without GStreamer.
type NativePipeline struct {
	pwFile   *os.File
	nodeID   uint32
	fps      int
	qp       int
	handle   uintptr
	cCap     *C.native_capture_t
	onFrame  func(frame []byte, isKey bool)
	mu       sync.Mutex
	stopped  bool
}

// NewNativePipeline creates a new native capture pipeline using libpipewire and libavcodec.
func NewNativePipeline(pwFile *os.File, nodeID uint32, fps int, qp int) *NativePipeline {
	if qp <= 0 {
		qp = 26
	}
	if fps <= 0 {
		fps = 30
	}
	handle := atomic.AddUintptr(&nativeHandleSeq, 1)
	p := &NativePipeline{
		pwFile: pwFile,
		nodeID: nodeID,
		fps:    fps,
		qp:     qp,
		handle: handle,
	}
	nativePipelines.Store(handle, p)
	return p
}

// Start activates the native capture and encoding loop.
func (p *NativePipeline) Start(onFrame func(frame []byte, isKey bool)) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.onFrame = onFrame
	p.stopped = false

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
