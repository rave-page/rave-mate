//go:build windows && cgo

// Package mfenc is the native Media Foundation HARDWARE video encoder: raw RGBA frames
// go to the GPU once (D3D11 upload → VideoProcessor CSC/scale → NVENC/AMF/QSV silicon
// via the async encoder MFT) and come back as annex-B H.264 access units. No ffmpeg
// child, no multi-GB/s stdin pipe. One OS thread owns each pipeline (COM MTA).
package mfenc

// #cgo CXXFLAGS: -O2
// #cgo LDFLAGS: -lmfplat -lole32 -ld3d11 -ldxgi -loleaut32
// #include <stdlib.h>
// #include "mf_shim.h"
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

// AU is one encoded access unit (annex-B, parameter sets in-band on keyframes).
type AU struct {
	Data     []byte
	PTSNs    int64
	Keyframe bool
}

var availOnce sync.Once
var availOK bool

// Available reports whether a D3D11 hardware device + hardware H.264 encoder MFT exist
// (probed once; runs on its own locked thread so COM state never leaks into the caller).
func Available() bool {
	availOnce.Do(func() {
		done := make(chan bool, 1)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			done <- C.mf_shim_available() == 1
		}()
		availOK = <-done
	})
	return availOK
}

// Encoder is one live pipeline. Encode/ForceKeyframe/Close are safe from any goroutine
// (marshalled to the owner thread); AUs arrive on Output.
type Encoder struct {
	feedCh  chan feedReq
	ctrlCh  chan ctrlReq
	out     chan AU
	done    chan struct{}
	name    string
	bgraIn  bool
	closeMu sync.Mutex
	closed  bool
}

type feedReq struct {
	pix   []byte
	ptsNs int64
	err   chan error
}

type ctrlReq struct {
	kind int // 1=forceIDR 2=close
	done chan struct{}
}

// New builds a hardware pipeline on the DEFAULT adapter: inW/inH source dims, outW/outH
// encode dims (caller clamps/evens; VP scales when different), fps, bitrate kbps, gop frames.
func New(inW, inH, outW, outH int, fps float64, bitrateKbps, gopFrames int) (*Encoder, error) {
	return NewOn(0, inW, inH, outW, outH, fps, bitrateKbps, gopFrames)
}

// NewOn is New pinned to one GPU: adapterLUID is the DXGI AdapterLuid packed HighPart<<32|LowPart
// (encoderscan.LUIDInt64); 0 = default adapter. An adapter that cannot host the pipeline degrades
// to the default one inside the shim - a device preference never kills a route.
func NewOn(adapterLUID int64, inW, inH, outW, outH int, fps float64, bitrateKbps, gopFrames int) (*Encoder, error) {
	if fps <= 0 {
		fps = 30
	}
	e := &Encoder{
		feedCh: make(chan feedReq),
		ctrlCh: make(chan ctrlReq),
		out:    make(chan AU, 8), // bounded: a full channel blocks the feeder (paced upstream)
		done:   make(chan struct{}),
	}
	openErr := make(chan error, 1)
	go e.run(adapterLUID, inW, inH, outW, outH, fps, bitrateKbps, gopFrames, openErr)
	if err := <-openErr; err != nil {
		return nil, err
	}
	return e, nil
}

// Output yields encoded AUs; closed after Close drains the pipeline.
func (e *Encoder) Output() <-chan AU { return e.out }

// Name returns the active encoder MFT's friendly name (diagnostic).
func (e *Encoder) Name() string { return e.name }

// InputIsBGRA reports whether the VP negotiated ARGB32 (shim swizzles; diagnostic).
func (e *Encoder) InputIsBGRA() bool { return e.bgraIn }

// Encode feeds one RGBA frame (len must be inW*inH*4); blocks at the pipeline's pace.
func (e *Encoder) Encode(rgba []byte, ptsNs int64) error {
	req := feedReq{pix: rgba, ptsNs: ptsNs, err: make(chan error, 1)}
	select {
	case e.feedCh <- req:
		return <-req.err
	case <-e.done:
		return errors.New("mfenc: closed")
	}
}

// ForceKeyframe requests an IDR on the next fed frame (live - no encoder restart).
func (e *Encoder) ForceKeyframe() {
	req := ctrlReq{kind: 1, done: make(chan struct{})}
	select {
	case e.ctrlCh <- req:
		<-req.done
	case <-e.done:
	}
}

// Close drains + tears down the pipeline; Output closes after the tail AUs.
func (e *Encoder) Close() {
	e.closeMu.Lock()
	defer e.closeMu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	req := ctrlReq{kind: 2, done: make(chan struct{})}
	select {
	case e.ctrlCh <- req:
		<-req.done
	case <-e.done:
	}
}

// run owns the pipeline on one locked OS thread: open, serve feed/ctrl, drain, close.
func (e *Encoder) run(adapterLUID int64, inW, inH, outW, outH int, fps float64, kbps, gop int, openErr chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	fpsN, fpsD := fpsRational(fps)
	errbuf := (*C.char)(C.malloc(256))
	defer C.free(unsafe.Pointer(errbuf))
	h := C.mf_enc_open(C.int64_t(adapterLUID), C.int(inW), C.int(inH), C.int(outW), C.int(outH),
		C.int(fpsN), C.int(fpsD), C.int(kbps), C.int(gop), errbuf, 256)
	if h == nil {
		openErr <- fmt.Errorf("mfenc: %s", C.GoString(errbuf))
		close(e.done)
		close(e.out)
		return
	}
	nb := (*C.char)(C.malloc(128))
	C.mf_enc_name(h, nb, 128)
	e.name = C.GoString(nb)
	C.free(unsafe.Pointer(nb))
	e.bgraIn = C.mf_enc_input_is_bgra(h) == 1
	openErr <- nil

	buf := make([]byte, 1<<20) // AU copy target; grows on demand
	pump := func() {           // drain every pending AU into the out channel
		for {
			var pts C.int64_t
			var key C.int
			n := C.mf_enc_next(h, (*C.uint8_t)(unsafe.Pointer(&buf[0])), C.int(len(buf)), &pts, &key)
			if n == 0 {
				return
			}
			if n < 0 {
				need := int(-n)
				if need <= len(buf) {
					return // hard error, drop pump (feed will surface it)
				}
				buf = make([]byte, need+need/2)
				continue
			}
			au := AU{Data: append([]byte(nil), buf[:int(n)]...), PTSNs: int64(pts) * 100, Keyframe: key == 1}
			select {
			case e.out <- au:
			case <-e.done:
				return
			}
		}
	}

	for {
		select {
		case req := <-e.feedCh:
			if len(req.pix) < inW*inH*4 {
				req.err <- fmt.Errorf("mfenc: short frame %d < %d", len(req.pix), inW*inH*4)
				continue
			}
			rc := C.mf_enc_feed(h, (*C.uint8_t)(unsafe.Pointer(&req.pix[0])), C.int(inW*4), C.int64_t(req.ptsNs/100))
			if rc != 0 {
				req.err <- fmt.Errorf("mfenc: feed rc=%d hr=0x%08x", int(rc), uint32(C.mf_enc_last_hr(h)))
			} else {
				req.err <- nil
			}
			pump()
		case req := <-e.ctrlCh:
			switch req.kind {
			case 1:
				C.mf_enc_force_idr(h)
				close(req.done)
			case 2:
				C.mf_enc_drain(h)
				pump()
				C.mf_enc_close(h)
				close(e.done)
				close(e.out)
				close(req.done)
				return
			}
		}
	}
}

// fpsRational maps common fractional rates to exact rationals (29.97 → 30000/1001).
func fpsRational(fps float64) (int, int) {
	switch {
	case fps > 29.9 && fps < 29.98:
		return 30000, 1001
	case fps > 59.9 && fps < 59.95:
		return 60000, 1001
	case fps > 23.9 && fps < 23.99:
		return 24000, 1001
	}
	if fps == float64(int(fps)) {
		return int(fps), 1
	}
	return int(fps*1000 + 0.5), 1000
}

// SwizzleRGBAToBGRA is the shim's upload swizzle (exposed for the color canary test).
func SwizzleRGBAToBGRA(dst, src []byte) {
	n := len(src) / 4
	if len(dst) < n*4 || n == 0 {
		return
	}
	C.mf_swizzle_rgba_bgra((*C.uint8_t)(unsafe.Pointer(&dst[0])), (*C.uint8_t)(unsafe.Pointer(&src[0])), C.int(n))
}
