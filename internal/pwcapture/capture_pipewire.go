//go:build linux && pipewire

package pwcapture

/*
#cgo CFLAGS: -I/usr/include/pipewire-0.3 -I/usr/include/spa-0.2 -D_REENTRANT
#cgo LDFLAGS: -ldl -lpthread
#include <stdlib.h>
#include "shim.h"
*/
import "C"

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
	"unsafe"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

// captureAvailable dlopens libpipewire-0.3 (once, in the shim) and reports whether it resolved.
func captureAvailable() bool { return C.rpw_available() != 0 }

// openCapture starts a PipeWire capture of nodeName and returns it as a medialink.Source. Frames
// are RGBA (CodecNRGBA). The returned source's Next blocks until a frame or ctx cancel.
func openCapture(_ context.Context, log *logbus.Bus, nodeName string) (medialink.Source, error) {
	cname := C.CString(nodeName)
	defer C.free(unsafe.Pointer(cname))
	h := C.rpw_start(cname)
	if h == nil {
		return nil, fmt.Errorf("pwcapture: could not start capture of %q (libpipewire missing or connect failed)", nodeName)
	}
	return &pwSource{h: h, log: log, node: nodeName}, nil
}

// pwSource is one PipeWire capture exposed as a medialink.Source. Bounded: one latest-frame slot
// lives in the shim (drop-oldest + counters); the Go side holds a single reusable RGBA buffer.
// Next/Close are called serially by the route pump, so the buffer is returned without a copy and
// recycled on the next Next (Release is nil - GC owns the buffer, the consumer must not retain it,
// which the encode feeder doesn't).
type pwSource struct {
	h    *C.struct_rpw_capture
	log  *logbus.Bus
	node string

	mu     sync.Mutex
	buf    []byte
	closed bool
}

// Next implements medialink.Source.
func (s *pwSource) Next(ctx context.Context) (*medialink.Frame, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, io.EOF
		}
		if s.buf == nil {
			s.buf = make([]byte, 1920*1080*4) // grown on -2
		}
		var w, hh C.int
		n := int(C.rpw_wait_frame(s.h, (*C.uchar)(unsafe.Pointer(&s.buf[0])), C.int(len(s.buf)), &w, &hh, 200))
		buf := s.buf
		s.mu.Unlock()
		switch {
		case n > 0:
			return &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
				Payload: buf[:n], PTS: time.Now().UnixNano()}, nil
		case n == 0:
			continue // timeout: re-check ctx + the producer may be mid-reconnect (VRChat restart)
		case n == -2: // buffer too small - grow to the real frame size and retry
			s.mu.Lock()
			need := int(w) * int(hh) * 4
			if need > len(s.buf) {
				s.buf = make([]byte, need)
			}
			s.mu.Unlock()
			continue
		default: // -1: fatal stream error (core disconnect)
			if s.log != nil {
				s.log.Warn(source, "PipeWire capture stream error - ending route", map[string]any{"node": s.node})
			}
			return nil, io.EOF
		}
	}
}

// Close implements medialink.Source.
func (s *pwSource) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	h := s.h
	s.mu.Unlock()
	if s.log != nil {
		s.log.Debug(source, "PipeWire capture closed", map[string]any{
			"node": s.node, "dropped": int64(C.rpw_dropped(h)), "dmabufSkipped": int64(C.rpw_dmabuf_skipped(h))})
	}
	C.rpw_stop(h) // blocks until the loop thread stops; no callbacks after this
	return nil
}
