// Package mediaroute is the medialink P4 route glue (MEDIALINK_DESIGN.md §3/§8 P4): it
// advertises this instance's local Spout senders as medialink video sources (opt-in
// MediaLink.ShareVideo), materializes received routes as local Spout senders
// ("rave-mate link <source>"), and drives receive-route creation from the Peers tab.
// Same-PC rule (§3): a route to an instance on the SAME machine is refused - the Spout sender
// is already directly visible there; encoding locally is never allowed.
package mediaroute

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/framedebug"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/testcard"
	"rave.page/mate/internal/videoshare"
)

const (
	source        = "mediaroute"
	scanEvery     = 2 * time.Second
	linkPrefix    = "rave-mate link " // receive-output sender names (excluded from sharing: loop guard)
	camPrefix     = "rave-mate cam "  // the webcam feature's OWN capture sender - already a direct "webcam" source; re-sharing it double-advertises the camera
	receiveGrace  = 30 * time.Second  // offered-but-never-up receives are cleaned after this
	defaultFPSAdv = 60                // Spout carries no fps metadata; advertise the common case
	// parkTTL bounds how long a kept-alive republish sender is held after its route ended before it
	// is destroyed; parkCap bounds the held set (= medialink's concurrent-route cap). Both keep the
	// VRAM-churn fix (kept senders survive a route restart, 12b25a7) from leaking DX11 textures.
	parkTTL = 10 * time.Minute
	parkCap = 8
)

// Router is the medialink surface mediaroute drives (satisfied by *medialink.RouteManager).
type Router interface {
	RegisterSource(medialink.SourceDesc, medialink.SourceOpen)
	UnregisterSource(id string)
	RegisterSink(medialink.SinkDesc, medialink.SinkOpen)
	UnregisterSink(id string)
	OfferRoute(target, sourceID, sinkID string, opt medialink.OfferOptions) (string, error)
	CloseRoute(session string)
	RemoteAdverts() map[string]medialink.Advert
	Stats() []medialink.RouteStat
}

// Options wires a Manager. Log + Router + Cfg required; the seams default to videoshare.
type Options struct {
	Log      *logbus.Bus
	Router   Router
	Cfg      func() config.MediaLinkFeature
	SameHost func(peer string) bool // §3 same-PC guard; nil = no guard

	// Test seams (default: videoshare Spout backend).
	ListSenders func() []string
	SenderSize  func(name string) (w, h int, ok bool)
	// SenderShare resolves a sender's GPU shared-texture handle + DXGI format (zero-copy
	// encode, zigmedia inc 1). Scalars only - no pixels, no capture opened.
	SenderShare func(name string) (handle uint64, dxgiFormat uint32, w, h int, ok bool)
	// NewSharedSender opens the receive-route's local sender EAGERLY and exposes its destination
	// GPU texture, so the decoder child can render into it (zigmedia inc 2). nil = videoshare.
	NewSharedSender func(name string, w, h int) (videoshare.SharedSender, error)
	// GrabFrame reads a sender's CURRENT content once (diagnostic oracle, not a hot path).
	GrabFrame func(name string, w, h int) (*image.NRGBA, error)
	// NewFrameSender opens a plain named sender (testcard generator). nil = videoshare.
	NewFrameSender func(name string) (videoshare.FrameSender, error)
	OpenSource     func(name string, w, h int) (medialink.Source, error)
	OpenSink       func(name string, w, h int) (medialink.Sink, error)
	OpenReceiver   func(name string, maxFPS float64) (videoshare.FrameReceiver, error) // shared-capture backend
	PutPix         func([]byte)                                                        // pooled-buffer recycler
}

// Receive is one requested receive route (UI listing + sink cleanup bookkeeping).
type Receive struct {
	Session  string
	Peer     string
	SourceID string // remote source id (de-dup key: one receive per source)
	SinkID   string
	Name     string // remote source name
	Since    time.Time
}

// Manager owns the share scanner + receive bookkeeping. Safe for concurrent use.
type Manager struct {
	log      *logbus.Bus
	router   Router
	cfg      func() config.MediaLinkFeature
	sameHost func(string) bool

	listSenders  func() []string
	senderSize   func(string) (int, int, bool)
	senderShare  func(string) (uint64, uint32, int, int, bool)
	grabFrame    func(string, int, int) (*image.NRGBA, error)
	newFrameSnd  func(string) (videoshare.FrameSender, error)
	newSharedSnd func(string, int, int) (videoshare.SharedSender, error)
	openSource   func(string, int, int) (medialink.Source, error)
	openSink     func(string, int, int) (medialink.Sink, error)
	hub          *captureHub // one capture per Spout source, fanned out to N routes

	now func() time.Time // seam for the park TTL reaper (default time.Now)

	mu       sync.Mutex
	shared   map[string]medialink.SourceDesc // sender name → advertised desc
	receives map[string]Receive              // session → state
	tc       *testcard.Gen                   // running diagnostic generator (nil = off)
	// parked holds republish senders whose route ended but whose Spout sender (+ DX11 shared
	// texture) is kept open for the next reconnect, so Resolume never re-registers GL/DX interop.
	// Keyed by sender name (linkPrefix + source). Bounded: cap parkCap, drop-oldest on overflow
	// (evicted inner sink is closed) - a held 4K sender is ~33 MB of texture, so an unbounded cache
	// is a VRAM leak. Reaped after parkTTL idle.
	parked map[string]*parkedSink
	// stopping marks senders whose NEXT keptSink.Close must destroy (explicit StopReceive) rather
	// than park. Keyed by sender name; deleted when consumed.
	stopping map[string]bool
}

// New builds the manager (inert until Start).
func New(o Options) *Manager {
	m := &Manager{
		log: o.Log, router: o.Router, cfg: o.Cfg, sameHost: o.SameHost,
		listSenders: o.ListSenders, senderSize: o.SenderSize, senderShare: o.SenderShare,
		grabFrame: o.GrabFrame, newFrameSnd: o.NewFrameSender,
		newSharedSnd: o.NewSharedSender, openSource: o.OpenSource, openSink: o.OpenSink,
		shared: map[string]medialink.SourceDesc{}, receives: map[string]Receive{},
		parked: map[string]*parkedSink{}, stopping: map[string]bool{}, now: time.Now,
	}
	m.hub = newCaptureHub(o.Log, o.OpenReceiver, o.PutPix)
	if m.listSenders == nil {
		m.listSenders = videoshare.ListSenders
	}
	if m.senderSize == nil {
		m.senderSize = videoshare.SenderSize
	}
	if m.grabFrame == nil {
		m.grabFrame = videoshare.GrabSenderFrame
	}
	if m.senderShare == nil {
		m.senderShare = videoshare.SenderShare
	}
	if m.newSharedSnd == nil {
		m.newSharedSnd = func(n string, w, h int) (videoshare.SharedSender, error) {
			return videoshare.NewSharedSender(o.Log, n, w, h)
		}
	}
	if m.newFrameSnd == nil {
		m.newFrameSnd = func(n string) (videoshare.FrameSender, error) {
			return videoshare.NewFrameSender(o.Log, n)
		}
	}
	if m.openSource == nil {
		m.openSource = m.openSpoutSource
	}
	if m.openSink == nil {
		m.openSink = m.openSpoutSink
	}
	return m
}

// Start runs the scan/cleanup loop (ctx-bound). Non-blocking.
func (m *Manager) Start(ctx context.Context) {
	debuglog.Go(m.log, source, func() {
		t := time.NewTicker(scanEvery)
		defer t.Stop()
		defer m.destroyAllParked() // release every held sender on shutdown
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.scan()
				m.cleanup()
				m.reapParked()
			}
		}
	})
}

// scan diffs the local sender registry against the advertised set (ShareVideo only).
func (m *Manager) scan() {
	share := m.cfg().ShareVideo
	var names []string
	if share {
		for _, n := range m.listSenders() {
			// Never re-share a RECEIVED route (loop guard) or the webcam's OWN capture sender - the
			// webcam already advertises itself as a direct "webcam" source, so re-sharing its Spout
			// preview double-lists the same camera (the "two sources for one device" bug).
			if strings.HasPrefix(n, linkPrefix) || strings.HasPrefix(n, camPrefix) {
				continue
			}
			names = append(names, n)
		}
	}
	live := map[string]bool{}
	for _, n := range names {
		live[n] = true
		w, h, ok := m.senderSize(n)
		if !ok || w <= 0 || h <= 0 {
			continue
		}
		m.mu.Lock()
		prev, had := m.shared[n]
		m.mu.Unlock()
		if had && prev.Width == w && prev.Height == h {
			continue
		}
		fps := float64(defaultFPSAdv)
		if c := m.cfg().FPSCap(); c > 0 && float64(c) < fps {
			fps = float64(c) // the sender-side cap is the real delivery rate - advertise it
		}
		// Spout carries no frame-rate metadata, so this number is an ASSUMPTION, and it is the one
		// that decides how often we capture: a 30 fps canvas advertised at 60 is captured, converted
		// and encoded twice per frame (measured on the 2-PC rig - a 30 fps source ran at wire 62 fps).
		// Say so once per sender, with the lever, instead of letting it stay silent.
		if fps > 30 {
			m.log.Info(source, "sharing sender at an ASSUMED frame rate - Spout carries no fps metadata", map[string]any{
				"sender": n, "assumedFPS": fps, "w": w, "h": h,
				"hint": "if the source renders slower (a 30 fps OBS/Resolume canvas), set MediaLink " +
					"frame-rate cap to match - otherwise every frame is captured and encoded twice"})
		}
		desc := medialink.SourceDesc{ID: "spout:" + n, Name: n, Kind: medialink.KindVideo,
			Codec: medialink.CodecNRGBA, Width: w, Height: h, FPS: fps}
		name := n
		m.router.RegisterSource(desc, func(context.Context, medialink.Offer) (medialink.Source, error) {
			ww, hh, ok := m.senderSize(name)
			if !ok {
				return nil, fmt.Errorf("mediaroute: sender %q is gone", name)
			}
			return m.openSource(name, ww, hh)
		})
		m.mu.Lock()
		m.shared[n] = desc
		m.mu.Unlock()
	}
	// Vanished senders drop from the advert.
	m.mu.Lock()
	var gone []string
	for n := range m.shared {
		if !live[n] {
			gone = append(gone, n)
		}
	}
	for _, n := range gone {
		delete(m.shared, n)
	}
	m.mu.Unlock()
	for _, n := range gone {
		m.router.UnregisterSource("spout:" + n)
	}
}

// cleanup unregisters per-receive sinks whose route never came up / already ended.
func (m *Manager) cleanup() {
	active := map[string]bool{}
	for _, s := range m.router.Stats() {
		active[s.Session] = true
	}
	m.mu.Lock()
	var drop []Receive
	for sess, r := range m.receives {
		if !active[sess] && time.Since(r.Since) > receiveGrace {
			drop = append(drop, r)
			delete(m.receives, sess)
		}
	}
	m.mu.Unlock()
	for _, r := range drop {
		m.router.UnregisterSink(r.SinkID)
	}
}

// RemoteSource is one receivable video source on a paired instance (UI listing).
type RemoteSource struct {
	Peer string
	Desc medialink.SourceDesc
}

// RemoteVideoSources lists every paired instance's advertised video sources, stable order.
func (m *Manager) RemoteVideoSources() []RemoteSource {
	var out []RemoteSource
	for peer, ad := range m.router.RemoteAdverts() {
		for _, s := range ad.Sources {
			if s.Kind == medialink.KindVideo {
				out = append(out, RemoteSource{Peer: peer, Desc: s})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Peer != out[j].Peer {
			return out[i].Peer < out[j].Peer
		}
		return out[i].Desc.ID < out[j].Desc.ID
	})
	return out
}

// Receives snapshots the receive routes this instance requested (active or pending).
func (m *Manager) Receives() []Receive {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Receive, 0, len(m.receives))
	for _, r := range m.receives {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// StartReceive offers to pull peer's source into a fresh local Spout sender
// ("rave-mate link <name>"). Returns the route session id.
func (m *Manager) StartReceive(peer, sourceID string) (string, error) {
	if m.sameHost != nil && m.sameHost(peer) {
		return "", errors.New("same PC: pick the Spout sender directly - a route would re-encode for nothing (§3)")
	}
	// De-dup: one receive per (peer, source). Activating the same source twice stacked a second full
	// route (each pinning its own jitter buffer + Spout sender) - a big multiplier in the RAM blowup.
	m.mu.Lock()
	for _, r := range m.receives {
		if r.Peer == peer && r.SourceID == sourceID {
			sess := r.Session
			m.mu.Unlock()
			return sess, nil // already receiving this source - idempotent
		}
	}
	m.mu.Unlock()
	ad, ok := m.router.RemoteAdverts()[peer]
	if !ok {
		return "", fmt.Errorf("mediaroute: no advert from %s", peer)
	}
	var desc medialink.SourceDesc
	found := false
	for _, s := range ad.Sources {
		if s.ID == sourceID {
			desc, found = s, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("mediaroute: %s no longer advertises %q", peer, sourceID)
	}
	if desc.Width <= 0 || desc.Height <= 0 {
		return "", fmt.Errorf("mediaroute: source %q has no dimensions yet", desc.Name)
	}
	sinkID := "net:" + randID()
	sender := linkPrefix + desc.Name
	w, h := desc.Width, desc.Height
	m.router.RegisterSink(medialink.SinkDesc{ID: sinkID, Name: sender, Kind: medialink.KindVideo},
		func(context.Context, medialink.Answer) (medialink.Sink, error) {
			return m.openReceiveSink(sender, w, h)
		})
	cfg := m.cfg()
	opt := medialink.OfferOptions{
		BitrateKbps: cfg.Bitrate(),
		Decoders:    preferDecoders(cfg.PreferCodec, ad.Caps),
	}
	session, err := m.router.OfferRoute(peer, sourceID, sinkID, opt)
	if err != nil {
		m.router.UnregisterSink(sinkID)
		return "", err
	}
	m.mu.Lock()
	m.receives[session] = Receive{Session: session, Peer: peer, SourceID: sourceID, SinkID: sinkID,
		Name: desc.Name, Since: time.Now()}
	m.mu.Unlock()
	m.log.Info(source, "receive offered", map[string]any{"peer": peer, "source": desc.Name,
		"session": session, "sender": sender})
	return session, nil
}

// StopReceive tears one receive route down and unregisters its sink. Unlike a route restart, an
// explicit stop DESTROYS the kept-alive sender: mark it so the pending keptSink.Close closes the
// inner, or - if the route already ended and the sender is parked - close it directly here.
func (m *Manager) StopReceive(session string) {
	m.mu.Lock()
	r, ok := m.receives[session]
	delete(m.receives, session)
	var destroyNow medialink.Sink
	if ok {
		name := linkPrefix + r.Name
		if p, parked := m.parked[name]; parked {
			delete(m.parked, name)
			destroyNow = p.inner
		} else {
			m.stopping[name] = true
		}
	}
	m.mu.Unlock()
	m.router.CloseRoute(session)
	if ok {
		m.router.UnregisterSink(r.SinkID)
	}
	if destroyNow != nil {
		_ = destroyNow.Close()
		m.log.Info(source, "receive stopped - kept-alive sender destroyed", map[string]any{"session": session})
	}
}

// preferDecoders narrows the offered decode caps to the preferred codec - only when the target
// actually holds a matching encoder (else nil = full matrix, never a broken raw fallback).
func preferDecoders(prefer string, caps *medialink.Caps) []string {
	var want string
	switch strings.ToLower(strings.TrimSpace(prefer)) {
	case "hevc":
		want = medialink.DecodeHEVC
	case "h264":
		want = medialink.DecodeH264
	case "mjpeg":
		want = medialink.DecodeJPEG
	default:
		return nil // auto
	}
	if caps == nil {
		return nil
	}
	for _, enc := range caps.Encoders {
		if encoderCodec(enc) == want {
			return []string{want}
		}
	}
	return nil
}

// encoderCodec maps an ffmpeg encoder name to its decode-capability name.
func encoderCodec(enc string) string {
	switch {
	case strings.HasPrefix(enc, "hevc") || enc == "libx265":
		return medialink.DecodeHEVC
	case strings.HasPrefix(enc, "h264") || enc == "libx264":
		return medialink.DecodeH264
	case enc == "mjpeg":
		return medialink.DecodeJPEG
	}
	return ""
}

// ── kept-alive republish senders (VRAM interop-churn fix, mirrors 12b25a7) ────

// keptSink wraps a route's inner Spout sink so a route restart (peer reconnect, decoder hard-fail)
// PARKS the sender instead of destroying it. Destroying it makes a NEW DX11 shared texture on the
// next route, which forces Resolume to re-register its GL/DX interop; on NVIDIA that churn is
// cumulative and ends in E_OUTOFVIDEOMEMORY mid-set. It delegates Write/Close and forwards the
// optional ZeroCopySink / PipelineReporter surfaces so the native GPU decode path and the route
// panel keep working.
type keptSink struct {
	m     *Manager
	inner medialink.Sink
	name  string
	w, h  int
	blank []byte // reused transparent gate-out frame (w*h*4); carried across park/reopen, one per sender lineage
}

var _ medialink.ZeroCopySink = (*keptSink)(nil)
var _ medialink.PipelineReporter = (*keptSink)(nil)

func (k *keptSink) Write(f *medialink.Frame) error { return k.inner.Write(f) }

// SharedTexture forwards the inner sink's destination texture (medialink.ZeroCopySink) so the
// native decoder can still render straight into it; zero/false when the inner has none (CPU sink).
func (k *keptSink) SharedTexture() (uint64, uint32, int, int, string, bool) {
	if zc, ok := k.inner.(medialink.ZeroCopySink); ok {
		return zc.SharedTexture()
	}
	return 0, 0, 0, 0, "", false
}

// PipeStats forwards the inner sink's telemetry (medialink.PipelineReporter); zero when absent.
func (k *keptSink) PipeStats() medialink.PipelineStats {
	if pr, ok := k.inner.(medialink.PipelineReporter); ok {
		return pr.PipeStats()
	}
	return medialink.PipelineStats{}
}

// Close parks the inner sink (kept alive) unless StopReceive flagged it for destruction.
func (k *keptSink) Close() error { return k.m.closeKept(k) }

// parkedSink is a kept-alive republish sender held between routes. blank is the reusable transparent
// frame buffer for this sender's dims (freed with the entry).
type parkedSink struct {
	inner medialink.Sink
	w, h  int
	since time.Time
	blank []byte
}

// openReceiveSink is the route sink open path: reuse a parked sender (same dims) so its shared
// handle is unchanged, replace it on a dims change, or open a fresh one. The result is always a
// keptSink so the next Close parks rather than destroys.
func (m *Manager) openReceiveSink(name string, w, h int) (medialink.Sink, error) {
	m.mu.Lock()
	p, ok := m.parked[name]
	if ok {
		delete(m.parked, name)
	}
	m.mu.Unlock()
	if ok {
		if p.w == w && p.h == h {
			m.log.Info(source, "receive sink reopened - reusing kept-alive Spout sender (shared handle unchanged)",
				map[string]any{"sender": name, "w": w, "h": h})
			return &keptSink{m: m, inner: p.inner, name: name, w: w, h: h, blank: p.blank}, nil
		}
		m.log.Info(source, "receive sink dims changed - replacing kept-alive sender",
			map[string]any{"sender": name, "oldW": p.w, "oldH": p.h, "w": w, "h": h})
		_ = p.inner.Close()
	}
	inner, err := m.openSink(name, w, h)
	if err != nil {
		return nil, err
	}
	return &keptSink{m: m, inner: inner, name: name, w: w, h: h}, nil
}

// closeKept is keptSink.Close: destroy on an explicit stop, else publish ONE transparent frame and
// park the sender. Blocking sink I/O runs outside m.mu (Send waits for the worker read; Close joins).
func (m *Manager) closeKept(k *keptSink) error {
	m.mu.Lock()
	destroy := m.stopping[k.name]
	if destroy {
		delete(m.stopping, k.name)
	}
	m.mu.Unlock()
	if destroy {
		_ = k.inner.Close()
		return nil
	}
	if k.blank == nil {
		k.blank = make([]byte, k.w*k.h*4) // one reusable blank per sender lineage; a 4K blank is 33 MB - never per-frame/per-close
	}
	if err := k.inner.Write(&medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA, Payload: k.blank}); err != nil {
		m.log.Debug(source, "kept-alive park: transparent frame write failed", map[string]any{"sender": k.name, "err": err.Error()})
	}
	m.parkKept(k)
	return nil
}

// parkKept adds k to the parked cache under the cap (drop-oldest; evicted inner closed outside mu).
func (m *Manager) parkKept(k *keptSink) {
	entry := &parkedSink{inner: k.inner, w: k.w, h: k.h, since: m.now(), blank: k.blank}
	m.mu.Lock()
	var evName string
	var evicted medialink.Sink
	if old, exists := m.parked[k.name]; exists {
		evName, evicted = k.name, old.inner // stale duplicate under one name: close it, don't leak
	} else if len(m.parked) >= parkCap {
		var oldest time.Time
		for n, p := range m.parked {
			if evName == "" || p.since.Before(oldest) {
				evName, oldest = n, p.since
			}
		}
		evicted = m.parked[evName].inner
		delete(m.parked, evName)
	}
	m.parked[k.name] = entry
	m.mu.Unlock()
	if evicted != nil {
		_ = evicted.Close()
		m.log.Info(source, "kept-alive sender evicted - cache full (drop-oldest)", map[string]any{"sender": evName})
	}
}

// reapParked destroys parked senders idle longer than parkTTL (inner Close runs outside mu).
func (m *Manager) reapParked() {
	now := m.now()
	m.mu.Lock()
	var expired []struct {
		name  string
		inner medialink.Sink
	}
	for n, p := range m.parked {
		if now.Sub(p.since) > parkTTL {
			expired = append(expired, struct {
				name  string
				inner medialink.Sink
			}{n, p.inner})
			delete(m.parked, n)
		}
	}
	m.mu.Unlock()
	for _, e := range expired {
		_ = e.inner.Close()
		m.log.Info(source, "kept-alive sender released after idle TTL", map[string]any{"sender": e.name})
	}
}

// destroyAllParked closes every held sender (Start shutdown).
func (m *Manager) destroyAllParked() {
	m.mu.Lock()
	all := m.parked
	m.parked = map[string]*parkedSink{}
	m.mu.Unlock()
	for _, p := range all {
		_ = p.inner.Close()
	}
	if len(all) > 0 {
		m.log.Info(source, "kept-alive senders released on shutdown", map[string]any{"count": len(all)})
	}
}

// ── videoshare-backed source/sink ─────────────────────────────────────────────

// spoutSource adapts a shared capture subscription to a medialink Source. Frames carry a Release
// hook (refcounted pooled capture buffer); the per-route fps cap drops over-budget frames here,
// before any encode/crypto cost - the readback itself is already capped inside the receiver
// (videoshare.RecvOptions.MaxFPS), which is what actually bounds sender bandwidth.
//
// The capture attaches LAZILY, on the first Next. A zero-copy encode session (the native MF
// child reading the sender's shared texture itself, medialink.ZeroCopySource) never calls Next,
// and an eager attach would spin the GL context + readback + pooled buffers for a consumer that
// does not exist - exactly the cost zigmedia inc 1 removes.
type spoutSource struct {
	name    string
	maxFPS  float64
	attach  func(name string, maxFPS float64) (captureFeed, error)
	shareOf func(name string) (uint64, uint32, int, int, bool)

	minGap time.Duration // MediaLink.MaxFPS cap (0 = uncapped)
	last   time.Time
	// capped counts frames discarded BY THE FPS CAP - deliberate rate limiting, not loss. It
	// rides PipelineStats.RateCapped so the panel can say so; folding it into Dropped alone made
	// a healthy 60 fps source feeding a 40 fps route read "dropped 41902 and climbing".
	capped atomic.Uint64

	mu   sync.Mutex
	feed captureFeed // nil until the first Next attaches (or a test injects one)
}

func (m *Manager) openSpoutSource(name string, _, _ int) (medialink.Source, error) {
	fps := m.cfg().FPSCap()
	s := &spoutSource{
		name: name, maxFPS: float64(fps), shareOf: m.senderShare,
		// Shared capture: N routes on one Spout sender fan out from ONE readback (capture.go).
		attach: func(n string, f float64) (captureFeed, error) { return m.hub.attach(n, f) },
	}
	if fps > 0 {
		s.minGap = time.Duration(float64(time.Second) / float64(fps))
	}
	return s, nil
}

// SharedTexture implements medialink.ZeroCopySource: the sender's DX11 shared-texture handle +
// format, resolved from the (cached) registry scan. Pure lookup - it never opens a capture.
func (s *spoutSource) SharedTexture() (uint64, uint32, int, int, string, bool) {
	if s.shareOf == nil || s.name == "" {
		return 0, 0, 0, 0, "", false
	}
	h, fmt, w, hh, ok := s.shareOf(s.name)
	if !ok {
		return 0, 0, 0, 0, "", false
	}
	return h, fmt, w, hh, s.name, true
}

// PipeStats implements medialink.PipelineReporter: the per-route fps-cap discards, reported BOTH
// in the Dropped total (so the whole-chain number stays complete) and in RateCapped (so the panel
// can separate deliberate throttling from real frame loss).
func (s *spoutSource) PipeStats() medialink.PipelineStats {
	n := s.capped.Load()
	return medialink.PipelineStats{Dropped: n, RateCapped: n}
}

// feedOrAttach returns this route's capture feed, opening the shared capture on first use.
func (s *spoutSource) feedOrAttach() (captureFeed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.feed != nil {
		return s.feed, nil
	}
	if s.attach == nil {
		return nil, errors.New("mediaroute: spout source has no capture backend")
	}
	f, err := s.attach(s.name, s.maxFPS)
	if err != nil {
		return nil, err
	}
	s.feed = f
	return f, nil
}

func (s *spoutSource) Next(ctx context.Context) (*medialink.Frame, error) {
	feed, err := s.feedOrAttach()
	if err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case f, ok := <-feed.frames():
			if !ok {
				return nil, io.EOF // capture ended - route ends cleanly
			}
			if s.minGap > 0 {
				now := time.Now()
				if now.Sub(s.last) < s.minGap {
					f.ref.release() // over this route's fps budget - drop our reference
					s.capped.Add(1)
					continue
				}
				s.last = now
			}
			ref := f.ref
			return &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
				Payload: f.img.Pix, Release: ref.release}, nil
		}
	}
}

// Close releases this route's capture subscription. A source that never attached (zero-copy
// route) has nothing to close - the readback was never opened.
func (s *spoutSource) Close() error {
	s.mu.Lock()
	f := s.feed
	s.feed = nil
	s.mu.Unlock()
	if f != nil {
		f.close()
	}
	return nil
}

// spoutSink presents decoded frames as a named local Spout sender. Write is called serially by the
// route's jitter drain, so the diagnostic counters need no locking.
type spoutSink struct {
	log *logbus.Bus
	fs  videoshare.FrameSender
	// shared is non-nil when the sender was opened eagerly with its destination GPU texture
	// exposed (zigmedia inc 2). It IS fs - the same object - so a refused native decode session
	// still publishes through Write without needing a second sender under this name.
	shared    videoshare.SharedSender
	name      string
	w, h      int
	sentOne   bool          // logged the first delivered frame (sender is now visible)
	dropped   atomic.Uint64 // frames skipped for wrong kind/size (compressed passthrough, dims mismatch)
	loggedBad bool          // logged the first drop once (rate-limit)
	// pubFrames/pubBytes are the volume Write actually PUBLISHED. Write returns nil for a frame it
	// threw away - an error-shaped contract over a volume-shaped operation, which is exactly how a
	// total failure hides: "no error" is indistinguishable from "nothing happened". Dropped cannot
	// stand in for it either, because a sink that drops nothing and publishes nothing reports the
	// same zero as a healthy idle one.
	pubFrames atomic.Uint64
	pubBytes  atomic.Uint64
}

// PipeStats implements medialink.PipelineReporter: the decode wrapper sums these drops into the
// route's Dropped and carries the published volume up, so "route up, frames arriving, no Spout
// sender" is visible as a number instead of as a Write that answered nil.
func (s *spoutSink) PipeStats() medialink.PipelineStats {
	fs := framedebug.For(s.stage()).Stats()
	return medialink.PipelineStats{Dropped: s.dropped.Load(),
		PubFrames: s.pubFrames.Load(), PubBytes: s.pubBytes.Load(),
		PubStalledMs: fs.StalledMs, PubChanges: fs.Changes, PubHash: fs.Hash,
		PubPeakFrac: fs.PeakFrac}
}

// stage names this sink's framedebug recorder. Per-sender, so two routes publishing different
// pictures cannot average each other's stall away.
func (s *spoutSink) stage() string { return "out:" + s.name }

func (m *Manager) openSpoutSink(name string, w, h int) (medialink.Sink, error) {
	// Native decode wanted → open the sender EAGERLY so its destination texture exists before the
	// decode engine is chosen (a decoder cannot create one). A failure here is not fatal: fall
	// straight through to the lazy frame sender, i.e. today's path byte for byte.
	if m.cfg().ZeroCopyDecode() && m.newSharedSnd != nil {
		if ss, err := m.newSharedSnd(name, w, h); err == nil {
			m.log.Info(source, "receive sink open (GPU destination texture)", map[string]any{
				"sender": name, "w": w, "h": h, "fmt": ss.Format()})
			return &spoutSink{log: m.log, fs: ss, shared: ss, name: name, w: w, h: h}, nil
		} else {
			m.log.Warn(source, "no GPU destination texture for this receive sink - using the frame path",
				map[string]any{"sender": name, "err": err.Error()})
		}
	}
	fs, err := videoshare.NewFrameSender(m.log, name)
	if err != nil {
		return nil, fmt.Errorf("mediaroute: video share unavailable: %w", err)
	}
	m.log.Info(source, "receive sink open", map[string]any{"sender": name, "w": w, "h": h})
	return &spoutSink{log: m.log, fs: fs, name: name, w: w, h: h}, nil
}

// SharedTexture implements medialink.ZeroCopySink: the local sender's destination texture. Pure
// lookup of scalars resolved at open - it never touches a pixel and never creates anything.
func (s *spoutSink) SharedTexture() (uint64, uint32, int, int, string, bool) {
	if s.shared == nil {
		return 0, 0, 0, 0, "", false
	}
	h := s.shared.Handle()
	if h == 0 {
		return 0, 0, 0, 0, "", false
	}
	return h, s.shared.Format(), s.w, s.h, s.name, true
}

func (s *spoutSink) Write(f *medialink.Frame) error {
	want := s.w * s.h * 4
	if f.Kind != medialink.KindVideo || len(f.Payload) < want {
		// Undecoded/foreign frame - skip, never fatal. But a *sustained* stream of these means the
		// Spout sender never materializes (it's created on the first real SendImage), so surface it
		// once: this is the "route up, frames received, but no Spout source" failure.
		s.dropped.Add(1)
		if !s.loggedBad {
			s.loggedBad = true
			s.log.Warn(source, "receive sink dropping frames - no Spout sender will appear", map[string]any{
				"sender": s.name, "kind": int(f.Kind), "gotBytes": len(f.Payload), "wantBytes": want,
				"hint": "compressed frame reached the raw sink (decode bypassed) or dims mismatch"})
		}
		return nil
	}
	img := &image.NRGBA{Pix: f.Payload, Stride: s.w * 4, Rect: image.Rect(0, 0, s.w, s.h)}
	// Observe BEFORE Send: the payload is a pooled buffer the sink does not own past this call.
	framedebug.For(s.stage()).Frame(img)
	// Testcard self-detects (6 samples on a non-card frame): when the diagnostic card is routed,
	// this stage proves exactly which frames arrived, repeated or went missing.
	testcard.Observe(s.stage(), img)
	if err := s.fs.Send(img); err != nil {
		return err
	}
	s.pubFrames.Add(1)
	s.pubBytes.Add(uint64(want))
	if !s.sentOne {
		s.sentOne = true
		s.log.Info(source, "receive sink live - Spout sender publishing", map[string]any{
			"sender": s.name, "w": s.w, "h": s.h, "droppedBefore": s.dropped.Load()})
	}
	return nil
}

func (s *spoutSink) Close() error { s.fs.Close(); return nil }

func randID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
