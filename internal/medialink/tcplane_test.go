package medialink

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTCClock is a manually-advanced ClockSource with a settable quality (shared-domain tests).
type fakeTCClock struct {
	ns atomic.Int64
	mu sync.Mutex
	q  ClockQuality
}

func newFakeTCClock() *fakeTCClock {
	return &fakeTCClock{q: ClockQuality{Tier: TierMonotonic, Locked: true}}
}
func (c *fakeTCClock) Now() int64 { return c.ns.Load() }
func (c *fakeTCClock) Quality() ClockQuality {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.q
}
func (c *fakeTCClock) setQuality(q ClockQuality) { c.mu.Lock(); c.q = q; c.mu.Unlock() }

// tcSourceAt returns a TCSource parked at a settable frame.
type tcSourceAt struct {
	mu      sync.Mutex
	frame   int64
	rate    Rate
	running bool
}

func (s *tcSourceAt) set(frame int64, rate Rate, running bool) {
	s.mu.Lock()
	s.frame, s.rate, s.running = frame, rate, running
	s.mu.Unlock()
}
func (s *tcSourceAt) sample() (int64, Rate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame, s.rate, s.running
}

// advertiseClock publishes a clock-capable advert for node on the hub (what RouteManager.Advertise
// emits with the P3 Caps.Clock cap).
func advertiseClock(h *hub, node string) {
	raw, _ := json.Marshal(Advert{Node: node, Caps: &Caps{Report: true, Sync: true, Clock: true}})
	h.publish(node, TopicAdvert, raw)
}

// TestTCAnnounceGolden pins the media.tc JSON + the Caps.Clock extension (additive under the §2.1
// v1 rule: P1/P2 peers ignore both - new topic + new omitempty field).
func TestTCAnnounceGolden(t *testing.T) {
	b, err := json.Marshal(TCAnnounce{Node: "A", Running: true, Rate: 30, Frame: 108000, Anchor: 1_000_000_001})
	if err != nil {
		t.Fatal(err)
	}
	const wantAnn = `{"node":"A","running":true,"rate":30,"frame":108000,"anchor_ns":1000000001}`
	if string(b) != wantAnn {
		t.Errorf("announce JSON drifted:\n got %s\nwant %s", b, wantAnn)
	}
	b, err = json.Marshal(TCAnnounce{Node: "B", Rate: 30, Drop: true, Frame: 17982, Anchor: 42})
	if err != nil {
		t.Fatal(err)
	}
	const wantDF = `{"node":"B","running":false,"rate":30,"drop":true,"frame":17982,"anchor_ns":42}`
	if string(b) != wantDF {
		t.Errorf("drop-frame announce JSON drifted:\n got %s\nwant %s", b, wantDF)
	}
	var ann TCAnnounce
	if err := json.Unmarshal([]byte(wantAnn), &ann); err != nil || ann.Frame != 108000 || !ann.Running {
		t.Fatalf("announce round-trip: %+v err=%v", ann, err)
	}

	// Caps.Clock is omitempty: a P2-shaped caps blob stays byte-identical (wire discipline).
	b, err = json.Marshal(Caps{Report: true, Sync: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"report":true,"sync":true}` {
		t.Errorf("P2 caps changed shape: %s", b)
	}
	b, err = json.Marshal(Caps{Clock: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"clock":true}` {
		t.Errorf("clock caps JSON drifted: %s", b)
	}
	// A P2-era advert (no clock key) parses with Clock=false - never a candidate.
	var c Caps
	if err := json.Unmarshal([]byte(`{"report":true,"sync":true}`), &c); err != nil || c.Clock {
		t.Fatalf("P2 caps must decode Clock=false: %+v err=%v", c, err)
	}
	// A P3 advert decodes into a P2-shaped struct (unknown "clock" ignored - Go JSON contract).
	type p2Caps struct {
		Report bool `json:"report,omitempty"`
		Sync   bool `json:"sync,omitempty"`
	}
	var old p2Caps
	if err := json.Unmarshal([]byte(`{"report":true,"sync":true,"clock":true}`), &old); err != nil || !old.Report {
		t.Fatalf("P2 peer must parse a P3 caps blob: %+v err=%v", old, err)
	}
}

// TestTCElectionAndFollow: two planes, auto election picks the lowest NodeID; the master announces
// its anchor and the slave projects TC off its own clock (receipt-anchor domain) within the frame
// math exactly.
func TestTCElectionAndFollow(t *testing.T) {
	h := &hub{}
	clkA, clkB := newFakeTCClock(), newFakeTCClock()
	pA := NewTCPlane(TCPlaneOptions{Self: "A", Bus: &busView{h, "A"}, Clock: clkA})
	pB := NewTCPlane(TCPlaneOptions{Self: "B", Bus: &busView{h, "B"}, Clock: clkB})
	pA.announceEvery, pB.announceEvery = time.Hour, time.Hour // manual AnnounceNow only

	src := &tcSourceAt{}
	src.set(100, FPS30, true)
	pA.SetLocalSource(src.sample)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pA.Start(ctx)
	defer pA.Stop()
	pB.Start(ctx)
	defer pB.Stop()

	advertiseClock(h, "A")
	advertiseClock(h, "B")

	if st := pA.Status(); st.Role != TCRoleMaster || st.Master != "A" {
		t.Fatalf("A status = %+v, want master", st)
	}
	if st := pB.Status(); st.Role != TCRoleSlave || st.Master != "A" {
		t.Fatalf("B status = %+v, want slave of A", st)
	}

	// Master announces frame 100 @30 running; B receives with its clock at 2 s.
	clkB.ns.Store(2_000_000_000)
	pA.AnnounceNow()
	tc, st := pB.Now()
	if !st.Running || st.Holdover || st.Rate != FPS30 {
		t.Fatalf("B follow status = %+v", st)
	}
	if got := tc.Frames(); got != 100 {
		t.Fatalf("B at receipt: frames = %d, want 100", got)
	}
	// +1 s on B's clock → +30 frames (receipt-anchor domain: monotonic tier).
	clkB.ns.Store(3_000_000_000)
	tc, _ = pB.Now()
	if got := tc.Frames(); got != 130 {
		t.Fatalf("B +1s: frames = %d, want 130", got)
	}
	// Stopped master: slave parks at the announced frame.
	src.set(200, FPS30, false)
	pA.AnnounceNow()
	clkB.ns.Store(9_000_000_000)
	tc, st = pB.Now()
	if got := tc.Frames(); got != 200 || st.Running {
		t.Fatalf("B parked: frames=%d running=%v, want 200/false", got, st.Running)
	}
}

// TestTCSharedDomainAnchor: a slave whose clock is disciplined (locked, non-monotonic tier) uses
// the master's anchor directly - the exact §4 slave equation TC = f(mediaclock − epoch).
func TestTCSharedDomainAnchor(t *testing.T) {
	h := &hub{}
	clkA, clkB := newFakeTCClock(), newFakeTCClock()
	clkB.setQuality(ClockQuality{Tier: TierSoftware, Locked: true})
	pA := NewTCPlane(TCPlaneOptions{Self: "A", Bus: &busView{h, "A"}, Clock: clkA})
	pB := NewTCPlane(TCPlaneOptions{Self: "B", Bus: &busView{h, "B"}, Clock: clkB})
	pA.announceEvery, pB.announceEvery = time.Hour, time.Hour
	src := &tcSourceAt{}
	src.set(100, FPS30, true)
	pA.SetLocalSource(src.sample)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pA.Start(ctx)
	defer pA.Stop()
	pB.Start(ctx)
	defer pB.Stop()
	advertiseClock(h, "A")

	clkA.ns.Store(5_000_000_000) // master anchor domain
	clkB.ns.Store(5_100_000_000) // B is disciplined: same domain, read a bit later
	pA.AnnounceNow()
	clkB.ns.Store(6_000_000_000) // 1 s after the master anchor
	tc, _ := pB.Now()
	if got := tc.Frames(); got != 130 {
		t.Fatalf("shared-domain frames = %d, want 130 (anchor=master)", got)
	}
}

// TestTCPinnedMaster: a pinned master (D6) beats the lowest-NodeID rule on every node.
func TestTCPinnedMaster(t *testing.T) {
	h := &hub{}
	pA := NewTCPlane(TCPlaneOptions{Self: "A", Bus: &busView{h, "A"}, Clock: newFakeTCClock(), Master: "B"})
	pB := NewTCPlane(TCPlaneOptions{Self: "B", Bus: &busView{h, "B"}, Clock: newFakeTCClock(), Master: "B"})
	pA.announceEvery, pB.announceEvery = time.Hour, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pA.Start(ctx)
	defer pA.Stop()
	pB.Start(ctx)
	defer pB.Stop()
	advertiseClock(h, "A")
	advertiseClock(h, "B")
	if st := pB.Status(); st.Role != TCRoleMaster {
		t.Fatalf("pinned B not master: %+v", st)
	}
	if st := pA.Status(); st.Role != TCRoleSlave || st.Master != "B" {
		t.Fatalf("A must follow pinned B: %+v", st)
	}
}

// TestTCNonClockPeerNeverCandidate: a P2 peer (caps without clock) is never elected even with the
// lowest NodeID - P3 election only considers media.clock-capable nodes.
func TestTCNonClockPeerNeverCandidate(t *testing.T) {
	h := &hub{}
	pB := NewTCPlane(TCPlaneOptions{Self: "B", Bus: &busView{h, "B"}, Clock: newFakeTCClock()})
	pB.announceEvery = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pB.Start(ctx)
	defer pB.Stop()
	raw, _ := json.Marshal(Advert{Node: "0", Caps: &Caps{Report: true, Sync: true}}) // P2 peer, id sorts first
	h.publish("0", TopicAdvert, raw)
	if st := pB.Status(); st.Role != TCRoleMaster || st.Master != "B" {
		t.Fatalf("P2 peer must not win election: %+v", st)
	}
}

// TestTCHoldoverFreewheel: master silence past staleAfter → the slave flags holdover and keeps
// projecting (freewheels) on the last anchor. No ticker on B (announceEvery=1h) so the deposition
// never races the assertions.
func TestTCHoldoverFreewheel(t *testing.T) {
	h := &hub{}
	clkA, clkB := newFakeTCClock(), newFakeTCClock()
	pA := NewTCPlane(TCPlaneOptions{Self: "A", Bus: &busView{h, "A"}, Clock: clkA})
	pB := NewTCPlane(TCPlaneOptions{Self: "B", Bus: &busView{h, "B"}, Clock: clkB})
	pA.announceEvery, pB.announceEvery = time.Hour, time.Hour
	pB.staleAfter = 20 * time.Millisecond
	src := &tcSourceAt{}
	src.set(100, FPS30, true)
	pA.SetLocalSource(src.sample)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pA.Start(ctx)
	pB.Start(ctx)
	defer pB.Stop()
	advertiseClock(h, "A")
	pA.AnnounceNow()
	pA.Stop() // master goes silent
	time.Sleep(40 * time.Millisecond)

	clkB.ns.Store(1_000_000_000)
	tc, st := pB.Now()
	if st.Role != TCRoleSlave || !st.Holdover {
		t.Fatalf("want slave in holdover, got %+v", st)
	}
	if got := tc.Frames(); got != 130 {
		t.Fatalf("freewheel frames = %d, want 130", got)
	}
}

// TestTCTakeover: with the re-election tick running, a slave deposes a stale master and takes
// over (§4 "re-election after 5 s", shortened), reporting the change via OnMaster.
func TestTCTakeover(t *testing.T) {
	h := &hub{}
	pA := NewTCPlane(TCPlaneOptions{Self: "A", Bus: &busView{h, "A"}, Clock: newFakeTCClock()})
	pA.announceEvery = time.Hour
	var masterMu sync.Mutex
	var masters []string
	pB := NewTCPlane(TCPlaneOptions{Self: "B", Bus: &busView{h, "B"}, Clock: newFakeTCClock(),
		OnMaster: func(n string) { masterMu.Lock(); masters = append(masters, n); masterMu.Unlock() }})
	pB.announceEvery, pB.staleAfter = 10*time.Millisecond, 40*time.Millisecond

	src := &tcSourceAt{}
	src.set(100, FPS30, true)
	pA.SetLocalSource(src.sample)
	bsrc := &tcSourceAt{}
	bsrc.set(0, FPS30, false)
	pB.SetLocalSource(bsrc.sample)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pA.Start(ctx)
	pB.Start(ctx)
	defer pB.Stop()
	advertiseClock(h, "A")
	pA.AnnounceNow()
	waitFor(t, time.Second, func() bool { st := pB.Status(); return st.Role == TCRoleSlave && !st.LastAt.IsZero() })

	pA.Stop() // master silent → B deposes it after staleAfter and elects itself
	waitFor(t, 2*time.Second, func() bool { return pB.Status().Role == TCRoleMaster })
	masterMu.Lock()
	defer masterMu.Unlock()
	if len(masters) == 0 || masters[len(masters)-1] != "B" {
		t.Fatalf("OnMaster history = %v, want …B", masters)
	}
}

// TestTCPeerGone: a departed peer leaves the candidate set immediately (peer-link state hook) -
// the slave re-elects without waiting out the stale window.
func TestTCPeerGone(t *testing.T) {
	h := &hub{}
	pB := NewTCPlane(TCPlaneOptions{Self: "B", Bus: &busView{h, "B"}, Clock: newFakeTCClock()})
	pB.announceEvery = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pB.Start(ctx)
	defer pB.Stop()
	advertiseClock(h, "A")
	if st := pB.Status(); st.Master != "A" {
		t.Fatalf("want master A, got %+v", st)
	}
	pB.PeerGone("A")
	if st := pB.Status(); st.Role != TCRoleMaster || st.Master != "B" {
		t.Fatalf("want self-mastership after peer gone, got %+v", st)
	}
}

// TestTCChaseCallback: a slave's chase hook fires with the projected master frame on every
// accepted running announce (the timecode.Service jam glue).
func TestTCChaseCallback(t *testing.T) {
	h := &hub{}
	clkA, clkB := newFakeTCClock(), newFakeTCClock()
	pA := NewTCPlane(TCPlaneOptions{Self: "A", Bus: &busView{h, "A"}, Clock: clkA})
	pB := NewTCPlane(TCPlaneOptions{Self: "B", Bus: &busView{h, "B"}, Clock: clkB})
	pA.announceEvery, pB.announceEvery = time.Hour, time.Hour
	src := &tcSourceAt{}
	src.set(300, FPS25, true)
	pA.SetLocalSource(src.sample)

	type chaseCall struct {
		frame int64
		rate  Rate
	}
	calls := make(chan chaseCall, 4)
	pB.SetChase(func(frame int64, rate Rate) { calls <- chaseCall{frame, rate} })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pA.Start(ctx)
	defer pA.Stop()
	pB.Start(ctx)
	defer pB.Stop()
	advertiseClock(h, "A")
	pA.AnnounceNow()
	select {
	case c := <-calls:
		if c.frame != 300 || c.rate != FPS25 {
			t.Fatalf("chase call = %+v, want frame 300 @25", c)
		}
	case <-time.After(time.Second):
		t.Fatal("chase never called")
	}
	// A stopped clock must not chase-jam the follower.
	src.set(400, FPS25, false)
	pA.AnnounceNow()
	select {
	case c := <-calls:
		t.Fatalf("chase fired for a stopped master: %+v", c)
	case <-time.After(50 * time.Millisecond):
	}
}
