//go:build linux && zignative

package medialink

// Go-receiver cgo interop (item B): the NATIVE Zig sender (rm_tx_*, ABI v2) -> a REAL Go udpConn over
// loopback. Proves the ABI fix end to end: a native sender CONTINUING the Go conn's datagram seq space
// makes the receiver rebind to the native socket and media flows (+ NACK->retransmit + clean bye); and
// the reverse (initial_seq 0 against an advanced receiver replay window) is rejected as designed.
//
// Build + run (Linux build container has Go+gcc+CGO+glibc; nothing compiles on sus):
//   cd native/zigmedia && zig build -Dtarget=x86_64-linux-gnu --release=fast
//   go test -tags zignative -run NativeSender ./internal/medialink/

import (
	"encoding/binary"
	"math/rand"
	"net"
	"rave.page/mate/internal/wirecrypto"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sockaddrIn4 renders a *net.UDPAddr as 16 sockaddr_in bytes (Linux: sin_family host-order, sin_port +
// sin_addr network-order) for rm_open_params.peer_addr.
func sockaddrIn4(a *net.UDPAddr) []byte {
	b := make([]byte, 16)
	b[0] = 2 // AF_INET low byte (host byte order on x86_64 LE)
	binary.BigEndian.PutUint16(b[2:], uint16(a.Port))
	copy(b[4:8], a.IP.To4())
	return b
}

// lossyPacketConn wraps a packetConn and drops media datagrams to exercise NACK recovery: the FIRST
// matching dropMatch (a one-shot targeted NIC drop) and/or a probabilistic lossRate on every media
// datagram (random path loss). Control datagrams are never dropped.
type lossyPacketConn struct {
	packetConn
	dropMatch func(dg []byte) bool
	dropped   atomic.Bool
	lossRate  float64 // 0 = off; else per-media-datagram drop probability
	rng       *rand.Rand
	rngMu     sync.Mutex
}

func (l *lossyPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := l.packetConn.ReadFrom(p)
		if err != nil {
			return n, addr, err
		}
		if !l.dropped.Load() && l.dropMatch != nil && l.dropMatch(p[:n]) {
			l.dropped.Store(true)
			continue // one-shot targeted drop
		}
		if l.lossRate > 0 {
			if h, _, e := parseDatagramHeader(p[:n]); e == nil && h.Typ == dgTypeMedia {
				l.rngMu.Lock()
				drop := l.rng.Float64() < l.lossRate
				l.rngMu.Unlock()
				if drop {
					continue // random media-datagram loss
				}
			}
		}
		return n, addr, err
	}
}

// realisticAU fills buf with a 4K60-shaped access unit: an IDR every 60 frames (400-900 kB, many
// fragments), else a P-frame (80-150 kB). Returns the AU slice + keyframe flag.
func realisticAU(idx int, buf []byte) ([]byte, bool) {
	keyframe := idx%60 == 0
	var n int
	if keyframe {
		n = 400_000 + (idx*5003)%500_001
	} else {
		n = 80_000 + (idx*4001)%70_001
	}
	if n > len(buf) {
		n = len(buf)
	}
	for i := 0; i < n; i++ {
		buf[i] = byte(i*131 + idx*17)
	}
	return buf[:n], keyframe
}

// realClock returns real wall-clock ns, so the udpConn's remote-rebind rate limit (compares now against
// lastRebind=0) passes immediately - needed to exercise the handshake->native rebind in a fast test.
type realClock struct{}

func (realClock) Now() int64            { return time.Now().UnixNano() }
func (realClock) Quality() ClockQuality { return ClockQuality{Tier: TierMonotonic, Locked: true} }

// TestNativeSenderToGoReceiverInterop: native sender (continuing a nonzero InitialSeq) -> real udpConn.
// The receiver binds to the native socket on the first datagram, media flows, a dropped fragment is
// recovered via NACK->retransmit, and rm_tx_close's bye ends the route with errUDPEnded (not a blackout).
func TestNativeSenderToGoReceiverInterop(t *testing.T) {
	if v := nativeABIVersion(); v != 5 {
		t.Fatalf("linked libravemedia ABI = %d, want 5 (stale lib?)", v)
	}
	if rc := nativeWireSelftest(); rc != 0 {
		t.Fatalf("rm_wire_selftest = %d (wire core mismatch)", rc)
	}
	master := testKey()
	salt := []byte("medialink-interop-route-0001")
	token := routeToken(master, salt)
	sendKey, sendPre, recvKey, recvPre, err := deriveRouteKeysRaw(master, salt, false) // native = responder
	if err != nil {
		t.Fatal(err)
	}

	raw, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	const dropFrame = uint32(3)
	lossy := &lossyPacketConn{packetConn: raw, dropMatch: func(dg []byte) bool {
		h, _, e := parseDatagramHeader(dg)
		return e == nil && h.Typ == dgTypeMedia && h.FrameSeq == dropFrame && h.FragIdx == 1
	}}
	recv, err := newUDPConn(lossy, nil, udpConfig{
		Token: token, Initiator: true, Master: master, Salt: salt, Encrypt: true,
		Clock: NewMonotonicClock(), BodyMax: 1200,
		Liveness: 2 * time.Second, StartupGrace: 15 * time.Second, Keepalive: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()

	const initialSeq = uint64(100000) // continues the (simulated) Go handshake conn's datagram seq space
	ns, err := openCgoNativeSender(sockaddrIn4(raw.LocalAddr().(*net.UDPAddr)), token, true,
		sendKey, sendPre, recvKey, recvPre, byte(CodecHEVC), 1, 1200, initialSeq, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.close()

	// Multi-fragment AUs (3000 B -> 3 datagrams) so the dropped fragment needs a per-fragment NACK.
	const nAUs = 20
	au := make([]byte, 3000)
	for i := range au {
		au[i] = byte(i)
	}
	for i := 0; i < nAUs; i++ {
		ns.submitAU(au, int64(i)*16_666_667, i == 0)
		time.Sleep(16 * time.Millisecond)
	}

	got := readN(recv, nAUs, 5*time.Second)
	if len(got) < nAUs-1 {
		t.Fatalf("media did not flow native->Go: got %d/%d frames", len(got), nAUs)
	}
	// The first datagram's seq (initialSeq) was strictly-newest at the empty-window receiver, so it bound
	// the remote to the native socket (first bind) and media decoded - that IS the rebind-on-first-datagram.
	seen3 := false
	for _, f := range got {
		if f.Seq == dropFrame {
			seen3 = true
		}
	}
	if !seen3 {
		t.Fatal("the NACK-dropped frame was not recovered (no retransmit delivery)")
	}
	if rs := recv.Stats(); rs.NacksSent == 0 {
		t.Fatal("receiver sent no fragment NACK for the dropped fragment")
	}
	// Allow the retransmit to be counted, then assert the native sender retransmitted.
	var retx uint64
	for i := 0; i < 50; i++ {
		if _, retx, _, _ = ns.stats(); retx > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if retx == 0 {
		t.Fatal("native sender performed no retransmit on the receiver's NACK")
	}

	// Clean bye: closing the native sender sends a bye -> receiver ends with errUDPEnded, promptly.
	ns.close()
	start := time.Now()
	var endErr error
	for time.Since(start) < 2*time.Second {
		if _, e := recv.ReadFrame(); e != nil {
			endErr = e
			break
		}
	}
	if endErr != errUDPEnded {
		t.Fatalf("native sender bye did not end the route cleanly: err=%v (want errUDPEnded)", endErr)
	}
}

// newAdvancedReceiver builds a udpConn (realClock) whose replay window has been advanced to highSeq by
// one authenticated handshake ping from a throwaway socket (also binds its remote to that socket), so a
// later native sender's low-seq datagrams land below the window. Returns the receiver + the handshake
// socket (close it with the receiver).
func newAdvancedReceiver(t *testing.T, master, salt []byte, token uint32, highSeq uint64) (*udpConn, *net.UDPConn) {
	t.Helper()
	raw, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	recv, err := newUDPConn(raw, nil, udpConfig{
		Token: token, Initiator: true, Master: master, Salt: salt, Encrypt: true,
		Clock: realClock{}, BodyMax: 1200,
		Liveness: 10 * time.Second, StartupGrace: 20 * time.Second, Keepalive: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	hs, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	// The sender direction (responder) seals s2c; the initiator receiver opens s2c. A single ping at
	// seq=highSeq sets the receiver's replay-window highest to highSeq (first datagram -> started).
	hcrypto, err := newDgCrypto(master, salt, false)
	if err != nil {
		t.Fatal(err)
	}
	enc := newDgEncoder(token, hcrypto, 1200)
	enc.seq = highSeq
	var ts [8]byte
	putBeU64(ts[:], uint64(realClock{}.Now()))
	ping := enc.control(dgTypePing, ts[:])
	if _, err := hs.WriteToUDP(ping, raw.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // let the receiver accept the ping + advance the window
	return recv, hs
}

// TestNativeSenderInitialSeqRebindVsAdvancedWindow: over a receiver whose replay window is already far
// advanced, a native sender at initial_seq=0 is REJECTED (too-old, no rebind, no media - the exact bug
// the ABI gap caused), while a native sender CONTINUING the seq (initial_seq = highSeq+1) rebinds the
// remote off the handshake socket and media flows. Same window; only InitialSeq differs.
func TestNativeSenderInitialSeqRebindVsAdvancedWindow(t *testing.T) {
	master := testKey()
	salt := []byte("medialink-interop-route-adv")
	token := routeToken(master, salt)
	sendKey, sendPre, recvKey, recvPre, err := deriveRouteKeysRaw(master, salt, false)
	if err != nil {
		t.Fatal(err)
	}
	const highSeq = uint64(1) << 20 // far above the 4096 replay window; seq 0 can never catch up in-test

	au := make([]byte, 1200)
	for i := range au {
		au[i] = byte(i)
	}

	// ── reject: initial_seq = 0 against the advanced window → no media. ──
	t.Run("stale-zero-seq-rejected", func(t *testing.T) {
		recv, hs := newAdvancedReceiver(t, master, salt, token, highSeq)
		defer recv.Close()
		defer hs.Close()
		raw := recv.pc.(*net.UDPConn)
		ns, err := openCgoNativeSender(sockaddrIn4(raw.LocalAddr().(*net.UDPAddr)), token, true,
			sendKey, sendPre, recvKey, recvPre, byte(CodecHEVC), 1, 1200, 0 /*stale*/, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer ns.close()
		for i := 0; i < 30; i++ { // ~480ms of frames; seq stays far below the window floor
			ns.submitAU(au, int64(i)*16_666_667, i == 0)
			time.Sleep(16 * time.Millisecond)
		}
		if got := readN(recv, 1, 400*time.Millisecond); len(got) != 0 {
			t.Fatalf("stale seq-0 sender delivered %d frames - replay window did NOT reject it", len(got))
		}
		if rb := recv.Stats().Rebinds; rb != 0 {
			t.Fatalf("stale seq-0 sender rebound the remote (%d rebinds) - should be rejected", rb)
		}
	})

	// ── accept: initial_seq = highSeq+1 continues → rebind off the handshake socket + media flows. ──
	t.Run("continued-seq-accepted", func(t *testing.T) {
		recv, hs := newAdvancedReceiver(t, master, salt, token, highSeq)
		defer recv.Close()
		defer hs.Close()
		raw := recv.pc.(*net.UDPConn)
		ns, err := openCgoNativeSender(sockaddrIn4(raw.LocalAddr().(*net.UDPAddr)), token, true,
			sendKey, sendPre, recvKey, recvPre, byte(CodecHEVC), 1, 1200, highSeq+1 /*continue*/, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer ns.close()
		for i := 0; i < 30; i++ {
			ns.submitAU(au, int64(i)*16_666_667, i == 0)
			time.Sleep(16 * time.Millisecond)
		}
		got := readN(recv, 10, 3*time.Second)
		if len(got) < 5 {
			t.Fatalf("continued-seq sender delivered only %d frames - rebind/accept failed", len(got))
		}
		if rb := recv.Stats().Rebinds; rb < 1 {
			t.Fatalf("continued-seq sender did not rebind the remote off the handshake socket (rebinds=%d)", rb)
		}
	})
}

// TestNativeSenderRealistic4K60 drives the native sender at 4K60-realistic sizes (IDR 400-900 kB,
// P-frames 80-150 kB, ~45 Mbps, multi-fragment every frame) into a REAL Go udpConn, under 0.1 %
// datagram loss. Asserts: nearly every frame COMPLETES (the pacing + per-fragment clock-refresh fix -
// the bare target bitrate starved multi-fragment keyframes), the dropped fragments are recovered via
// NACK -> retransmit (receiver NacksSent > 0, sender retransmits > 0), and OWD stays sane (the PTS
// rebase keeps frames in the receiver's clock domain, not classified permanently late).
func TestNativeSenderRealistic4K60(t *testing.T) {
	master := testKey()
	salt := []byte("medialink-interop-4k60")
	token := routeToken(master, salt)
	sendKey, sendPre, recvKey, recvPre, err := deriveRouteKeysRaw(master, salt, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	lossy := &lossyPacketConn{packetConn: raw, lossRate: 0.001, rng: rand.New(rand.NewSource(1))}
	recv, err := newUDPConn(lossy, nil, udpConfig{
		Token: token, Initiator: true, Master: master, Salt: salt, Encrypt: true,
		Clock: NewMonotonicClock(), BodyMax: 1200,
		Liveness: 3 * time.Second, StartupGrace: 15 * time.Second, Keepalive: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()

	// 45 Mbps budget, passed as the BARE target bytes/s (exactly what the live native path passes today,
	// kbps*1000/8). Pre-fix the native pacer ran at this bare rate and starved keyframes; the sender now
	// applies the Go gain+floor. A large PTS epoch base mimics the capture clock, exercising the rebase.
	const bitrateKbps = 45000
	rate := float64(bitrateKbps) * 1000 / 8
	const ptsBase = int64(1_700_000_000_000_000_000) // ~a wall-epoch ns, far from the receiver's clock
	ns, err := openCgoNativeSender(sockaddrIn4(raw.LocalAddr().(*net.UDPAddr)), token, true,
		sendKey, sendPre, recvKey, recvPre, byte(CodecHEVC), 1, 1200, 100000, rate)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.close()

	var delivered int64
	done := make(chan struct{})
	go func() {
		for {
			f, e := recv.ReadFrame()
			if e != nil {
				close(done)
				return
			}
			_ = f
			atomic.AddInt64(&delivered, 1)
		}
	}()

	const nFrames = 180 // ~3 s at 60 fps; 3 IDRs
	buf := make([]byte, 1<<20)
	for i := 0; i < nFrames; i++ {
		au, kf := realisticAU(i, buf)
		ns.submitAU(au, ptsBase+int64(i)*16_666_667, kf)
		time.Sleep(16 * time.Millisecond)
	}
	// Drain: allow the last frames + any NACK recovery to land.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) && atomic.LoadInt64(&delivered) < nFrames {
		time.Sleep(20 * time.Millisecond)
	}
	got := atomic.LoadInt64(&delivered)
	if got < nFrames*95/100 {
		t.Fatalf("multi-fragment frames did not complete: %d/%d (pacing starvation?)", got, nFrames)
	}
	rs := recv.Stats()
	if rs.NacksSent == 0 {
		t.Fatalf("no NACKs sent under 0.1%% loss (loss not exercised?)")
	}
	_, retx, _, _ := ns.stats()
	if retx == 0 {
		t.Fatalf("native sender retransmitted nothing despite %d NACKs sent", rs.NacksSent)
	}
	if ow := rs.OwdNs; ow < 0 || ow > int64(100*time.Millisecond) {
		t.Fatalf("OWD insane (%d ns) - PTS not rebased into the receiver's clock domain", ow)
	}
	t.Logf("realistic 4K60: delivered=%d/%d nacksSent=%d retrans=%d owd=%dms lossPct=%.3f",
		got, nFrames, rs.NacksSent, retx, rs.OwdNs/int64(time.Millisecond), rs.LossPct)
}

// TestNativeSender4K60TenSeconds (item B acceptance): native sender -> REAL Go udpConn, 4K60-realistic
// AUs (IDR 400-900 kB every 60, P 80-150 kB, ~50 Mbps) for 10 s under 0.1 % random datagram loss.
// The sender is opened with media_clock_ns = the receiver's clock (same host), so PTS land in the
// receiver's domain like the router's media clock. Asserts: EVERY frame completes (NACK recovery),
// the SENDER saw NACKs and retransmitted, no TX queue drops, transit (arrival - PTS) small and
// positive, and a receiver jitter buffer stays near 1-2 frames with ~0 misses.
func TestNativeSender4K60TenSeconds(t *testing.T) {
	master := testKey()
	salt := []byte("medialink-interop-4k60-10s")
	token := routeToken(master, salt)
	sendKey, sendPre, recvKey, recvPre, err := deriveRouteKeysRaw(master, salt, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.SetReadBuffer(8 << 20); err != nil {
		t.Logf("SetReadBuffer: %v", err)
	}
	clk := NewMonotonicClock()
	lossy := &lossyPacketConn{packetConn: raw, lossRate: 0.001, rng: rand.New(rand.NewSource(42))}
	recv, err := newUDPConn(lossy, nil, udpConfig{
		Token: token, Initiator: true, Master: master, Salt: salt, Encrypt: true,
		Clock: clk, BodyMax: 1200,
		Liveness: 3 * time.Second, StartupGrace: 15 * time.Second, Keepalive: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()

	const bitrateKbps = 45000
	const ptsBase = int64(1_700_000_000_000_000_000) // capture-clock epoch, far from the receiver's clock
	ns, err := openCgoNativeSenderClock(sockaddrIn4(raw.LocalAddr().(*net.UDPAddr)), token, true,
		sendKey, sendPre, recvKey, recvPre, byte(CodecHEVC), 1, 1200, 100000, float64(bitrateKbps)*1000/8, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer ns.close()

	const nFrames = 600 // 10 s at 60 fps
	jb := newJitterBufferPreset(60, false, PlayoutLowLatency)
	var mu sync.Mutex
	seen := make(map[uint32]bool, nFrames)
	var transits []int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			f, e := recv.ReadFrame()
			if e != nil {
				return
			}
			now := clk.Now()
			mu.Lock()
			seen[f.Seq] = true
			transits = append(transits, now-f.PTS)
			mu.Unlock()
			jb.push(f, now)
		}
	}()
	// Release runner (runJitter's loop shape on the real clock).
	stopRel := make(chan struct{})
	relDone := make(chan struct{})
	go func() {
		defer close(relDone)
		for {
			if g, wake := jb.pop(clk.Now()); g != nil {
				continue
			} else {
				d := time.Millisecond
				if wake != 0 {
					if w := time.Duration(wake - clk.Now()); w > 0 && w < d {
						d = w
					}
				}
				select {
				case <-stopRel:
					return
				case <-jb.notify:
				case <-time.After(d):
				}
			}
		}
	}()

	buf := make([]byte, 1<<20)
	start := time.Now()
	for i := 0; i < nFrames; i++ {
		au, kf := realisticAU(i, buf)
		ns.submitAU(au, ptsBase+int64(i)*16_666_667, kf)
		if d := time.Until(start.Add(time.Duration(i+1) * 16_666_667)); d > 0 {
			time.Sleep(d) // absolute schedule: no cumulative sleep drift
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n >= nFrames {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let the playout drain
	close(stopRel)
	<-relDone
	mu.Lock()
	got := len(seen)
	tr := append([]int64(nil), transits...)
	js := jb.statsAt(clk.Now())
	mu.Unlock()
	rs := recv.Stats()
	ss := ns.fullStats()
	sort.Slice(tr, func(i, j int) bool { return tr[i] < tr[j] })
	p50, p99, mx := tr[len(tr)/2], tr[len(tr)*99/100], tr[len(tr)-1]
	t.Logf("4K60 10s: delivered=%d/%d sender{sent=%d dropped=%d nacksRecv=%d retx=%d bytes=%.1fMB} recv{nacksSent=%d reasmDrops=%d owd=%.1fms lossPct=%.3f} transit p50/p99/max=%.1f/%.1f/%.1fms jb{playout=%.1fms misses=%d lateRate=%.3f drops=%d}",
		got, nFrames, ss.AUsSent, ss.AUsDropped, ss.NacksRecv, ss.Retransmits, float64(ss.BytesSent)/1e6,
		rs.NacksSent, rs.ReasmDrops, float64(rs.OwdNs)/1e6, rs.LossPct,
		float64(p50)/1e6, float64(p99)/1e6, float64(mx)/1e6, js.PlayoutMs, js.Late, js.LateRate, js.PolicyDrops)
	if got != nFrames {
		t.Fatalf("not every frame completed: %d/%d (reasmDrops=%d)", got, nFrames, rs.ReasmDrops)
	}
	if ss.AUsDropped != 0 {
		t.Fatalf("sender TX queue dropped %d AUs", ss.AUsDropped)
	}
	if rs.NacksSent == 0 || ss.NacksRecv == 0 || ss.Retransmits == 0 {
		t.Fatalf("NACK path not exercised end to end: recv nacksSent=%d sender nacksRecv=%d retx=%d",
			rs.NacksSent, ss.NacksRecv, ss.Retransmits)
	}
	if tr[0] < 0 || p50 > int64(30*time.Millisecond) {
		t.Fatalf("PTS not in the receiver clock domain: transit min=%d p50=%d ns", tr[0], p50)
	}
	if js.PolicyDrops != 0 || js.Late > 10 || js.PlayoutMs > 50 {
		t.Fatalf("receiver playout unhealthy: %+v", js)
	}
}

// TestNativeSenderMetaRateAndSync (ABI v4): the REAL Go receiver's control-plane meta reaches the
// native sender on a native route: a MetaRate (governor trim) becomes the polled rate hint within
// ~1 s, and a MetaSync ping is answered with a SyncPong in the media-clock domain (pairwise clock
// estimate + RTT on the receiver). Media keeps flowing meanwhile.
func TestNativeSenderMetaRateAndSync(t *testing.T) {
	master := testKey()
	salt := []byte("medialink-interop-meta-v4")
	token := routeToken(master, salt)
	sendKey, sendPre, recvKey, recvPre, err := deriveRouteKeysRaw(master, salt, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	clk := NewMonotonicClock()
	recv, err := newUDPConn(raw, nil, udpConfig{Token: token, Initiator: true, Master: master, Salt: salt,
		Encrypt: true, Clock: clk, BodyMax: 1200, Liveness: 3 * time.Second, StartupGrace: 15 * time.Second,
		Keepalive: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()
	ns, err := openCgoNativeSenderClock(sockaddrIn4(raw.LocalAddr().(*net.UDPAddr)), token, true,
		sendKey, sendPre, recvKey, recvPre, byte(CodecHEVC), 1, 1200, 100000, 0, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer ns.close()

	pongs := make(chan SyncPong, 4)
	go func() {
		for {
			f, e := recv.ReadFrame()
			if e != nil {
				return
			}
			if p, err := DecodeSyncPong(f); err == nil {
				pongs <- p
			}
		}
	}()
	au := make([]byte, 3000)
	stop := make(chan struct{})
	defer close(stop)
	go func() { // media flowing so the receiver binds the native socket
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			ns.submitAU(au, int64(i)*16_666_667, i == 0)
			time.Sleep(16 * time.Millisecond)
		}
	}()
	time.Sleep(300 * time.Millisecond)

	send := func(payload any, seq uint32) {
		f, err := MetaFrame(payload, clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		f.Seq = seq
		if err := recv.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	want := RateHint{Type: MetaRate, MaxBitrateKbps: 18000, MaxFPS: 30, MaxHeight: 1080, QualityTarget: 70}
	t0 := time.Now()
	send(want, 1)
	var got RateHint
	var gen uint64
	for time.Since(t0) < time.Second {
		if got, gen = ns.rateHint(); gen > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if gen == 0 || got != want {
		t.Fatalf("rate hint not surfaced within 1 s: gen=%d got=%+v want %+v", gen, got, want)
	}
	t.Logf("rate hint surfaced after %v", time.Since(t0))

	t1 := clk.Now()
	send(SyncPing{Type: MetaSync, ID: 7, T1: t1}, 2)
	select {
	case p := <-pongs:
		t4 := clk.Now()
		rtt := (t4 - p.T1) - (p.T3 - p.T2)
		off := ((p.T2 - p.T1) + (p.T3 - t4)) / 2
		if p.ID != 7 || p.T1 != t1 || rtt < 0 || rtt > int64(50*time.Millisecond) || off < -int64(5*time.Millisecond) || off > int64(5*time.Millisecond) {
			t.Fatalf("sync pong insane: %+v rtt=%d off=%d", p, rtt, off)
		}
		t.Logf("sync pong: rtt=%dus offset=%dus", rtt/1000, off/1000)
	case <-time.After(time.Second):
		t.Fatal("no SyncPong from the native sender")
	}
}

// TestNativeSenderTCPToGoConn: the native TCP sender (legacy Conn wire) -> a REAL Go medialink.Conn
// (NewConn, the dialing/receiving end). 4K60-realistic AUs for 10 s: every frame decodes in order with
// its payload intact; the receiver's MetaRate + MetaSync over the same connection reach the native
// sender (rate hint, SyncPong).
func TestNativeSenderTCPToGoConn(t *testing.T) {
	master := testKey()
	salt := []byte("medialink-interop-tcp-native")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cli, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	f, err := srv.(*net.TCPConn).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a, _ := wirecrypto.HkdfSha256(master, salt, []byte("medialink c2s v1"), 32)
	b, _ := wirecrypto.HkdfSha256(master, salt, []byte("medialink s2c v1"), 32)
	var sk, rk [32]byte
	copy(sk[:], b)
	copy(rk[:], a)
	clk := NewMonotonicClock()
	ns, err := openCgoNativeSenderTCP(int(f.Fd()), true, sk, rk, byte(CodecHEVC), 1, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	recv, err := NewConn(cli, master, salt, true) // receiving end dialed = initiator
	if err != nil {
		t.Fatal(err)
	}
	const n = 600
	type got struct {
		frames, bad int
		pong        *SyncPong
	}
	res := make(chan got, 1)
	go func() {
		var g got
		for g.frames < n || g.pong == nil {
			fr, err := recv.ReadFrame()
			if err != nil {
				break
			}
			if p, err := DecodeSyncPong(fr); err == nil {
				pp := p
				g.pong = &pp
				continue
			}
			if fr.Kind != KindVideo || fr.Seq != uint32(g.frames) || len(fr.Payload) < 5 || fr.Payload[0] != 0xA5 ||
				binary.BigEndian.Uint32(fr.Payload[1:5]) != uint32(g.frames) {
				g.bad++
			}
			g.frames++
		}
		res <- g
	}()
	buf := make([]byte, 1<<20)
	start := time.Now()
	for i := 0; i < n; i++ {
		au, kf := realisticAU(i, buf)
		au[0] = 0xA5
		binary.BigEndian.PutUint32(au[1:5], uint32(i))
		ns.submitAU(au, int64(i)*16_666_667, kf)
		if i == 30 {
			mf, _ := MetaFrame(RateHint{Type: MetaRate, MaxBitrateKbps: 15000, MaxFPS: 30}, clk.Now())
			if err := recv.WriteFrame(mf); err != nil {
				t.Fatal(err)
			}
			sp, _ := MetaFrame(SyncPing{Type: MetaSync, ID: 9, T1: clk.Now()}, clk.Now())
			if err := recv.WriteFrame(sp); err != nil {
				t.Fatal(err)
			}
		}
		if d := time.Until(start.Add(time.Duration(i+1) * 16_666_667)); d > 0 {
			time.Sleep(d)
		}
	}
	var g got
	select {
	case g = <-res:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver did not finish")
	}
	h, gen := ns.rateHint()
	ss := ns.fullStats()
	t.Logf("tcp native: frames=%d/%d bad=%d sent=%d dropped=%d bytes=%.1fMB hint(gen=%d br=%d fps=%d) pong=%v",
		g.frames, n, g.bad, ss.AUsSent, ss.AUsDropped, float64(ss.BytesSent)/1e6, gen, h.MaxBitrateKbps, h.MaxFPS, g.pong != nil)
	if g.frames != n || g.bad != 0 || ss.AUsDropped != 0 {
		t.Fatalf("frames %d/%d bad %d dropped %d", g.frames, n, g.bad, ss.AUsDropped)
	}
	if gen == 0 || h.MaxBitrateKbps != 15000 || h.MaxFPS != 30 {
		t.Fatalf("MetaRate over TCP not surfaced: gen=%d %+v", gen, h)
	}
	if g.pong == nil || g.pong.ID != 9 {
		t.Fatal("no SyncPong over TCP")
	}
	ns.close()
}
