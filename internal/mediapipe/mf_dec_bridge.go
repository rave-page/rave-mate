package mediapipe

// mf_dec_bridge.go - native Media Foundation hardware DECODE as a medialink Sink (zigmedia inc 2).
// The receive-side mirror of mf_bridge.go: compressed AUs go over a shared-memory ring to the same
// supervised per-adapter Zig child, which decodes them and blits each frame straight into the local
// video-share sender's shared texture. No ffmpeg child, no 33 MB-per-frame stdout pipe, no second
// GPU upload - and a vendor-driver fault kills only the child (the supervisor re-places the
// session). ffmpeg stays the fallback engine and the parity reference.

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mfenc"
)

// ZeroCopyDecode gates the native decode path. Package seam (same shape as ZeroCopyCapture) - the
// daemon and the media child both point it at their live config. Default OFF.
var ZeroCopyDecode = func() bool { return false }

// decRingKbpsHint sizes the inbound AU ring when the route negotiated no bitrate: 20 Mbps is
// mediaroute's default budget, and the ring rule (half a second, 4-16 MiB) floors at 4 MiB anyway.
const decRingKbpsHint = 20000

// mfDecoder implements medialink.Sink (+PipelineReporter) over an mfenc decode session.
type mfDecoder struct {
	log  *logbus.Bus
	dec  *mfenc.ProcDecSession
	sink medialink.Sink // the destination sink: it OWNS the sender; foreign frames pass through
	spec medialink.DecodeSpec

	needKey atomic.Bool   // after open/re-place the decoder needs an IDR first
	dropped atomic.Uint64 // AUs dropped awaiting a keyframe
	closed  atomic.Bool
	opened  time.Time
}

// newMFDecoder opens the native decode session for spec, publishing into sink's shared texture.
// Error = the caller runs the ffmpeg decode child (byte-identical to today).
func newMFDecoder(_ context.Context, log *logbus.Bus, spec medialink.DecodeSpec, sink medialink.Sink) (*mfDecoder, error) {
	if !ZeroCopyDecode() {
		return nil, errors.New("mediapipe: native decode not enabled")
	}
	hevc, ok := decCodecSupported(spec.Codec)
	if !ok {
		return nil, fmt.Errorf("mediapipe: codec %v has no native decode path", spec.Codec)
	}
	zcs, ok := sink.(medialink.ZeroCopySink)
	if !ok {
		return nil, errors.New("mediapipe: sink exposes no GPU destination texture")
	}
	h, _, w, hh, name, ok := zcs.SharedTexture()
	if !ok || h == 0 {
		return nil, errors.New("mediapipe: destination sender has no shared texture")
	}
	if mfenc.DecodePinnedToFrames(name) {
		return nil, fmt.Errorf("mediapipe: destination %q is pinned to the frame path", name)
	}
	if spec.Width <= 0 || spec.Height <= 0 {
		return nil, fmt.Errorf("mediapipe: bad decode size %dx%d", spec.Width, spec.Height)
	}
	d, err := mfenc.OpenProcDecSession(mfenc.ProcDecOpts{
		InW: spec.Width, InH: spec.Height, OutW: w, OutH: hh, FPS: spec.FPS, HEVC: hevc,
		KbpsHint: decRingKbpsHint,
		Dest: &mfenc.DecodeDest{Name: name, Resolve: func() (uint64, uint32, int, int, bool) {
			hd, f, ww, hgt, _, ok := zcs.SharedTexture()
			return hd, f, ww, hgt, ok
		}},
	})
	if err != nil {
		return nil, err
	}
	md := &mfDecoder{log: log, dec: d, sink: sink, spec: spec, opened: time.Now()}
	md.needKey.Store(true)
	log.Info(source, "native MF hardware decode (isolated child, GPU-resident publish)", map[string]any{
		"decoder": d.Name(), "in": fmt.Sprintf("%dx%d", spec.Width, spec.Height),
		"dest": fmt.Sprintf("%s %dx%d", name, w, hh), "hardwareMFT": d.IsHardware()})
	return md, nil
}

// decCodecSupported maps a wire codec to the native decoder; ok=false = use ffmpeg.
func decCodecSupported(c medialink.Codec) (hevc bool, ok bool) {
	switch c {
	case medialink.CodecH264:
		return false, true
	case medialink.CodecHEVC:
		return true, true
	}
	return false, false
}

// Write implements medialink.Sink: hand the AU to the child. Frames of ANOTHER codec pass through to
// the inner sink untouched (same defensive degrade as the ffmpeg decoder - e.g. a raw route that
// never negotiated encode).
func (m *mfDecoder) Write(f *medialink.Frame) error {
	if f.Codec != m.spec.Codec {
		return m.sink.Write(f)
	}
	if m.closed.Load() {
		return nil
	}
	key := f.Keyframe()
	if m.needKey.Load() {
		if !key {
			m.dropped.Add(1)
			return nil // a fresh decoder cannot use a non-keyframe: drop, counted
		}
		m.needKey.Store(false)
	}
	if err := m.dec.Decode(f.Payload, f.PTS, key); err != nil {
		// The session failed for good (destination pinned to the frame path / crash limit): end the
		// route so it re-establishes on the ffmpeg decoder rather than publishing nothing.
		m.log.Warn(source, "native decode session ended - route re-establishes on the ffmpeg decoder",
			map[string]any{"err": err.Error()})
		return err
	}
	return nil
}

// Close implements medialink.Sink: stop the session, then close the inner sink (which owns the
// destination sender - it must outlive the child's last publish).
func (m *mfDecoder) Close() error {
	m.closed.Store(true)
	m.dec.Close()
	return m.sink.Close()
}

// PipeStats implements medialink.PipelineReporter.
func (m *mfDecoder) PipeStats() medialink.PipelineStats {
	st := m.dec.Stats()
	accel := "d3d11"
	if !m.dec.IsHardware() {
		accel = "d3d11-dxva" // the MS D3D11-aware decoder MFT, still GPU-resident
	}
	return medialink.PipelineStats{
		Encoder: "mf-native-decode", HWAccel: accel, OutFPS: st.DecFPS, Restarts: st.Restarts,
		QueueDepth: st.QueueDepth, ChildCPUPct: st.ChildCPUPct,
		Dropped:    m.dropped.Load() + st.DecDropped + st.InDropped + medialink.InnerDrops(m.sink),
		RateCapped: medialink.InnerRateCapped(m.sink),
		ZeroDecode: st.DecFlags&1 != 0, DecFPS: st.DecFPS, DecBusyMs: st.DecBusyMs,
		InDropped: st.InDropped, DecDropped: st.DecDropped, DecErrors: st.DecErrors,
		DecStaleMs: st.DecStaleMs, DecMtxTimeo: st.MtxTimeouts, Downgrades: st.Downgrades,
	}
}
