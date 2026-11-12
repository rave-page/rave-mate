package medialink

import (
	"net"
	"runtime"
	"testing"
	"time"
)

const testToken = 0xCAFEF00D

// udpPair builds two udpConns (A=initiator, B=responder) over an impaired mem link. nil impairments
// = a clean link. cfgMut tweaks both configs.
func udpPair(t *testing.T, aToB, bToA *impairment, encrypt bool, cfgMut func(*udpConfig)) (a, b *udpConn, aMem, bMem *memConn) {
	t.Helper()
	if aToB == nil {
		aToB = newImpairment(1)
	}
	if bToA == nil {
		bToA = newImpairment(2)
	}
	am, bm := newMemPair(aToB, bToA)
	master := testKey()
	mk := func(pc *memConn, peer net.Addr, initiator bool) *udpConn {
		cfg := udpConfig{Token: testToken, Initiator: initiator, Master: master, Encrypt: encrypt,
			Clock: NewMonotonicClock()}
		if cfgMut != nil {
			cfgMut(&cfg)
		}
		u, err := newUDPConn(pc, peer, cfg)
		if err != nil {
			t.Fatalf("newUDPConn: %v", err)
		}
		return u
	}
	a = mk(am, bm.self, true)
	b = mk(bm, am.self, false)
	return a, b, am, bm
}

// readN reads up to n frames from u within timeout.
func readN(u *udpConn, n int, timeout time.Duration) []*Frame {
	out := make([]*Frame, 0, n)
	type res struct {
		f   *Frame
		err error
	}
	ch := make(chan res, 1)
	deadline := time.After(timeout)
	for len(out) < n {
		go func() {
			f, err := u.ReadFrame()
			ch <- res{f, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				return out
			}
			out = append(out, r.f)
		case <-deadline:
			return out
		}
	}
	return out
}

func sendFrames(u *udpConn, n, payloadLen int) []*Frame {
	sent := make([]*Frame, n)
	for i := 0; i < n; i++ {
		f := sampleFrame(uint32(i), 1, payloadLen)
		f.Flags = 0
		if i%30 == 0 {
			f.Flags = FlagKeyframe
		}
		sent[i] = f
		_ = u.WriteFrame(f)
	}
	return sent
}

// matchBySeq verifies every received frame matches the sent frame of the same Seq (the UDP
// transport delivers in COMPLETION order under loss/reorder - the router's jitter buffer reorders
// for playout, not this layer). Returns the count of distinct sent frames that arrived intact.
func matchBySeq(t *testing.T, sent, got []*Frame) int {
	t.Helper()
	bySeq := map[uint32]*Frame{}
	for _, f := range sent {
		bySeq[f.Seq] = f
	}
	seen := map[uint32]bool{}
	for _, g := range got {
		want, ok := bySeq[g.Seq]
		if !ok {
			t.Fatalf("received a frame with unknown Seq %d", g.Seq)
		}
		if !frameEqual(g, want) {
			t.Fatalf("frame Seq %d corrupt", g.Seq)
		}
		seen[g.Seq] = true
	}
	return len(seen)
}

func TestUDPConnCleanRoundTrip(t *testing.T) {
	for _, enc := range []bool{false, true} {
		a, b, _, _ := udpPair(t, nil, nil, enc, nil)
		const n = 40
		sent := sendFrames(a, n, 100)
		got := readN(b, n, 4*time.Second)
		if len(got) != n {
			t.Fatalf("enc=%v got %d/%d frames", enc, len(got), n)
		}
		for i := range got {
			if !frameEqual(got[i], sent[i]) {
				t.Fatalf("enc=%v frame %d mismatch", enc, i)
			}
		}
		a.Close()
		b.Close()
	}
}

func TestUDPConnMultiFragment(t *testing.T) {
	a, b, _, _ := udpPair(t, nil, nil, true, func(c *udpConfig) { c.BodyMax = 300 })
	const n = 20
	sent := sendFrames(a, n, 4000) // ~14 fragments each
	got := readN(b, n, 4*time.Second)
	if len(got) != n {
		t.Fatalf("got %d/%d multi-fragment frames", len(got), n)
	}
	for i := range got {
		if !frameEqual(got[i], sent[i]) {
			t.Fatalf("multi-fragment frame %d mismatch", i)
		}
	}
	a.Close()
	b.Close()
}

func TestUDPConnLossRecovery(t *testing.T) {
	aToB := newImpairment(11)
	aToB.loss = 0.02 // 2% datagram loss on the media direction
	bToA := newImpairment(12)
	a, b, _, _ := udpPair(t, aToB, bToA, true, func(c *udpConfig) { c.BodyMax = 400 })
	const n = 60
	sent := sendFrames(a, n, 3000) // multi-fragment so per-fragment NACK matters
	got := readN(b, n, 6*time.Second)
	// Fragment NACK + retransmit should recover almost everything; allow a couple of deadline drops.
	recovered := matchBySeq(t, sent, got)
	if recovered < n-2 {
		t.Fatalf("loss recovery: only %d/%d frames (NACK/retransmit under-recovered)", recovered, n)
	}
	bs := b.Stats()
	if bs.NacksSent == 0 {
		t.Fatal("receiver sent no fragment NACKs under 2% loss")
	}
	as := a.Stats()
	if as.Retransmits == 0 {
		t.Fatal("sender performed no retransmits under 2% loss")
	}
	t.Logf("loss recovery: got=%d/%d nacksSent=%d retransmits=%d lossPct=%.2f",
		len(got), n, bs.NacksSent, as.Retransmits, bs.LossPct)
	a.Close()
	b.Close()
}

func TestUDPConnReorderDup(t *testing.T) {
	aToB := newImpairment(21)
	aToB.jitter = 4 * time.Millisecond // forces reordering
	aToB.dup = 0.05                    // 5% duplication
	a, b, _, _ := udpPair(t, aToB, nil, true, func(c *udpConfig) { c.BodyMax = 500 })
	const n = 40
	sent := sendFrames(a, n, 2500)
	got := readN(b, n, 6*time.Second)
	if recovered := matchBySeq(t, sent, got); recovered != n {
		t.Fatalf("reorder/dup: only %d/%d distinct frames arrived intact", recovered, n)
	}
	if b.Stats().DupDgram == 0 {
		t.Fatal("duplicates were not detected by the replay window")
	}
	a.Close()
	b.Close()
}

// TestUDPConnNoSpuriousNacksOnReorder (item 1): multi-fragment frames at a 60fps-ish cadence over a
// REORDERING link with 0% loss must NOT trigger spurious fragment NACKs - the fragments of the
// frame currently arriving (and brief intra-frame reorder) are in flight, not lost.
func TestUDPConnNoSpuriousNacksOnReorder(t *testing.T) {
	aToB := newImpairment(71)
	aToB.jitter = 2 * time.Millisecond // intra-frame reorder; frames still complete well before the next
	a, b, _, _ := udpPair(t, aToB, nil, true, func(c *udpConfig) { c.BodyMax = 400 })
	defer a.Close()
	defer b.Close()
	const n = 40
	sent := make([]*Frame, n)
	go func() {
		for i := 0; i < n; i++ {
			f := sampleFrame(uint32(i), 1, 4000) // ~11 fragments/frame
			if i%30 == 0 {
				f.Flags = FlagKeyframe
			} else {
				f.Flags = 0
			}
			sent[i] = f
			_ = a.WriteFrame(f)
			time.Sleep(8 * time.Millisecond) // ~realistic inter-frame cadence (so a frame completes first)
		}
	}()
	got := readN(b, n, 6*time.Second)
	if recovered := matchBySeq(t, sent, got); recovered < n-1 {
		t.Fatalf("reorder: only %d/%d frames intact", recovered, n)
	}
	if ns := b.Stats().NacksSent; ns > 3 {
		t.Fatalf("spurious NACKs on a 0%%-loss reordering link: %d (want ~0)", ns)
	}
}

func TestUDPConnKeepaliveLiveness(t *testing.T) {
	// B→A fully blacked out: A receives nothing → A hits the liveness timeout and ReadFrame errors.
	aToB := newImpairment(31)
	bToA := newImpairment(32)
	bToA.loss = 1.0
	a, b, _, _ := udpPair(t, aToB, bToA, true, func(c *udpConfig) {
		c.Keepalive = 50 * time.Millisecond
		c.Liveness = 300 * time.Millisecond
	})
	defer a.Close()
	defer b.Close()
	start := time.Now()
	_, err := a.ReadFrame()
	if err != errUDPDead {
		t.Fatalf("expected errUDPDead on blackout, got %v", err)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("liveness blackout took too long: %v", d)
	}
}

// TestUDPConnRejectFloodNoLiveness proves rejected datagrams (bad token / wrong mode) never reset
// the liveness timer and cannot keep a dead route alive (security requirement). B is the only live
// conn; the "attacker" floods it with bad-token datagrams and NO valid traffic exists, so B must
// still hit the liveness timeout.
func TestUDPConnRejectFloodNoLiveness(t *testing.T) {
	am, bm := newMemPair(newImpairment(41), newImpairment(42))
	b, err := newUDPConn(bm, am.self, udpConfig{Token: testToken, Initiator: false, Master: testKey(),
		Encrypt: true, Clock: NewMonotonicClock(), Keepalive: 50 * time.Millisecond, Liveness: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	// Flood B with bad-token cleartext datagrams via the raw mem link (am delivers to B's inbox).
	bad := newDgEncoder(0xBADBAD, nil, 1200) // wrong token + clear-on-AEAD-route (two rejection reasons)
	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(5 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				dgs, _, _, _ := bad.encodeFrame(nil, sampleFrame(1, 1, 50))
				_, _ = am.WriteTo(dgs[0], nil)
			}
		}
	}()
	defer close(stop)
	_, err = b.ReadFrame()
	if err != errUDPDead {
		t.Fatalf("flood of rejected datagrams kept the route alive: got %v", err)
	}
	if b.Stats().RejectedDgram == 0 {
		t.Fatal("rejected datagrams were not counted")
	}
}

func TestUDPConnRealSocket(t *testing.T) {
	pa, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	master := testKey()
	a, err := newUDPConn(pa, pb.LocalAddr(), udpConfig{Token: testToken, Initiator: true, Master: master, Encrypt: true, Clock: NewMonotonicClock()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := newUDPConn(pb, pa.LocalAddr(), udpConfig{Token: testToken, Initiator: false, Master: master, Encrypt: true, Clock: NewMonotonicClock()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer b.Close()
	const n = 10
	sent := sendFrames(a, n, 2000)
	got := readN(b, n, 4*time.Second)
	if len(got) != n {
		t.Fatalf("real-socket: got %d/%d frames", len(got), n)
	}
	for i := range got {
		if !frameEqual(got[i], sent[i]) {
			t.Fatalf("real-socket frame %d mismatch", i)
		}
	}
}

func TestUDPConnNoGoroutineLeak(t *testing.T) {
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		a, b, _, _ := udpPair(t, nil, nil, true, nil)
		_ = a.WriteFrame(sampleFrame(1, 1, 100))
		_ = readN(b, 1, time.Second)
		a.Close()
		b.Close()
	}
	// Allow scheduler + AfterFunc timers to drain.
	time.Sleep(200 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+8 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}
