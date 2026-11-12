//go:build linux && pipewire

package pwcapture

/*
#cgo CFLAGS: -I/usr/include/pipewire-0.3 -I/usr/include/spa-0.2 -D_REENTRANT
#cgo LDFLAGS: -ldl -lpthread
#include <stdlib.h>
#include "shim.h"
#include "vaenc.h"
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
	"rave.page/mate/internal/ratewin"
)

// captureAvailable dlopens libpipewire-0.3 (once, in the shim) and reports whether it resolved.
func captureAvailable() bool { return C.rpw_available() != 0 }

// VAEncodeAvailable reports whether the box can run the zero-copy dmabuf→VA-API encoder (libva loads
// AND PipeWire capture is usable). The media child advertises the vaapi-native encoder names only
// when this holds.
func VAEncodeAvailable() bool { return captureAvailable() && C.rva_available() != 0 }

// compile-time: pwSource serves the raw path AND the vaapi-native encode path + its control seams.
var (
	_ medialink.Source             = (*pwSource)(nil)
	_ medialink.DMABufEncodeSource = (*pwSource)(nil)
	_ medialink.KeyframeSource     = (*pwSource)(nil)
	_ medialink.RateControlSource  = (*pwSource)(nil)
	_ medialink.PipelineReporter   = (*pwSource)(nil)
)

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
	aubuf  []byte // reusable Annex-B AU buffer (encode mode)
	closed bool
	done   chan struct{} // closed on Close; stops the encode telemetry goroutine

	// vaapi-native encode mode (StartDMABufEncode). Once set, Next returns compressed AUs.
	encMode  bool
	encCodec medialink.Codec
	encName  string // negotiated engine name (PipelineStats.Encoder)
	encNode  string // VA render node
	lastMaxH int    // last MaxHeight applied via SetRateHint (coded-height cap; 0 = native)

	// telemetry: windowed rates ticked per AU, so OutFPS/CapFPS/bytes-per-frame are accurate on ANY
	// single read (the route's ctl stats poll once) - the MF bridge does the same. Earlier the engine
	// computed read-time deltas, which read 0 on a single poll (the "fps out=0" bug).
	outRate     ratewin.Ring // emitted AUs: OutFPS + bytes/frame + AU totals
	capRate     ratewin.Ring // dmabuf frames captured (from the C cap_frames counter delta): CapFPS
	lastCapTick int64        // last cap_frames seen; nextAU is called serially by the pump (no lock)
}

// Next implements medialink.Source. In vaapi-native encode mode it returns compressed Annex-B AUs
// (CodecH264/HEVC, keyframe flag); otherwise raw NRGBA frames.
func (s *pwSource) Next(ctx context.Context) (*medialink.Frame, error) {
	if s.encMode {
		return s.nextAU(ctx)
	}
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
		var cpts C.longlong
		n := int(C.rpw_wait_frame(s.h, (*C.uchar)(unsafe.Pointer(&s.buf[0])), C.int(len(s.buf)), &w, &hh, &cpts, 200))
		buf := s.buf
		s.mu.Unlock()
		switch {
		case n > 0:
			pts := int64(cpts) // capture-clock PTS from the buffer; 0 => stamp now
			if pts == 0 {
				pts = time.Now().UnixNano()
			}
			return &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
				Payload: buf[:n], PTS: pts}, nil
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
	if s.done != nil {
		close(s.done)
	}
	s.mu.Unlock()
	if s.log != nil {
		s.log.Debug(source, "PipeWire capture closed", map[string]any{
			"node": s.node, "dropped": int64(C.rpw_dropped(h)), "dmabufSkipped": int64(C.rpw_dmabuf_skipped(h))})
	}
	C.rpw_stop(h) // blocks until the loop thread stops; no callbacks after this
	return nil
}

// StartDMABufEncode implements medialink.DMABufEncodeSource: switch this capture to the fused
// zero-copy dmabuf→VA→H.264/HEVC path (frames never become Go pixels) and return itself as the
// AU-producing Source. ok=false when VA is unavailable - the caller falls back to readback+ffmpeg.
func (s *pwSource) StartDMABufEncode(spec medialink.EncodeSpec) (medialink.Source, bool, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, false, io.EOF
	}
	s.mu.Unlock()
	codec := 0 // H.264
	if spec.Codec == medialink.CodecHEVC {
		codec = 1
	}
	fps := int(spec.FPS + 0.5)
	node := spec.VAAPIRenderNode
	var cnode *C.char
	if node != "" {
		cnode = C.CString(node)
		defer C.free(unsafe.Pointer(cnode))
	}
	if C.rpw_set_encode(s.h, C.int(codec), C.int(spec.BitrateKbps), C.int(spec.MaxHeight), C.int(fps), cnode) == 0 {
		return nil, false, nil // VA unavailable (or HEVC not yet supported by the native engine)
	}
	s.mu.Lock()
	s.encMode = true
	s.encCodec = spec.Codec
	s.encName = spec.Encoder
	s.encNode = node
	s.lastMaxH = spec.MaxHeight // initial coded cap applied at engage; SetRateHint diffs against it
	if s.done == nil {
		s.done = make(chan struct{})
	}
	s.mu.Unlock()
	go s.encTelemetry()
	C.rpw_request_keyframe(s.h) // route start: open with an IDR so the receiver decodes immediately
	if s.log != nil {
		s.log.Info(source, "vaapi-native encode engaged", map[string]any{
			"node": s.node, "encoder": spec.Encoder, "in": sizeStr(spec.Width, spec.Height),
			"maxHeight": spec.MaxHeight, "kbps": spec.BitrateKbps, "renderNode": node})
	}
	return s, true, nil
}

// nextAU returns one compressed AU in encode mode.
func (s *pwSource) nextAU(ctx context.Context) (*medialink.Frame, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, io.EOF
		}
		if s.aubuf == nil {
			s.aubuf = make([]byte, 1<<20) // grown on -2
		}
		var w, hh, kf C.int
		var cpts C.longlong
		n := int(C.rpw_wait_au(s.h, (*C.uchar)(unsafe.Pointer(&s.aubuf[0])), C.int(len(s.aubuf)), &w, &hh, &kf, &cpts, 200))
		buf := s.aubuf
		s.mu.Unlock()
		switch {
		case n > 0:
			now := time.Now()
			s.outRate.Add(uint64(n), 1, now) // bytes + 1 AU
			cap := int64(C.rpw_cap_frames(s.h))
			if d := cap - s.lastCapTick; d > 0 {
				s.capRate.Add(0, uint64(d), now)
			}
			s.lastCapTick = cap
			pts := int64(cpts) // capture-clock PTS from the buffer; 0 => stamp now
			if pts == 0 {
				pts = now.UnixNano()
			}
			f := &medialink.Frame{Kind: medialink.KindVideo, Codec: s.encCodec,
				Payload: buf[:n], PTS: pts}
			if kf != 0 {
				f.Flags |= medialink.FlagKeyframe
			}
			return f, nil
		case n == 0:
			continue // timeout / mid-reconnect
		case n == -2: // AU bigger than the buffer - grow and retry
			s.mu.Lock()
			need := int(w) * int(hh) * 4
			if need > len(s.aubuf) {
				s.aubuf = make([]byte, need)
			}
			s.mu.Unlock()
			continue
		default: // -1: fatal
			if s.log != nil {
				s.log.Warn(source, "PipeWire encode stream error - ending route", map[string]any{"node": s.node})
			}
			return nil, io.EOF
		}
	}
}

// RequestKeyframe implements medialink.KeyframeSource: force an IDR on the next encoded frame (PLI).
func (s *pwSource) RequestKeyframe() {
	if s.encMode {
		C.rpw_request_keyframe(s.h)
	}
}

// SetRateHint implements medialink.RateControlSource: the receiver's VRAM governor caps our output.
// Bitrate is the live lever (the corrector-scaled CBR target, no reopen). MaxHeight is now LIVE: the
// encoder re-creates at the capped CODED size (nominal source geometry unchanged - the receiver
// upscales). MaxFPS stays reserved (logged).
func (s *pwSource) SetRateHint(h medialink.RateHint) {
	if !s.encMode {
		return
	}
	if h.MaxBitrateKbps > 0 {
		C.rpw_set_bitrate(s.h, C.int(h.MaxBitrateKbps))
	}
	if h.MaxHeight != s.lastMaxH { // 0 = restore native coded size
		s.lastMaxH = h.MaxHeight
		C.rpw_set_cap_height(s.h, C.int(h.MaxHeight))
	}
	if h.MaxFPS > 0 && s.log != nil {
		s.log.Debug(source, "rate hint: fps cap not yet live on the native path (reserved); applied bitrate + height", map[string]any{
			"maxFPS": h.MaxFPS, "maxHeight": h.MaxHeight, "maxBitrateKbps": h.MaxBitrateKbps})
	}
}

// PipeStats implements medialink.PipelineReporter: engine, coded size, capture+encode fps, dropped,
// import path and the content oracle (bytes-per-frame). fps/bytes-per-frame are deltas between calls.
func (s *pwSource) PipeStats() medialink.PipelineStats {
	if !s.encMode {
		return medialink.PipelineStats{}
	}
	drops := uint64(C.rpw_enc_drops(s.h))
	importOK := C.rpw_import_ok(s.h) != 0
	now := time.Now()
	_, outFPS := s.outRate.Rate(now) // AUs/s leaving the encoder (windowed; accurate on one read)
	_, capFPS := s.capRate.Rate(now) // dmabuf frames/s captured
	bpf := s.outRate.PerEvent(now)   // bytes per AU (content oracle)
	auBytes, auCount := s.outRate.Totals()
	return medialink.PipelineStats{
		Encoder: s.encName, OutFPS: outFPS, CapFPS: capFPS,
		Dropped: drops, ZeroCopy: importOK,
		AUBytes: auBytes, AUCount: auCount, AUBytesPerFrame: bpf,
		DevPolicy: s.encNode,
	}
}

// encTelemetry logs one line per interval with the engine, coded size, capture/encode fps, dropped,
// import path and render node - the log-only diagnosis channel (item 7), complete on a HEALTHY route.
func (s *pwSource) encTelemetry() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		st := s.PipeStats()
		cw, ch := int(C.rpw_coded_w(s.h)), int(C.rpw_coded_h(s.h))
		importPath := "egl-readback"
		if C.rpw_import_ok(s.h) != 0 {
			importPath = "va-dmabuf"
		}
		if s.log != nil {
			s.log.Info(source, "route encode telemetry", map[string]any{
				"engine": st.Encoder, "coded": sizeStr(cw, ch), "import": importPath,
				"renderNode": s.encNode, "capFps": fmtf(st.CapFPS), "encFps": fmtf(st.OutFPS),
				"dropped": st.Dropped, "bytesPerFrame": uint64(st.AUBytesPerFrame),
				"reconnects": int64(C.rpw_reconnects(s.h))})
		}
	}
}

// fmtf is a tiny one-decimal formatter (avoids pulling fmt into the hot path).
func fmtf(v float64) string { return fmt.Sprintf("%.1f", v) }
