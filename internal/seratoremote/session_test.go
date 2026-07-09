package seratoremote

import (
	"context"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/osc"
)

// Ported from serato-connect tests/remote/session.test.ts. The handshake tests drive a
// session over an in-memory net.Pipe; the status-dispatch tests exercise the Receiver's
// stateful router directly.

// collector reads frames off a conn into a slice until stop() is called.
type collector struct {
	mu   sync.Mutex
	msgs []osc.Message
}

func startCollector(conn net.Conn) *collector {
	c := &collector{}
	go func() {
		r := newFrameReader(0)
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				msgs, _ := r.push(buf[:n], nil)
				c.mu.Lock()
				c.msgs = append(c.msgs, msgs...)
				c.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *collector) has(addr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if m.Address == addr {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func newTestSession(t *testing.T, cb Callbacks, topics []string) (client net.Conn, cancel func()) {
	t.Helper()
	cl, srv := net.Pipe()
	ctx, cxl := context.WithCancel(context.Background())
	sess := newSession(srv, topics, "rave-mate", "test-uuid", 0, false, cb, nil, logbus.New(64), "test")
	go sess.run(ctx)
	return cl, func() { cl.Close(); cxl() }
}

func TestSessionAuthorizeResponse(t *testing.T) {
	cl, cancel := newTestSession(t, Callbacks{}, nil)
	defer cancel()
	col := startCollector(cl)
	if _, err := cl.Write(frame(osc.Msg(pathAuthorizeRequest))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return col.has(pathAuthorizeResponse) })
}

func TestSessionPairAndSubscribe(t *testing.T) {
	topics := []string{"/Register/Status/Deck/Playhead", "/Register/Status/Deck/Song/Title"}
	var paired bool
	var mu sync.Mutex
	cl, cancel := newTestSession(t, Callbacks{OnPaired: func(string) { mu.Lock(); paired = true; mu.Unlock() }}, topics)
	defer cancel()
	col := startCollector(cl)
	if _, err := cl.Write(frame(osc.Msg(pathPairingPair))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return col.has(pathPairingStatus) })
	for _, tp := range topics {
		waitFor(t, func() bool { return col.has(tp) })
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return paired })
}

func TestSessionPingEcho(t *testing.T) {
	var pinged bool
	var mu sync.Mutex
	cl, cancel := newTestSession(t, Callbacks{OnPing: func() { mu.Lock(); pinged = true; mu.Unlock() }}, nil)
	defer cancel()
	col := startCollector(cl)
	if _, err := cl.Write(frame(osc.Msg(pathPing))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return col.has(pathPing) })
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return pinged })
}

// ── status dispatch (Receiver.routeStatus) ────────────────────────────────────

func newTestReceiver(cb Callbacks) *Receiver { return New(Options{}, cb, logbus.New(64)) }

func TestRouteDeckChangeConverges(t *testing.T) {
	var events []DeckChange
	r := newTestReceiver(Callbacks{OnDeckChange: func(d DeckChange) { events = append(events, d) }})
	r.routeStatus(osc.Msg(pathSongTitle, osc.ArgInt(0), osc.ArgString("T")))
	r.routeStatus(osc.Msg(pathSongArtist, osc.ArgInt(0), osc.ArgString("A")))
	r.routeStatus(osc.Msg(pathSongFilepath, osc.ArgInt(0), osc.ArgString("/tmp/x.mp3")))
	if len(events) == 0 {
		t.Fatal("no deckChange")
	}
	last := events[len(events)-1]
	if last.Deck != 0 || last.Track == nil || last.Track.Title != "T" || last.Track.Artist != "A" || last.Track.FilePath != "/tmp/x.mp3" {
		t.Fatalf("last %+v", last.Track)
	}
}

func TestRoutePlayhead(t *testing.T) {
	var got []PlayheadEvent
	r := newTestReceiver(Callbacks{OnPlayhead: func(p PlayheadEvent) { got = append(got, p) }})
	r.routeStatus(osc.Msg(pathPlayhead, osc.ArgInt(0), osc.ArgFloat(10), osc.ArgFloat(180), osc.ArgFloat(125)))
	if len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
	p := got[0]
	if p.Deck != 0 || p.Raw != [3]float32{10, 180, 125} || p.PositionSeconds != 10 || p.LengthSeconds != 180 || p.BPM != 125 {
		t.Fatalf("playhead %+v", p)
	}
}

func TestRoutePlayheadCoalesces(t *testing.T) {
	var n int
	r := New(Options{PlayheadInterval: time.Hour}, Callbacks{OnPlayhead: func(PlayheadEvent) { n++ }}, logbus.New(64))
	for i := 0; i < 5; i++ {
		r.routeStatus(osc.Msg(pathPlayhead, osc.ArgInt(0), osc.ArgFloat(float32(i)), osc.ArgFloat(1), osc.ArgFloat(1)))
	}
	if n != 1 {
		t.Fatalf("expected 1 coalesced emit, got %d", n)
	}
}

func TestRouteMixer(t *testing.T) {
	var cf, up bool
	r := newTestReceiver(Callbacks{OnMixer: func(m MixerEvent) {
		if m.HasCrossfader {
			cf = true
		}
		if m.HasUpfader && m.Deck == 0 {
			up = true
		}
	}})
	r.routeStatus(osc.Msg(pathCrossfader, osc.ArgFloat(0.25)))
	r.routeStatus(osc.Msg(pathUpfader, osc.ArgInt(0), osc.ArgFloat(0.8)))
	if !cf || !up {
		t.Fatalf("cf=%v up=%v", cf, up)
	}
}

func TestRouteEjectOnInvalid(t *testing.T) {
	var events []DeckChange
	r := newTestReceiver(Callbacks{OnDeckChange: func(d DeckChange) { events = append(events, d) }})
	r.routeStatus(osc.Msg(pathSongTitle, osc.ArgInt(0), osc.ArgString("T")))
	r.routeStatus(osc.Msg(pathSongValid, osc.ArgInt(0), osc.ArgFloat(0)))
	idx := slices.IndexFunc(events, func(d DeckChange) bool { return d.Track == nil })
	if idx < 0 {
		t.Fatal("no eject event")
	}
	if events[idx].Deck != 0 {
		t.Fatalf("eject deck %d", events[idx].Deck)
	}
}

func TestRouteLoop(t *testing.T) {
	var got []LoopEvent
	r := newTestReceiver(Callbacks{OnLoop: func(l LoopEvent) { got = append(got, l) }})
	r.routeStatus(osc.Msg(pathAutoLoopOn, osc.ArgInt(1), osc.ArgFloat(1)))
	r.routeStatus(osc.Msg(pathBeatLength, osc.ArgInt(1), osc.ArgFloat(4)))
	if len(got) < 2 {
		t.Fatalf("got %d loop events", len(got))
	}
	last := got[len(got)-1]
	if last.Deck != 1 || !last.AutoLoopOn || last.BeatLength != 4 {
		t.Fatalf("loop %+v", last.Loop)
	}
}
