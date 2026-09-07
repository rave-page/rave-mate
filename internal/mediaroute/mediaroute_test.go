package mediaroute

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

// fakeRouter records medialink surface calls.
type fakeRouter struct {
	mu       sync.Mutex
	sources  map[string]medialink.SourceDesc
	sinks    map[string]medialink.SinkOpen
	adverts  map[string]medialink.Advert
	stats    []medialink.RouteStat
	offers   []medialink.OfferOptions
	offerErr error
	closed   []string
	session  string
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{sources: map[string]medialink.SourceDesc{},
		sinks: map[string]medialink.SinkOpen{}, adverts: map[string]medialink.Advert{},
		session: "sess-1"}
}
func (r *fakeRouter) RegisterSource(d medialink.SourceDesc, _ medialink.SourceOpen) {
	r.mu.Lock()
	r.sources[d.ID] = d
	r.mu.Unlock()
}
func (r *fakeRouter) UnregisterSource(id string) {
	r.mu.Lock()
	delete(r.sources, id)
	r.mu.Unlock()
}
func (r *fakeRouter) RegisterSink(d medialink.SinkDesc, open medialink.SinkOpen) {
	r.mu.Lock()
	r.sinks[d.ID] = open
	r.mu.Unlock()
}
func (r *fakeRouter) UnregisterSink(id string) {
	r.mu.Lock()
	delete(r.sinks, id)
	r.mu.Unlock()
}
func (r *fakeRouter) OfferRoute(_, _, _ string, opt medialink.OfferOptions) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offerErr != nil {
		return "", r.offerErr
	}
	r.offers = append(r.offers, opt)
	return r.session, nil
}
func (r *fakeRouter) CloseRoute(session string) {
	r.mu.Lock()
	r.closed = append(r.closed, session)
	r.mu.Unlock()
}
func (r *fakeRouter) RemoteAdverts() map[string]medialink.Advert {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]medialink.Advert{}
	for k, v := range r.adverts {
		out[k] = v
	}
	return out
}
func (r *fakeRouter) Stats() []medialink.RouteStat {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]medialink.RouteStat{}, r.stats...)
}

func testManager(fr *fakeRouter, cfg config.MediaLinkFeature, senders []string) *Manager {
	return New(Options{
		Log: logbus.New(16), Router: fr,
		Cfg:         func() config.MediaLinkFeature { return cfg },
		ListSenders: func() []string { return senders },
		SenderSize:  func(string) (int, int, bool) { return 1920, 1080, true },
		OpenSource: func(string, int, int) (medialink.Source, error) {
			return medialink.NewChanSource(make(chan *medialink.Frame)), nil
		},
		OpenSink: func(string, int, int) (medialink.Sink, error) { return nopSink{}, nil },
	})
}

type nopSink struct{}

func (nopSink) Write(*medialink.Frame) error { return nil }
func (nopSink) Close() error                 { return nil }

// countingSink records opens/closes/writes for the kept-alive tests and forwards a ZeroCopySink
// handle so keptSink's SharedTexture forwarding is exercised.
type countingSink struct {
	mu     sync.Mutex
	w, h   int
	shared bool
	handle uint64
	writes [][]byte
	closed int
}

func (c *countingSink) Write(f *medialink.Frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := make([]byte, len(f.Payload))
	copy(b, f.Payload)
	c.writes = append(c.writes, b)
	return nil
}
func (c *countingSink) Close() error {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
	return nil
}
func (c *countingSink) SharedTexture() (uint64, uint32, int, int, string, bool) {
	if !c.shared {
		return 0, 0, 0, 0, "", false
	}
	return c.handle, 87, c.w, c.h, "tex", true
}

// sinkFactory is a counting Options.OpenSink seam.
type sinkFactory struct {
	mu     sync.Mutex
	opens  int
	shared bool
	sinks  []*countingSink
}

func (sf *sinkFactory) open(_ string, w, h int) (medialink.Sink, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.opens++
	cs := &countingSink{w: w, h: h, shared: sf.shared, handle: uint64(0xABCD00 + sf.opens)}
	sf.sinks = append(sf.sinks, cs)
	return cs, nil
}

func keptManager(sf *sinkFactory) *Manager {
	return New(Options{
		Log: logbus.New(16), Router: newFakeRouter(),
		Cfg:      func() config.MediaLinkFeature { return config.MediaLinkFeature{} },
		OpenSink: sf.open,
	})
}

func TestReceiveSinkKeptAliveAcrossRestart(t *testing.T) {
	sf := &sinkFactory{shared: true}
	m := keptManager(sf)
	name := linkPrefix + "OBS"
	w, h := 8, 4
	s1, err := m.openReceiveSink(name, w, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil { // route restart: parks + publishes one blank
		t.Fatal(err)
	}
	s2, err := m.openReceiveSink(name, w, h)
	if err != nil {
		t.Fatal(err)
	}
	if sf.opens != 1 {
		t.Fatalf("opens=%d, want 1 (sender reused across restart)", sf.opens)
	}
	// SharedTexture forwards the reused inner sink's value.
	hnd, dxfmt, gw, gh, _, ok := s2.(*keptSink).SharedTexture()
	if !ok || gw != w || gh != h || dxfmt != 87 {
		t.Fatalf("shared texture not forwarded: ok=%v %dx%d fmt=%d", ok, gw, gh, dxfmt)
	}
	cs := sf.sinks[0]
	if hnd != cs.handle {
		t.Fatalf("handle=%#x, want %#x", hnd, cs.handle)
	}
	// Exactly one zero-filled blank frame of w*h*4 bytes was published on park.
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.writes) != 1 {
		t.Fatalf("writes=%d, want 1 blank on park", len(cs.writes))
	}
	if len(cs.writes[0]) != w*h*4 {
		t.Fatalf("blank len=%d, want %d", len(cs.writes[0]), w*h*4)
	}
	for i, b := range cs.writes[0] {
		if b != 0 {
			t.Fatalf("blank byte %d = %d, want 0", i, b)
		}
	}
}

func TestReceiveSinkDestroyedOnExplicitStop(t *testing.T) {
	sf := &sinkFactory{}
	fr := newFakeRouter()
	desc := medialink.SourceDesc{ID: "spout:OBS", Name: "OBS", Kind: medialink.KindVideo,
		Width: 8, Height: 4, FPS: 60}
	fr.adverts["peerB"] = advertWith(desc, "hevc_nvenc")
	m := New(Options{Log: logbus.New(16), Router: fr,
		Cfg:      func() config.MediaLinkFeature { return config.MediaLinkFeature{} },
		OpenSink: sf.open})
	sess, err := m.StartReceive("peerB", "spout:OBS")
	if err != nil {
		t.Fatal(err)
	}
	fr.mu.Lock()
	var open medialink.SinkOpen
	for _, o := range fr.sinks {
		open = o
	}
	fr.mu.Unlock()
	s, err := open(context.Background(), medialink.Answer{})
	if err != nil {
		t.Fatal(err)
	}
	m.StopReceive(sess)               // explicit stop → destroy, not park
	if err := s.Close(); err != nil { // route teardown
		t.Fatal(err)
	}
	if sf.opens != 1 || sf.sinks[0].closed != 1 {
		t.Fatalf("explicit stop must destroy: opens=%d closed=%d", sf.opens, sf.sinks[0].closed)
	}
	if n := len(sf.sinks[0].writes); n != 0 {
		t.Fatalf("stop wrote %d frames, want 0 (destroy, not park)", n)
	}
	m.mu.Lock()
	np := len(m.parked)
	m.mu.Unlock()
	if np != 0 {
		t.Fatalf("parked=%d after stop, want 0", np)
	}
}

func TestReceiveSinkDimsChangeReplaces(t *testing.T) {
	sf := &sinkFactory{}
	m := keptManager(sf)
	name := linkPrefix + "OBS"
	s1, _ := m.openReceiveSink(name, 8, 4)
	_ = s1.Close() // park at 8x4
	s2, err := m.openReceiveSink(name, 16, 8)
	if err != nil {
		t.Fatal(err)
	}
	_ = s2
	if sf.opens != 2 {
		t.Fatalf("opens=%d, want 2 (fresh sender for new dims)", sf.opens)
	}
	if sf.sinks[0].closed != 1 {
		t.Fatalf("old sender not closed on dims change: closed=%d", sf.sinks[0].closed)
	}
}

func TestReceiveSinkTTLReaped(t *testing.T) {
	sf := &sinkFactory{}
	m := keptManager(sf)
	now := time.Now()
	m.now = func() time.Time { return now }
	name := linkPrefix + "OBS"
	s1, _ := m.openReceiveSink(name, 8, 4)
	_ = s1.Close() // parked at `now`
	m.reapParked()
	if sf.sinks[0].closed != 0 {
		t.Fatal("reaped before TTL")
	}
	now = now.Add(parkTTL + time.Second)
	m.reapParked()
	if sf.sinks[0].closed != 1 {
		t.Fatalf("TTL reaper did not close: closed=%d", sf.sinks[0].closed)
	}
	m.mu.Lock()
	np := len(m.parked)
	m.mu.Unlock()
	if np != 0 {
		t.Fatalf("parked=%d after reap, want 0", np)
	}
}

func TestReceiveSinkCacheCapEvictsOldest(t *testing.T) {
	sf := &sinkFactory{}
	m := keptManager(sf)
	base := time.Now()
	for i := 0; i < parkCap+1; i++ {
		t0 := base.Add(time.Duration(i) * time.Second)
		m.now = func() time.Time { return t0 }
		s, _ := m.openReceiveSink(linkPrefix+fmt.Sprintf("S%d", i), 4, 2)
		_ = s.Close()
	}
	closes := 0
	for _, cs := range sf.sinks {
		closes += cs.closed
	}
	if closes != 1 {
		t.Fatalf("cap evict: total closes=%d, want 1", closes)
	}
	m.mu.Lock()
	np := len(m.parked)
	_, s0 := m.parked[linkPrefix+"S0"]
	m.mu.Unlock()
	if np != parkCap {
		t.Fatalf("parked=%d, want %d", np, parkCap)
	}
	if s0 {
		t.Fatal("oldest (S0) not evicted")
	}
}

func TestScanSharesAndPrunes(t *testing.T) {
	fr := newFakeRouter()
	senders := []string{"OBS Spout", "rave-mate link Remote", "Resolume Out"}
	m := testManager(fr, config.MediaLinkFeature{ShareVideo: true}, senders)
	m.scan()
	fr.mu.Lock()
	if len(fr.sources) != 2 {
		t.Fatalf("shared %d sources: %+v", len(fr.sources), fr.sources)
	}
	d, ok := fr.sources["spout:OBS Spout"]
	fr.mu.Unlock()
	if !ok || d.Width != 1920 || d.Kind != medialink.KindVideo || d.Codec != medialink.CodecNRGBA {
		t.Fatalf("desc: %+v", d)
	}
	// The received-route sender is never re-shared (loop guard).
	fr.mu.Lock()
	_, leaked := fr.sources["spout:rave-mate link Remote"]
	fr.mu.Unlock()
	if leaked {
		t.Fatal("link sender re-shared")
	}
	// A vanished sender is unregistered.
	m.listSenders = func() []string { return []string{"OBS Spout"} }
	m.scan()
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if _, still := fr.sources["spout:Resolume Out"]; still || len(fr.sources) != 1 {
		t.Fatalf("prune failed: %+v", fr.sources)
	}
}

func TestScanOffWithoutShareVideo(t *testing.T) {
	fr := newFakeRouter()
	m := testManager(fr, config.MediaLinkFeature{}, []string{"OBS Spout"})
	m.scan()
	if len(fr.sources) != 0 {
		t.Fatalf("sharing must be opt-in: %+v", fr.sources)
	}
}

func advertWith(desc medialink.SourceDesc, encoders ...string) medialink.Advert {
	return medialink.Advert{Sources: []medialink.SourceDesc{desc},
		Caps: &medialink.Caps{Encoders: encoders}}
}

func TestStartReceiveOffersAndStop(t *testing.T) {
	fr := newFakeRouter()
	desc := medialink.SourceDesc{ID: "spout:OBS", Name: "OBS", Kind: medialink.KindVideo,
		Width: 1280, Height: 720, FPS: 60}
	fr.adverts["peerB"] = advertWith(desc, "hevc_nvenc")
	m := testManager(fr, config.MediaLinkFeature{BitrateKbps: 8000}, nil)

	sess, err := m.StartReceive("peerB", "spout:OBS")
	if err != nil || sess != "sess-1" {
		t.Fatalf("StartReceive: %v %q", err, sess)
	}
	fr.mu.Lock()
	if len(fr.sinks) != 1 || len(fr.offers) != 1 {
		t.Fatalf("sink/offer not registered: %d/%d", len(fr.sinks), len(fr.offers))
	}
	opt := fr.offers[0]
	fr.mu.Unlock()
	if opt.BitrateKbps != 8000 || opt.Decoders != nil {
		t.Fatalf("offer options: %+v", opt)
	}
	if got := m.Receives(); len(got) != 1 || got[0].Peer != "peerB" || got[0].Name != "OBS" {
		t.Fatalf("receives: %+v", got)
	}
	// The registered sink opens the local "rave-mate link" sender.
	fr.mu.Lock()
	var open medialink.SinkOpen
	var sinkID string
	for id, o := range fr.sinks {
		sinkID, open = id, o
	}
	fr.mu.Unlock()
	if !strings.HasPrefix(sinkID, "net:") {
		t.Fatalf("sink id: %q", sinkID)
	}
	if _, err := open(context.Background(), medialink.Answer{}); err != nil {
		t.Fatalf("sink open: %v", err)
	}
	m.StopReceive(sess)
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.closed) != 1 || fr.closed[0] != sess || len(fr.sinks) != 0 {
		t.Fatalf("stop: closed=%v sinks=%d", fr.closed, len(fr.sinks))
	}
}

func TestStartReceiveGuards(t *testing.T) {
	fr := newFakeRouter()
	desc := medialink.SourceDesc{ID: "s", Name: "S", Kind: medialink.KindVideo, Width: 640, Height: 480}
	fr.adverts["peerB"] = advertWith(desc)
	m := testManager(fr, config.MediaLinkFeature{}, nil)
	m.sameHost = func(peer string) bool { return peer == "peerB" }
	// §3 same-PC rule: refused.
	if _, err := m.StartReceive("peerB", "s"); err == nil || !strings.Contains(err.Error(), "same PC") {
		t.Fatalf("same-PC guard: %v", err)
	}
	// Unknown peer / source.
	if _, err := m.StartReceive("nobody", "s"); err == nil {
		t.Fatal("unknown peer must error")
	}
	m.sameHost = nil
	if _, err := m.StartReceive("peerB", "wrong"); err == nil {
		t.Fatal("unknown source must error")
	}
	// Dimensionless source refused (decode framing needs W/H).
	fr.adverts["peerC"] = advertWith(medialink.SourceDesc{ID: "d", Name: "D", Kind: medialink.KindVideo})
	if _, err := m.StartReceive("peerC", "d"); err == nil {
		t.Fatal("dimensionless source must error")
	}
	if len(fr.sinks) != 0 {
		t.Fatalf("failed receives must not leak sinks: %+v", fr.sinks)
	}
}

func TestPreferDecoders(t *testing.T) {
	caps := &medialink.Caps{Encoders: []string{"hevc_nvenc", "h264_nvenc", "mjpeg"}}
	cases := []struct {
		prefer string
		caps   *medialink.Caps
		want   []string
	}{
		{"", caps, nil}, // auto
		{"auto", caps, nil},
		{"hevc", caps, []string{medialink.DecodeHEVC}},
		{"H264", caps, []string{medialink.DecodeH264}},
		{"mjpeg", caps, []string{medialink.DecodeJPEG}},
		{"hevc", &medialink.Caps{Encoders: []string{"libx264"}}, nil}, // target can't encode it
		{"hevc", nil, nil}, // P1 peer
	}
	for _, c := range cases {
		got := preferDecoders(c.prefer, c.caps)
		if len(got) != len(c.want) || (len(got) == 1 && got[0] != c.want[0]) {
			t.Errorf("preferDecoders(%q) = %v, want %v", c.prefer, got, c.want)
		}
	}
}

func TestCleanupUnregistersDeadReceives(t *testing.T) {
	fr := newFakeRouter()
	desc := medialink.SourceDesc{ID: "s", Name: "S", Kind: medialink.KindVideo, Width: 64, Height: 48}
	fr.adverts["peerB"] = advertWith(desc)
	m := testManager(fr, config.MediaLinkFeature{}, nil)
	sess, err := m.StartReceive("peerB", "s")
	if err != nil {
		t.Fatal(err)
	}
	// Route never came up; age past the grace window.
	m.mu.Lock()
	r := m.receives[sess]
	r.Since = time.Now().Add(-2 * receiveGrace)
	m.receives[sess] = r
	m.mu.Unlock()
	m.cleanup()
	fr.mu.Lock()
	nSinks := len(fr.sinks)
	fr.mu.Unlock()
	if nSinks != 0 {
		t.Fatal("dead receive sink not cleaned")
	}
	// An ACTIVE session is kept.
	fr.mu.Lock()
	fr.session = "sess-2"
	fr.mu.Unlock()
	sess2, err := m.StartReceive("peerB", "s")
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	r2 := m.receives[sess2]
	r2.Since = time.Now().Add(-2 * receiveGrace)
	m.receives[sess2] = r2
	m.mu.Unlock()
	fr.mu.Lock()
	fr.stats = []medialink.RouteStat{{Session: sess2}}
	fr.mu.Unlock()
	m.cleanup()
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.sinks) != 1 {
		t.Fatalf("active receive must keep its sink: %+v", fr.sinks)
	}
}
