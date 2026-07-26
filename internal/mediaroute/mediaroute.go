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
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/videoshare"
)

const (
	source        = "mediaroute"
	scanEvery     = 2 * time.Second
	linkPrefix    = "rave-mate link " // receive-output sender names (excluded from sharing: loop guard)
	camPrefix     = "rave-mate cam "  // the webcam feature's OWN capture sender - already a direct "webcam" source; re-sharing it double-advertises the camera
	receiveGrace  = 30 * time.Second  // offered-but-never-up receives are cleaned after this
	defaultFPSAdv = 60                // Spout carries no fps metadata; advertise the common case
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
	OpenSource  func(name string, w, h int) (medialink.Source, error)
	OpenSink    func(name string, w, h int) (medialink.Sink, error)
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

	listSenders func() []string
	senderSize  func(string) (int, int, bool)
	openSource  func(string, int, int) (medialink.Source, error)
	openSink    func(string, int, int) (medialink.Sink, error)

	mu       sync.Mutex
	shared   map[string]medialink.SourceDesc // sender name → advertised desc
	receives map[string]Receive              // session → state
}

// New builds the manager (inert until Start).
func New(o Options) *Manager {
	m := &Manager{
		log: o.Log, router: o.Router, cfg: o.Cfg, sameHost: o.SameHost,
		listSenders: o.ListSenders, senderSize: o.SenderSize,
		openSource: o.OpenSource, openSink: o.OpenSink,
		shared: map[string]medialink.SourceDesc{}, receives: map[string]Receive{},
	}
	if m.listSenders == nil {
		m.listSenders = videoshare.ListSenders
	}
	if m.senderSize == nil {
		m.senderSize = videoshare.SenderSize
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
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.scan()
				m.cleanup()
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
			return m.openSink(sender, w, h)
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

// StopReceive tears one receive route down and unregisters its sink.
func (m *Manager) StopReceive(session string) {
	m.mu.Lock()
	r, ok := m.receives[session]
	delete(m.receives, session)
	m.mu.Unlock()
	m.router.CloseRoute(session)
	if ok {
		m.router.UnregisterSink(r.SinkID)
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

// ── videoshare-backed source/sink ─────────────────────────────────────────────

// spoutSource adapts a FrameReceiver to a medialink Source. Frames carry a Release hook
// (pooled capture buffers); the sender-side fps cap drops over-budget frames here, before
// any encode/crypto cost.
type spoutSource struct {
	recv   videoshare.FrameReceiver
	minGap time.Duration // MediaLink.MaxFPS cap (0 = uncapped)
	last   time.Time
}

func (m *Manager) openSpoutSource(name string, _, _ int) (medialink.Source, error) {
	fps := m.cfg().FPSCap()
	// The cap goes INTO the receiver: an over-budget poll skips ReceiveImage entirely, so a 120 fps
	// VJ source capped to 60 pays 60 GPU→CPU readbacks/s instead of 120. The gate below stays as the
	// downstream guard (shared capture runs at the fastest route's rate; slower routes drop here).
	recv, err := videoshare.NewFrameReceiverOpts(m.log, name, videoshare.RecvOptions{MaxFPS: float64(fps)})
	if err != nil {
		return nil, err
	}
	s := &spoutSource{recv: recv}
	if fps > 0 {
		s.minGap = time.Duration(float64(time.Second) / float64(fps))
	}
	return s, nil
}

func (s *spoutSource) Next(ctx context.Context) (*medialink.Frame, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case img, ok := <-s.recv.Frames():
			if !ok {
				return nil, io.EOF // receiver closed - route ends cleanly
			}
			if s.minGap > 0 {
				now := time.Now()
				if now.Sub(s.last) < s.minGap {
					videoshare.PutPix(img.Pix) // over the fps budget - recycle + wait for the next
					continue
				}
				s.last = now
			}
			pix := img.Pix
			return &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
				Payload: pix, Release: func() { videoshare.PutPix(pix) }}, nil
		}
	}
}

func (s *spoutSource) Close() error { s.recv.Close(); return nil }

// spoutSink presents decoded frames as a named local Spout sender. Write is called serially by the
// route's jitter drain, so the diagnostic counters need no locking.
type spoutSink struct {
	log       *logbus.Bus
	fs        videoshare.FrameSender
	name      string
	w, h      int
	sentOne   bool // logged the first delivered frame (sender is now visible)
	dropped   int  // frames skipped for wrong kind/size (compressed passthrough, dims mismatch)
	loggedBad bool // logged the first drop once (rate-limit)
}

func (m *Manager) openSpoutSink(name string, w, h int) (medialink.Sink, error) {
	fs, err := videoshare.NewFrameSender(m.log, name)
	if err != nil {
		return nil, fmt.Errorf("mediaroute: video share unavailable: %w", err)
	}
	m.log.Info(source, "receive sink open", map[string]any{"sender": name, "w": w, "h": h})
	return &spoutSink{log: m.log, fs: fs, name: name, w: w, h: h}, nil
}

func (s *spoutSink) Write(f *medialink.Frame) error {
	want := s.w * s.h * 4
	if f.Kind != medialink.KindVideo || len(f.Payload) < want {
		// Undecoded/foreign frame - skip, never fatal. But a *sustained* stream of these means the
		// Spout sender never materializes (it's created on the first real SendImage), so surface it
		// once: this is the "route up, frames received, but no Spout source" failure.
		s.dropped++
		if !s.loggedBad {
			s.loggedBad = true
			s.log.Warn(source, "receive sink dropping frames - no Spout sender will appear", map[string]any{
				"sender": s.name, "kind": int(f.Kind), "gotBytes": len(f.Payload), "wantBytes": want,
				"hint": "compressed frame reached the raw sink (decode bypassed) or dims mismatch"})
		}
		return nil
	}
	if err := s.fs.Send(&image.NRGBA{Pix: f.Payload, Stride: s.w * 4,
		Rect: image.Rect(0, 0, s.w, s.h)}); err != nil {
		return err
	}
	if !s.sentOne {
		s.sentOne = true
		s.log.Info(source, "receive sink live - Spout sender publishing", map[string]any{
			"sender": s.name, "w": s.w, "h": s.h, "droppedBefore": s.dropped})
	}
	return nil
}

func (s *spoutSink) Close() error { s.fs.Close(); return nil }

func randID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
