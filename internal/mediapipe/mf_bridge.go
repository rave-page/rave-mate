package mediapipe

// mf_bridge.go - native Media Foundation hardware encode as a medialink Source: the
// preferred H.264 engine on Windows. Frames go over a shared-memory ring to the
// supervised per-adapter Zig encoder child (native/zigenc: D3D11 upload →
// VideoProcessorBlt CSC/scale → encoder silicon) and come back as annex-B AUs with PTS
// preserved - no ffmpeg child, no multi-GB/s stdin pipe, and a vendor-driver fault kills
// only the encoder child (supervisor restarts it; the route survives). ffmpeg remains
// the fallback engine and the decode side. Scaling (spec.MaxHeight) rides the same Blt.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/mfenc"
)

// subWait bounds how long Next() waits for a mid-route substitution verdict after the native AU
// stream ends. Only reached when nothing published a verdict (belt and braces: every exit path in
// emit/feed/Close publishes one), so it is a liveness floor, not a latency budget.
const subWait = 2 * time.Second

// routeTelemetryEvery paces the log-side content oracle. 10 s is frequent enough to catch a route
// going black mid-run and cheap enough to leave on permanently (one log line per route).
const routeTelemetryEvery = 10 * time.Second

// mfBridge implements medialink.Source (+KeyframeSource, PipelineReporter) over an
// mfenc encoder-child session.
type mfBridge struct {
	log    *logbus.Bus
	enc    *mfenc.ProcSession
	src    medialink.Source
	size   int
	frames chan *medialink.Frame
	ctx    context.Context // route ctx: the substitute encoder and its pump ride it
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	tcq     []medialink.Timecode // FIFO pts→TC carry (no B-frames: output order = input order)
	lastKey int64

	out rate
	// dropped: undersized/foreign input frames. Atomic - feed() writes it, PipeStats reads it
	// from the telemetry goroutine (it was a plain int, i.e. a data race that also never
	// reached any surface).
	dropped    atomic.Uint64
	zeroCopy   bool
	downgrades int

	// ── mid-route substitution (never black-frame) ──
	// A mid-route native failure used to END THE ROUTE: feed() logged "MF encode failed - route
	// ends" and returned, the source closed, and the peer was left with a frozen picture while
	// every counter still read healthy. The ffmpeg substitution only ever existed at OPEN time.
	// sub is a supervised ffmpeg encoder built over the SAME inner source: once it is set, Next()
	// and PipeStats() delegate to it, so real pixels keep flowing on the same route and the reason
	// is visible in the panel. Guarded by subOnce - one substitution per route, never a flap.
	spec       medialink.EncodeSpec
	subOnce    sync.Once
	sub        atomic.Pointer[encoder]
	subReady   chan struct{} // closed when sub is live (or when substitution gave up)
	degradeMu  sync.Mutex
	degrade    string
	subEncoder string

	// Content oracle. b.out counts bytes AND AUs on ONE sliding window (native and substituted),
	// so bytes-per-frame is measured over a single span instead of two unrelated ones. The log
	// stream carries it every routeTelemetryEvery and, since inc 5, so does the rendered panel:
	// the panel's counters have been observed frozen for 25 minutes on a demonstrably live route,
	// and "OutFPS looks fine" cannot tell a real picture from a black one either way.
	noContentWarned atomic.Bool
	// adapterName is the RESOLVED adapter description (a stale configured LUID names nothing).
	adapterName string
}

// ZeroCopyCapture gates the zigmedia path: a Spout source's pixels reach the encoder child as a GPU
// shared-texture handle instead of a host readback. Package seam (same shape as mfenc.Warnf).
//
// The PRODUCT default lives in config.MediaLinkFeature.ZeroCopyCapture and is ON since increment 5.
// This fallback stays false because an UNWIRED process must not assume a capability it was never
// told about; both production wiring sites point it at the live config unconditionally
// (app.go and featurehost/feat_media.go). A third construction site must wire it too, or the
// default silently does not apply there.
var ZeroCopyCapture = func() bool { return false }

// ZeroCopyAffinity gates re-placing a zero-copy session on the adapter that owns the sender's
// texture (zigmedia inc 3, risk R7). Package seam; the product default is OFF - the re-place is
// live-verified only between two identical GPUs (see config.ZigAffinity).
var ZeroCopyAffinity = func() bool { return false }

// newMFBridge builds the native pipeline for spec; error = caller falls back to ffmpeg.
func newMFBridge(ctx context.Context, log *logbus.Bus, spec medialink.EncodeSpec, src medialink.Source) (*mfBridge, error) {
	if spec.Width <= 0 || spec.Height <= 0 || spec.Width > 16384 || spec.Height > 16384 {
		return nil, fmt.Errorf("mfenc: bad encode size %dx%d", spec.Width, spec.Height)
	}
	fps := spec.FPS
	if fps <= 0 {
		fps = 30
	}
	outW, outH := spec.Width, spec.Height
	if spec.MaxHeight > 0 && outH > spec.MaxHeight {
		outW = outW * spec.MaxHeight / outH
		outH = spec.MaxHeight
	}
	outW, outH = outW&^1, outH&^1 // encoders want even dims
	kbps := spec.BitrateKbps
	if kbps <= 0 {
		kbps = defaultBitrateKbps(outW, outH, fps)
	}
	// Device preference (WP-3): the native engine takes the adapter LUID directly, so it is the
	// device-steerable H.264 path on every vendor (ffmpeg has no device flag for AMF at all). An
	// unusable LUID degrades to the default adapter inside the shim.
	var luid int64
	if key, _, ok := spec.Device(); ok {
		if v, ok := encoderscan.LUIDInt64(key); ok {
			luid = v
		}
	}
	opts := mfenc.ProcOpts{LUID: luid, InW: spec.Width, InH: spec.Height, OutW: outW, OutH: outH,
		FPS: fps, Kbps: kbps, Gop: gopFrames(fps)}
	// Zero-copy request (zigmedia inc 1) needs ALL of: the flag, a source that really exposes a
	// shared texture, a handle that matches the negotiated geometry, and a sender not already
	// pinned to the readback path. Anything missing = the readback frame path, byte for byte.
	// Decided PER SOURCE (zcVerdict) - a webcam route and a Spout route in the same process take
	// different paths, and neither is inferred from the flag alone.
	v := zeroCopyOpts(&opts, spec, src)
	zc, downgrades := v.request, 0
	if !zc && v.applicable {
		// A source that COULD have qualified did not: one WARN naming the reason, and counted, so a
		// rig that always falls back is visible rather than silently slow. Not logged for a webcam:
		// there the readback is the only path that ever existed, not a downgrade.
		downgrades = 1
		log.Warn(source, "this route is on the CPU readback path, not zero-copy capture",
			map[string]any{"reason": v.reason, "in": fmt.Sprintf("%dx%d", spec.Width, spec.Height)})
	}
	enc, err := mfenc.OpenProcSessionOpts(opts)
	if err != nil && zc && errors.Is(err, mfenc.ErrZeroCopyRefused) {
		// Same child, same geometry, reopened on the readback path. ONE warn per route so a rig
		// that always downgrades is visible instead of silently slow.
		log.Warn(source, "zero-copy capture refused - reopening this route on the readback path",
			map[string]any{"err": err.Error(), "sender": opts.Spout.Name, "hint": affinityHint(err)})
		opts.Spout, zc = nil, false
		downgrades++
		enc, err = mfenc.OpenProcSessionOpts(opts)
	}
	if err != nil {
		return nil, err
	}
	bctx, cancel := context.WithCancel(ctx)
	b := &mfBridge{log: log, enc: enc, src: src, size: spec.Width * spec.Height * 4,
		frames: make(chan *medialink.Frame, encFramesBuf), cancel: cancel, done: make(chan struct{}),
		zeroCopy: zc, downgrades: downgrades, spec: spec, subReady: make(chan struct{})}
	b.ctx = bctx
	b.adapterName = enc.Stats().AdapterName
	if r := enc.DegradeReason(); r != "" {
		b.noteDegrade(r) // opened already degraded (poisoned hardware → software tier)
	}
	log.Info(source, "native MF encode (isolated encoder child)", map[string]any{
		"encoder": enc.Name(), "in": fmt.Sprintf("%dx%d", spec.Width, spec.Height),
		"out": fmt.Sprintf("%dx%d", outW, outH), "kbps": kbps, "swizzle": enc.InputIsBGRA(),
		// device = what was REQUESTED; adapter = what the child resolved (a stale LUID silently
		// ran elsewhere). drive/tier make the vendor-portability verdict visible per route.
		"device":  spec.DeviceLUID,
		"adapter": fmt.Sprintf("%#x %s", uint64(enc.Stats().AdapterLUID), b.adapterName),
		"capture": captureLabel(zc), "drive": enc.Drive(), "tier": tierLabel(enc.IsSoftware()),
		"degraded": enc.DegradeReason()})
	// A zero-copy session's pixels never pass through here: no feed goroutine, so the source's
	// capture is never attached and no readback is ever performed (mediaroute attaches lazily).
	if !zc {
		go b.feed(bctx)
	}
	go b.emit(bctx)
	go b.routeTelemetry(bctx)
	return b, nil
}

func captureLabel(zeroCopy bool) string {
	if zeroCopy {
		return "zerocopy"
	}
	return "readback"
}

// capSyncLabel names the capture's synchronisation from the child's CapFlags bits (bit1 keyed
// mutex, bit2 named mutex, bit3 unsynchronised). "" when no zero-copy session is live.
func capSyncLabel(flags uint32) string {
	switch {
	case flags&0x2 != 0:
		return "keyed"
	case flags&0x4 != 0:
		return "named"
	case flags&0x8 != 0:
		return "unsync"
	}
	return ""
}

func tierLabel(software bool) string {
	if software {
		return "software-mf"
	}
	return "hardware"
}

// noteDegrade records why this route is off its best path (first reason wins = root cause).
func (b *mfBridge) noteDegrade(reason string) {
	b.degradeMu.Lock()
	if b.degrade == "" {
		b.degrade = reason
	}
	b.degradeMu.Unlock()
}

func (b *mfBridge) degradeReason() string {
	b.degradeMu.Lock()
	defer b.degradeMu.Unlock()
	return b.degrade
}

// substitute swaps a supervised ffmpeg H.264 encoder in over the SAME inner source, in place, so
// a mid-route native failure costs a hiccup instead of the route. It is the "degrade never
// black-frames" guarantee: the peer was answered H.264, the substitute is H.264, and the route
// object the caller holds does not change.
//
// Called at most once per route (subOnce): if the substitute itself dies, its own supervisor
// restarts it - flapping back to a native engine that just failed would be a loop.
func (b *mfBridge) substitute(reason string) {
	b.subOnce.Do(func() {
		defer close(b.subReady)
		b.noteDegrade(reason)
		name, ok := ffmpegH264Fallback()
		if !ok {
			b.log.Warn(source, "native MF encode failed mid-route and no ffmpeg H.264 encoder is probed - the route ends",
				map[string]any{"reason": reason})
			return
		}
		ffmpeg, ok := mediatools.Resolve("ffmpeg")
		if !ok {
			b.log.Warn(source, "native MF encode failed mid-route and ffmpeg is not installed - the route ends",
				map[string]any{"reason": reason})
			return
		}
		spec := b.spec
		spec.Encoder = name
		spec.Software = name == "libx264"
		sub, err := newEncoder(b.ctx, b.log, ffmpeg, spec, b.src)
		if err != nil {
			b.log.Warn(source, "native MF encode failed mid-route and the ffmpeg substitute would not start - the route ends",
				map[string]any{"reason": reason, "err": err.Error()})
			return
		}
		b.degradeMu.Lock()
		b.subEncoder = name
		b.degradeMu.Unlock()
		b.sub.Store(sub)
		// ONE loud line: this is a real capability loss (pipe-fed, higher sender load, no live
		// forced IDR), not a detail. The route panel carries the same reason.
		b.log.Warn(source, "native MF encode failed mid-route - substituted an ffmpeg H.264 encoder on the same route (the stream keeps real pixels; expect higher sender load)",
			map[string]any{"encoder": name, "reason": reason})
		go b.pumpSub(sub)
	})
}

// routeTelemetry logs one line per interval with the CONTENT oracle plus the identity of the code
// path serving this route. This is the only diagnosis channel on a box with no Go toolchain and no
// remote-exec, so it must be complete on a HEALTHY route, not only on failure.
func (b *mfBridge) routeTelemetry(ctx context.Context) {
	t := time.NewTicker(routeTelemetryEvery)
	defer t.Stop()
	var prevBytes, prevCount uint64
	prev := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		bytes, count := b.out.totals()
		dB, dN := bytes-prevBytes, count-prevCount
		secs := now.Sub(prev).Seconds()
		prevBytes, prevCount, prev = bytes, count, now
		st := b.PipeStats()
		perFrame := uint64(0)
		if dN > 0 {
			perFrame = dB / dN
		}
		kbps := 0.0
		if secs > 0 {
			kbps = float64(dB) * 8 / 1000 / secs
		}
		b.noteNoContent(dN, perFrame)
		b.log.Info(source, "route encode telemetry", map[string]any{
			"engine": st.Encoder, "tier": tierLabel(st.SoftwareEncode), "drive": st.Drive,
			// capSync is not cosmetic: it decides whether the shared-texture read is coherent at
			// all, and only the log makes the field's branch visible.
			"capture": captureLabel(b.zeroCopy), "capSync": st.CapSync, "device": st.DevPolicy,
			"mtxTimeouts": st.MtxTimeouts, "capStaleMs": fmt.Sprintf("%.0f", st.CapStaleMs),
			"adapter": fmt.Sprintf("%#x %s", uint64(st.AdapterLUID), b.adapterName),
			// The content oracle: a live picture cannot be a few hundred bytes per frame.
			"aus": dN, "bytesPerFrame": perFrame, "kbps": fmt.Sprintf("%.0f", kbps),
			"fps": fmt.Sprintf("%.1f", st.OutFPS),
			// Saturation is SEPARATE from failure is SEPARATE from deliberate rate limiting -
			// three different incidents, three different responses, three numbers. "dropped"
			// stays the TOTAL; rateCapped is the fps-cap share of it, lost is the remainder.
			"busyDrops": st.BusyDrops, "encFails": st.EncFails, "dropped": st.Dropped,
			"rateCapped": st.RateCapped, "lost": st.RealDrops(),
			"ledgerFails": st.LedgerFails, "poisoned": st.Poisoned,
			"degraded": st.DegradeReason,
		})
	}
}

// noteNoContent logs ONCE per route when the bitstream carries almost nothing while AUs keep
// flowing. This is the field's black-route signature (255 B/frame at 4K30 on a 20 Mbps budget),
// but the same reading is produced by a frozen source AND by a genuinely static one - so the
// wording names all three and the route is NOT marked degraded on it. The rendered panel carries
// the number itself; this is the nudge that reaches a log-only box.
func (b *mfBridge) noteNoContent(aus, bytesPerFrame uint64) {
	if aus == 0 || bytesPerFrame >= medialink.AUNoiseFloorBytes || b.noContentWarned.Load() {
		return
	}
	if b.noContentWarned.Swap(true) {
		return
	}
	b.log.Warn(source, "this route's bitstream carries almost no picture content - a black, frozen or completely static source all look like this",
		map[string]any{"bytesPerFrame": bytesPerFrame, "aus": aus,
			"capture": captureLabel(b.zeroCopy), "noiseFloor": medialink.AUNoiseFloorBytes})
}

// pumpSub forwards the substitute's frames onto this bridge's own channel, so consumers that
// already hold this Source never learn the engine changed underneath them.
func (b *mfBridge) pumpSub(sub *encoder) {
	for {
		f, err := sub.Next(b.ctx)
		if err != nil {
			return
		}
		b.out.tickBytes(len(f.Payload))
		select {
		case b.frames <- f:
		case <-b.ctx.Done():
			return
		}
	}
}

// zcVerdict is ONE source's zero-copy decision, spelled out. It became worth naming when
// zero-copy became the DEFAULT path (zigmedia inc 5): the interesting question flipped from "did
// anybody ask for this" to "why did THIS source not get it", and a default that quietly puts a
// whole rig back on the readback is exactly the failure the promotion gates exist to prevent.
//
// applicable separates the two kinds of "no", which must not be logged or counted the same way:
//   - a webcam / DirectShow / non-Spout source has no GPU shared texture AT ALL, so the readback
//     is not a downgrade, it is the only path that was ever possible. Silent, uncounted, correct.
//   - a Spout source that COULD have qualified and did not (DX9 or CPU-memoryshare sender, a
//     sender that resized between advert and open, a sender already pinned to the readback) is a
//     real downgrade: one WARN naming the reason, and counted so `ctl perf` can show a rig that
//     always downgrades instead of one that is mysteriously slow.
type zcVerdict struct {
	request    bool   // ask the child for src:"spout"
	applicable bool   // this source type could ever have done zero-copy
	reason     string // why not (empty when requested)
}

// zeroCopyOpts fills opts.Spout when the whole per-source gate holds and returns the verdict.
// Decided PER SOURCE, at open, from the source's own answer - never assumed from the flag, the
// route kind or the peer's advert.
func zeroCopyOpts(opts *mfenc.ProcOpts, spec medialink.EncodeSpec, src medialink.Source) zcVerdict {
	zcs, ok := src.(medialink.ZeroCopySource)
	if !ok {
		// Not applicable, whatever the flag says: there is no texture to hand anyone.
		return zcVerdict{reason: "source has no GPU shared texture (webcam / DirectShow / non-Spout)"}
	}
	if !ZeroCopyCapture() {
		return zcVerdict{applicable: true, reason: "zero-copy capture disabled by config"}
	}
	h, _, w, hh, name, ok := zcs.SharedTexture()
	if !ok || h == 0 {
		// A DX9 or CPU/memoryshare Spout sender has no DX11 shared texture. Worth knowing that the
		// readback cannot serve these EITHER (it needs the same handle) - so this is the one rung
		// where neither path works and the route will fail somewhere else.
		return zcVerdict{applicable: true, reason: "sender exposes no DX11 shared texture (DX9 or CPU/memory-share sender)"}
	}
	if w != spec.Width || hh != spec.Height {
		return zcVerdict{applicable: true, reason: fmt.Sprintf(
			"sender is %dx%d but the route negotiated %dx%d (it resized between advert and open)", w, hh, spec.Width, spec.Height)}
	}
	if mfenc.ZeroCopyPinnedToReadback(name) {
		return zcVerdict{applicable: true, reason: "this sender is pinned to the readback path (its zero-copy source failed repeatedly)"}
	}
	// Re-read on every (re)open + on the 2 s health tick: a restarted sender must never be
	// re-issued its dead handle (risk R1, the silently frozen picture).
	opts.Spout = &mfenc.SpoutSource{Name: name, Resolve: func() (uint64, uint32, int, int, bool) {
		hd, f, ww, hgt, _, ok := zcs.SharedTexture()
		return hd, f, ww, hgt, ok
	}}
	opts.ZeroCopyAdapters = affinityCandidates(spec)
	return zcVerdict{request: true, applicable: true}
}

// affinityHint turns the one refusal an operator can actually fix into an instruction. `open_shared`
// on a multi-adapter host is risk R7 - the sender's texture lives on the other GPU - and
// mediaLink.zigAffinity resolves it, but that key is default OFF because the re-place is only
// live-verified between two identical GPUs. Without this hint, leaving it off is a silent miss on
// exactly the rigs that need it.
func affinityHint(err error) string {
	if err == nil || !strings.Contains(err.Error(), "open_shared") {
		return ""
	}
	if len(encoderscan.Adapters()) < 2 {
		return ""
	}
	if ZeroCopyAffinity() {
		return "adapter affinity is already enabled and still could not open the sender's texture"
	}
	return "this host has more than one GPU and the sender's texture is probably on the other one - " +
		"set mediaLink.zigAffinity (or RAVE_MATE_ZIGMEDIA_AFFINITY=1) to re-place the session there"
}

// affinityCandidates lists the adapters a zero-copy session may be re-placed on (R7). EMPTY unless
// the gate is on AND the encode device is UNRESOLVED: a device the user pinned - or the governor
// chose via avoid-busiest - is policy, and "never silently move adapters" means policy wins over
// an optimisation. Absent candidates, mfenc keeps today's downgrade to the readback path.
func affinityCandidates(spec medialink.EncodeSpec) []int64 {
	if !ZeroCopyAffinity() {
		return nil
	}
	if _, _, resolved := spec.Device(); resolved {
		return nil
	}
	var out []int64
	for _, a := range encoderscan.Adapters() {
		if l, ok := encoderscan.LUIDInt64(a.LUID); ok {
			out = append(out, l)
		}
	}
	if len(out) < 2 {
		return nil // nothing to move to
	}
	return out
}

// feed pumps raw frames into the encoder; source EOF drains + closes it.
func (b *mfBridge) feed(ctx context.Context) {
	defer b.enc.Close() // drains; Output closes after tail AUs
	for {
		if b.sub.Load() != nil {
			return // substituted: the ffmpeg encoder owns the inner source from here
		}
		f, err := b.src.Next(ctx)
		if err != nil {
			return // EOF / cancel - drain via defer
		}
		if f.Kind != medialink.KindVideo || len(f.Payload) != b.size {
			b.dropped.Add(1)
			if f.Release != nil {
				f.Release()
			}
			continue
		}
		pts := f.PTS
		if pts == 0 {
			pts = time.Now().UnixNano()
		}
		b.mu.Lock()
		if len(b.tcq) < encPTSQueueCap {
			b.tcq = append(b.tcq, f.TC)
		}
		b.mu.Unlock()
		err = b.enc.Encode(f.Payload, pts)
		if f.Release != nil {
			f.Release() // Encode returns after the GPU upload copied the rows - buffer is free
		}
		if err != nil {
			if ctx.Err() != nil {
				return // route teardown
			}
			// NOT the end of the route any more: keep the pixels, change the engine.
			reason := err.Error()
			if r := b.enc.DegradeReason(); r != "" {
				reason = r // the child's attributed cause (stage/rc) beats "encode timeout"
			}
			b.substitute(reason)
			return
		}
	}
}

// emit converts encoder AUs to medialink frames. When the native session dies without the feed
// goroutine noticing - which is EVERY zero-copy route, where the child owns the source and there
// is no feed goroutine at all - this is where the substitution is triggered.
func (b *mfBridge) emit(ctx context.Context) {
	defer close(b.done)
	for au := range b.enc.Output() {
		b.out.tickBytes(len(au.Data))
		f := &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecH264,
			PTS: au.PTSNs, Payload: au.Data}
		if au.Keyframe {
			f.Flags |= medialink.FlagKeyframe
			b.mu.Lock()
			b.lastKey = time.Now().UnixNano()
			b.mu.Unlock()
		}
		b.mu.Lock()
		if len(b.tcq) > 0 {
			f.TC = b.tcq[0]
			b.tcq = b.tcq[1:]
		}
		b.mu.Unlock()
		select {
		case b.frames <- f:
		case <-ctx.Done():
			return
		}
	}
	// The native AU stream ended. If that was a FAILURE rather than teardown, substitute -
	// otherwise a zero-copy route (no feed goroutine) would end here with a frozen picture and
	// healthy-looking counters, which is precisely the failure shape this must never have.
	if ctx.Err() != nil {
		return
	}
	if err := b.enc.Failed(); err != nil || b.enc.DegradeReason() != "" {
		reason := b.enc.DegradeReason()
		if reason == "" {
			reason = err.Error()
		}
		b.substitute(reason)
		return
	}
	b.declineSubstitution() // clean end of stream: publish the verdict so Next never waits
}

// declineSubstitution publishes "no substitution" so Next() reports EOF immediately on a clean
// teardown. The verdict is always published exactly once, by whichever path gets there first.
func (b *mfBridge) declineSubstitution() {
	b.subOnce.Do(func() { close(b.subReady) })
}

// Next implements medialink.Source. b.done fires when the NATIVE AU stream ends, which on a
// substituted route is not the end of the source: wait for the substitution verdict before
// reporting EOF, so a mid-route engine swap is invisible to the consumer.
func (b *mfBridge) Next(ctx context.Context) (*medialink.Frame, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case f, ok := <-b.frames:
			if !ok {
				return nil, io.EOF
			}
			return f, nil
		case <-b.done:
			select {
			case f, ok := <-b.frames:
				if ok {
					return f, nil
				}
			default:
			}
			// Native stream over. A substitution may be starting: block on its verdict once.
			select {
			case <-b.subReady:
				if b.sub.Load() == nil {
					return nil, io.EOF // substitution declined/failed: the route really is over
				}
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(subWait):
				return nil, io.EOF // nobody is substituting: original behaviour
			}
			// Substituted: keep serving from b.frames (pumpSub feeds it).
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case f, ok := <-b.frames:
				if !ok {
					return nil, io.EOF
				}
				return f, nil
			}
		}
	}
}

// Close implements medialink.Source.
func (b *mfBridge) Close() error {
	b.cancel()
	b.declineSubstitution() // never leave a Next() blocked on a verdict that will not come
	if sub := b.sub.Load(); sub != nil {
		return sub.Close() // the substitute owns the inner source (it closes it)
	}
	if b.zeroCopy {
		b.enc.Close() // no feed goroutine owns the session on this path (its defer normally does)
	}
	return b.src.Close()
}

// RequestKeyframe implements medialink.KeyframeSource: a LIVE forced IDR (no child
// restart, no stream hole) - rate-limited so PLI storms stay cheap anyway.
func (b *mfBridge) RequestKeyframe() {
	if sub := b.sub.Load(); sub != nil {
		sub.RequestKeyframe() // substituted: PLI must still reach the engine that is running
		return
	}
	b.mu.Lock()
	fresh := time.Now().UnixNano()-b.lastKey < encKeyFreshNs
	b.mu.Unlock()
	if !fresh {
		b.enc.ForceKeyframe()
	}
}

// PipeStats implements medialink.PipelineReporter (per-session perf: p99 rise is the
// Phase-2 governor's early saturation signal).
func (b *mfBridge) PipeStats() medialink.PipelineStats {
	// Substituted: report the engine that is ACTUALLY running plus why. Reporting the native
	// engine here would be the same lie the old mid-route failure told - "healthy, native,
	// hardware" over a stream that had stopped.
	if sub := b.sub.Load(); sub != nil {
		st := sub.PipeStats()
		st.DegradeReason = b.degradeReason()
		st.SoftwareEncode = st.Encoder == "libx264"
		st.OutFPS = b.out.value()
		st.Downgrades += b.downgrades
		st.RateCapped = medialink.InnerRateCapped(b.src)
		// The content oracle follows the route, not the engine: a substituted route that ships
		// black is the same incident as a native one that does.
		st.AUBytes, st.AUCount = b.out.totals()
		st.AUBytesPerFrame = b.out.perFrame()
		return st
	}
	st := b.enc.Stats()
	if r := st.DegradeReason; r != "" {
		b.noteDegrade(r)
	}
	auBytes, auCount := b.out.totals()
	return medialink.PipelineStats{Encoder: medialink.EncoderMFNative, OutFPS: b.out.value(),
		Restarts: st.Restarts, LatP50Ms: st.LatP50Ms, LatP99Ms: st.LatP99Ms,
		QueueDepth: st.QueueDepth, ChildCPUPct: st.ChildCPUPct,
		ZeroCopy: st.ZeroCopy, CapFPS: st.CapFPS, CapSkips: st.CapSkips,
		MtxTimeouts: st.MtxTimeouts, SrcErrors: st.SrcErrors, CapStaleMs: st.CapStaleMs,
		CapSync:      capSyncLabel(st.CapFlags),
		AdapterMoved: st.AdapterMoved,
		EncBusyMs:    st.EncBusyMs,
		Downgrades:   b.downgrades + st.Downgrades,
		// Saturation drops count as drops: a route shedding frames must never look identical to a
		// healthy one (that equivalence is what let a black 4K route report healthy for 12 minutes).
		// The fps-cap share rides RateCapped as well, so "throttled by design" and "losing frames"
		// stop being the same number.
		Dropped:    b.dropped.Load() + medialink.InnerDrops(b.src) + uint64(st.BusyDrops),
		RateCapped: medialink.InnerRateCapped(b.src),
		// Vendor-portability + degrade visibility (never a silent downgrade).
		DegradeReason:  b.degradeReason(),
		Drive:          st.Drive,
		SoftwareEncode: st.Software,
		BusyDrops:      uint64(st.BusyDrops),
		EncFails:       uint64(st.EncFails),
		// The content oracle, cumulative AND windowed. Only the windowed figure can say whether the
		// route is shipping a picture NOW; a lifetime average over a route that went black an hour
		// ago still reads healthy.
		AUBytes:         auBytes,
		AUCount:         auCount,
		AUBytesPerFrame: b.out.perFrame(),
		AdapterLUID:     st.AdapterLUID,
		DevPolicy:       st.DevPolicy,
		Poisoned:        st.Poisoned,
		LedgerFails:     st.LedgerFails}
}
