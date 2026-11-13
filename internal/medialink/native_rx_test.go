package medialink

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNativeRX is a scripted native receiver.
type fakeNativeRX struct {
	mu    sync.Mutex
	stats NativeRXStats
	ok    bool
	err   error
	sent  [][]byte
	meta  chan NativeRXMeta
	nSend atomic.Int32
}

func newFakeNativeRX() *fakeNativeRX {
	return &fakeNativeRX{ok: true, meta: make(chan NativeRXMeta, 8)}
}

func (f *fakeNativeRX) SendMeta(js []byte) error {
	f.mu.Lock()
	f.sent = append(f.sent, append([]byte(nil), js...))
	f.mu.Unlock()
	f.nSend.Add(1)
	return nil
}
func (f *fakeNativeRX) Stats() (NativeRXStats, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats, f.ok
}
func (f *fakeNativeRX) Meta() <-chan NativeRXMeta { return f.meta }
func (f *fakeNativeRX) Err() error                { f.mu.Lock(); defer f.mu.Unlock(); return f.err }
func (f *fakeNativeRX) set(fn func(s *NativeRXStats)) {
	f.mu.Lock()
	fn(&f.stats)
	f.mu.Unlock()
}
func (f *fakeNativeRX) sentWith(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.sent {
		if strings.Contains(string(b), sub) {
			return true
		}
	}
	return false
}

// fakeNativeSink is a decode sink that can take the network source.
type fakeNativeSink struct {
	Sink
	attachErr error
	mu        sync.Mutex
	got       *NativeRXParams
	nrx       *fakeNativeRX
	writes    atomic.Int32
}

func (s *fakeNativeSink) Write(f *Frame) error { s.writes.Add(1); return s.Sink.Write(f) }
func (s *fakeNativeSink) AttachNativeRX(p NativeRXParams) (NativeRX, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := p
	s.got = &cp
	if s.attachErr != nil {
		return nil, s.attachErr
	}
	s.nrx = newFakeNativeRX()
	closeParkedSocket(p.Socket) // the fake owns it on success
	return s.nrx, nil
}
func (s *fakeNativeSink) params() *NativeRXParams { s.mu.Lock(); defer s.mu.Unlock(); return s.got }

// realUDPConn builds a dialer-style udpConn over a real loopback socket with a bound remote.
func realUDPConn(t *testing.T, salt []byte) *udpConn {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	sec := testKey()
	u, err := newUDPConn(pc, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, udpConfig{Token: routeToken(sec, salt),
		Initiator: true, Master: sec, Salt: salt, Encrypt: true, Clock: NewMonotonicClock(), Keepalive: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	return u
}

func testSalt() []byte {
	salt := make([]byte, 40)
	for i := range salt {
		salt[i] = byte(i + 3)
	}
	return salt
}

func TestTryAttachNativeRXFallbacks(t *testing.T) {
	rm := nativeTestRM()
	salt := testSalt()
	u := realUDPConn(t, salt)
	plain := &collectSink{}
	if _, ok := rm.tryAttachNativeRX(plain, u, "B", salt, true, 1, 60); ok {
		t.Fatal("a sink without NativeRXSink must keep the Go path")
	}
	fs := &fakeNativeSink{Sink: newCollectSink(1)}
	if _, ok := rm.tryAttachNativeRX(fs, nil, "B", salt, true, 1, 60); ok {
		t.Fatal("TCP route (no udpConn) must keep the Go path")
	}
	if _, ok := rm.tryAttachNativeRX(fs, u, "B", nil, true, 1, 60); ok {
		t.Fatal("legacy peer (no per-route salt) must keep the Go path")
	}
	t.Setenv("RAVE_MATE_NATIVE_RX", "0")
	if _, ok := rm.tryAttachNativeRX(fs, u, "B", salt, true, 1, 60); ok || fs.params() != nil {
		t.Fatal("RAVE_MATE_NATIVE_RX=0 must force the Go path")
	}
	t.Setenv("RAVE_MATE_NATIVE_RX", "")
	rm.recvImpl = func() string { return "go" }
	if _, ok := rm.tryAttachNativeRX(fs, u, "B", salt, true, 1, 60); ok || fs.params() != nil {
		t.Fatal(`mediaLink.recvImpl="go" must force the Go path`)
	}
	rm.recvImpl = nil
	if runtime.GOOS != "windows" {
		if _, ok := rm.tryAttachNativeRX(fs, u, "B", salt, true, 1, 60); ok {
			t.Fatal("native receive is Windows-only")
		}
		return
	}
	bad := &fakeNativeSink{Sink: newCollectSink(1), attachErr: errors.New("child said no")}
	if _, ok := rm.tryAttachNativeRX(bad, u, "B", salt, true, 1, 60); ok {
		t.Fatal("attach failure must keep the Go path")
	}
	if u.isDead() {
		t.Fatal("a failed attach must leave the Go udpConn running")
	}
	// A dual-stack (AF_INET6) socket cannot be handed to the sockaddr_in receiver: Go path, conn intact.
	pc6, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6unspecified})
	if err != nil {
		t.Skip("no IPv6 stack")
	}
	sec := testKey()
	u6, err := newUDPConn(pc6, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, udpConfig{Token: routeToken(sec, salt),
		Initiator: true, Master: sec, Salt: salt, Encrypt: true, Clock: NewMonotonicClock(), Keepalive: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer u6.Close()
	fs6 := &fakeNativeSink{Sink: newCollectSink(1)}
	if _, ok := rm.tryAttachNativeRX(fs6, u6, "B", salt, true, 1, 60); ok || fs6.params() != nil || u6.isDead() {
		t.Fatal("an AF_INET6 route socket must keep the Go path untouched")
	}
}

func TestTryAttachNativeRXHandsOver(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native receive child is Windows-only")
	}
	rm := nativeTestRM()
	rm.playout = func() string { return "smooth" }
	salt := testSalt()
	u := realUDPConn(t, salt)
	before := u.sendSeq()
	fs := &fakeNativeSink{Sink: newCollectSink(1)}
	nrx, ok := rm.tryAttachNativeRX(fs, u, "B", salt, true, 7, 60)
	if !ok || nrx == nil {
		t.Fatal("UDP route + native sink + keys must hand over")
	}
	p := fs.params()
	sk, skp, rk, rkp, _ := deriveRouteKeysRaw(testKey(), salt, true)
	if p.Socket == 0 || p.Token != u.token || p.SendKey != sk || p.SendPrefix != skp || p.OpenKey != rk || p.OpenPrefix != rkp {
		t.Fatalf("hand-off material: %+v", p)
	}
	if p.Peer != "127.0.0.1:9" || p.Stream != 7 || p.FPS != 60 || p.Preset != "smooth" || p.BodyMax != dgDefaultPayload || !p.Encrypt {
		t.Fatalf("route params: %+v", p)
	}
	if p.InitialSeq < before+nativeRXSeqJump {
		t.Fatalf("native control seq %d must start >= %d (Go conn seq %d + jump): one nonce space", p.InitialSeq, before+nativeRXSeqJump, before)
	}
	if !u.isDead() {
		t.Fatal("the Go udpConn must stop after the hand-off (its receive loop would steal datagrams)")
	}
}

func TestSuperviseNativeRXLifecycle(t *testing.T) {
	rm := nativeTestRM()
	run := func(prep func(f *fakeNativeRX, c *nativeRXConn)) error {
		f := newFakeNativeRX()
		c := newNativeRXConn(f)
		st := newRouteStat("s", "B", 1, "recv")
		st.setNativeRX("balanced")
		prep(f, c)
		done := make(chan error, 1)
		go func() { done <- rm.superviseNativeRX(context.Background(), c, f, st, "B") }()
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			_ = c.Close()
			return errors.New("supervisor did not end")
		}
	}
	if err := run(func(f *fakeNativeRX, _ *nativeRXConn) {
		f.set(func(s *NativeRXStats) { s.Datagrams, s.Bye = 10, true })
	}); !errors.Is(err, errUDPEnded) {
		t.Fatalf("bye: %v", err)
	}
	if err := run(func(f *fakeNativeRX, _ *nativeRXConn) {
		f.set(func(s *NativeRXStats) { s.Datagrams, s.SinceRx = 10, udpDefLiveness+time.Second })
	}); !errors.Is(err, errUDPDead) {
		t.Fatalf("blackout: %v", err)
	}
	if err := run(func(f *fakeNativeRX, _ *nativeRXConn) {
		f.set(func(s *NativeRXStats) { s.Datagrams, s.SinceRx = 0, -1 }) // cold start: grace, not dead
		f.mu.Lock()
		f.err = errors.New("pinned")
		f.mu.Unlock()
	}); err == nil || err.Error() != "pinned" {
		t.Fatalf("native failure: %v", err)
	}
	if err := run(func(_ *fakeNativeRX, c *nativeRXConn) { _ = c.Close() }); !errors.Is(err, errConnClosed) {
		t.Fatalf("route replaced/stopped: %v", err)
	}
}

func TestNativeRXTelemetryAndMeta(t *testing.T) {
	rm := nativeTestRM()
	f := newFakeNativeRX()
	c := newNativeRXConn(f)
	st := newRouteStat("s", "B", 1, "recv")
	st.setEncrypted(true)
	st.setTransportImpl("native")
	st.setNativeRX("balanced")
	rio := &routeIO{conn: c, st: st}
	// Receiver->sender meta goes through the native counter and never inflates the media counters.
	if err := rio.writeMeta(RateHint{Type: MetaRate, Stream: 0, MaxBitrateKbps: 9000}, 1); err != nil {
		t.Fatal(err)
	}
	if !f.sentWith(`"t":"rate"`) || !f.sentWith(`"br":9000`) {
		t.Fatalf("MetaRate not sent natively: %q", f.sent)
	}
	if err := c.WriteFrame(&Frame{Kind: KindVideo, Stream: 1}); err == nil {
		t.Fatal("a native rx conn carries no outbound media")
	}
	f.set(func(s *NativeRXStats) {
		*s = NativeRXStats{Datagrams: 900, Delivered: 300, Published: 299, MediaBytes: 3_000_000, Keyframes: 5,
			NacksSent: 4, NackRecovered: 3, LossPct: 0.5, OwdNs: 2e6, RTTns: 1e6, PlayoutMs: 170, TargetMs: 150, MaxMs: 170,
			PubCount: 298, PubP50Ms: 17, PubP99Ms: 18, PubMaxMs: 19, RelP99Ms: 18, SinceRx: 10 * time.Millisecond}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rm.superviseNativeRX(ctx, c, f, st, "B") }()
	f.meta <- NativeRXMeta{JSON: []byte(`{"t":"report","stream":1,"packets":5}`)}
	waitFor(t, 2*time.Second, func() bool {
		s := st.snapshot()
		return s.Frames == 300 && s.ReportsRecv == 1
	})
	s := st.snapshot()
	if s.TransportImpl != "native" || s.NativeRX == nil || s.UDP == nil || s.JB == nil || s.Stages == nil {
		t.Fatalf("native snapshot incomplete: %+v", s)
	}
	if s.UDP.LossPct != 0.5 || s.UDP.RTTns != 1e6 || s.JB.PlayoutMs != 170 || s.JB.Preset != "balanced" ||
		s.Stages.Publish.P99Ms != 18 || s.Stages.Publish.Count != 298 || s.Bytes != 3_000_000 || s.Keyframes != 5 ||
		s.NACKsSent != 4 || s.Recovered != 3 {
		t.Fatalf("native snapshot values: udp=%+v jb=%+v pub=%+v %+v", s.UDP, s.JB, s.Stages.Publish, s)
	}
	if s.ReportsSent != 0 {
		t.Fatalf("a MetaRate is not a report: reportsSent=%d", s.ReportsSent)
	}
	// The congestion controller reads the native transport numbers.
	if u := (nativeUDPStats{f}).Stats(); u.LossPct != 0.5 || u.OwdNs != 2e6 {
		t.Fatalf("congestion input: %+v", u)
	}
}

// TestNativeRXCountersSurviveReattach: a re-attached receiver restarts its counters at 0; the route's
// cumulative numbers (and so the rate window) stay monotonic.
func TestNativeRXCountersSurviveReattach(t *testing.T) {
	st := newRouteStat("s", "B", 1, "recv")
	st.setNativeRX("balanced")
	st.setNativeRXStats(NativeRXStats{Delivered: 500, MediaBytes: 5e6})
	st.setNativeRXStats(NativeRXStats{Delivered: 20, MediaBytes: 2e5}) // new child incarnation
	st.setNativeRXStats(NativeRXStats{Delivered: 30, MediaBytes: 3e5})
	if s := st.snapshot(); s.Frames != 530 || s.Bytes != 5_300_000 || s.RateBps < 0 || s.RateBps > 1e12 {
		t.Fatalf("counters across re-attach: frames=%d bytes=%d rate=%f", s.Frames, s.Bytes, s.RateBps)
	}
}
