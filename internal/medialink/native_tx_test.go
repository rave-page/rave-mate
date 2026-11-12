package medialink

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type fakeNativeTX struct {
	done   chan struct{}
	stats  NativeTXStats
	closed bool
	err    error
}

func (f *fakeNativeTX) SetRate(kbps, fps int) {}
func (f *fakeNativeTX) Stats() NativeTXStats  { return f.stats }
func (f *fakeNativeTX) Done() <-chan struct{} { return f.done }
func (f *fakeNativeTX) Err() error            { return f.err }
func (f *fakeNativeTX) Close() error          { f.closed = true; return nil }

type fakeNativeSrc struct {
	got       *NativeTXParams
	tx        NativeTX
	attachErr error
}

func (s *fakeNativeSrc) Next(context.Context) (*Frame, error) { return nil, io.EOF }
func (s *fakeNativeSrc) Close() error                         { return nil }
func (s *fakeNativeSrc) AttachNativeTX(p NativeTXParams) (NativeTX, error) {
	s.got = &p
	if s.attachErr != nil {
		return nil, s.attachErr
	}
	return s.tx, nil
}

func nativeTestRM() *RouteManager {
	return New(Options{Self: "X", Bus: &busView{&hub{}, "X"}, Secrets: fakeSecrets{key: testKey()}, Ports: []int{0}})
}

func boundUDPConn(tok uint32) *udpConn {
	u := &udpConn{enc: newDgEncoder(tok, nil, 1200)}
	u.remote.Store(net.Addr(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 5000}))
	return u
}

func TestTryAttachNativeTX(t *testing.T) {
	rm := nativeTestRM()
	salt := make([]byte, 40)
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	pa := &pendingAnswer{peer: "B", salt: salt, token: 0x1234, stream: 3, encrypt: true,
		offer: Offer{Session: "s1", Bitrate: 8000}, srcDesc: SourceDesc{FPS: 60},
		choice: &CodecChoice{Codec: CodecHEVC}}
	u := boundUDPConn(0x1234)
	ftx := &fakeNativeTX{done: make(chan struct{})}

	// (a) native-capable source → attach with correctly mirrored params.
	fs := &fakeNativeSrc{tx: ftx}
	tx, ok := rm.tryAttachNativeTX(pa, u, fs)
	if !ok || tx != ftx {
		t.Fatalf("expected native attach, ok=%v tx=%v", ok, tx)
	}
	g := fs.got
	if g.PeerAddr != "10.0.0.2:5000" || g.Token != 0x1234 || g.Stream != 3 || g.Codec != CodecHEVC {
		t.Fatalf("params not mirrored: %+v", g)
	}
	if !g.Encrypt || g.SocketFD >= 0 || g.Transport != TransportUDP || g.BitrateKbps != 8000 || g.FPS != 60 {
		t.Fatalf("params wrong: %+v", g)
	}
	if (g.SendKey == [32]byte{}) || (g.RecvKey == [32]byte{}) {
		t.Fatal("keys not derived")
	}
	if g.SendKey == g.RecvKey {
		t.Fatal("send/recv keys must differ (per-direction)")
	}

	// (b) fallbacks → Go pump.
	if _, ok := rm.tryAttachNativeTX(pa, u, &sliceSource{}); ok {
		t.Fatal("non-native source attached")
	}
	if _, ok := rm.tryAttachNativeTX(pa, nil, fs); ok {
		t.Fatal("TCP (udp nil) attached - not native yet")
	}
	paLegacy := *pa
	paLegacy.salt = nil
	if _, ok := rm.tryAttachNativeTX(&paLegacy, u, fs); ok {
		t.Fatal("legacy peer (no salt) attached")
	}
	if _, ok := rm.tryAttachNativeTX(pa, u, &fakeNativeSrc{attachErr: errors.New("boom")}); ok {
		t.Fatal("attach error should fall back to Go pump")
	}
}

func TestSuperviseNativeTXClosesOnDone(t *testing.T) {
	rm := nativeTestRM()
	st := newRouteStat("s", "B", 3, "send")
	rio := &routeIO{st: st}
	ftx := &fakeNativeTX{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rm.superviseNativeTX(ctx, cancel, ftx, rio); close(done) }()
	close(ftx.done) // native ended on its own
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise did not return on tx.Done()")
	}
	if !ftx.closed {
		t.Fatal("tx not closed on native end")
	}
}

func TestSetNativeStatsIntoRouteStat(t *testing.T) {
	st := newRouteStat("s", "B", 3, "send")
	st.setTransportImpl("native")
	st.setNativeStats(NativeTXStats{AUsSent: 42, BytesSent: 1000, Retransmits: 2, Seq: 50})
	s := st.snapshot()
	if s.Frames != 42 || s.Bytes != 1000 || s.Retransmits != 2 || s.HighestSeq != 50 || s.TransportImpl != "native" {
		t.Fatalf("native stats not reflected: %+v", s)
	}
	// The Go pump never marks enc/tx on a native route: the source's native stages + keyframes must show.
	enc := StageSnapshot{Count: 600, MeanMs: 16.7, P50Ms: 17, P99Ms: 18, MaxMs: 19}
	st.setNativeStats(NativeTXStats{AUsSent: 43, Keyframes: 5, Enc: enc, Tx: enc})
	s = st.snapshot()
	if s.Keyframes != 5 || s.Stages == nil || s.Stages.EncDone != enc || s.Stages.TxQueue != enc {
		t.Fatalf("native stages/keyframes not reflected: kf=%d stages=%+v", s.Keyframes, s.Stages)
	}
}
