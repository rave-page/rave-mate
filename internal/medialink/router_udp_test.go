package medialink

import (
	"context"
	"net"
	"testing"
	"time"
)

// udpRouteManagers builds two loopback RouteManagers with UDP enabled/disabled per side.
func udpRouteManagers(t *testing.T, aUDP, bUDP bool) (rmA, rmB *RouteManager, h *hub) {
	t.Helper()
	h = &hub{}
	secrets := fakeSecrets{key: testKey()}
	rmB = New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1", UDP: bUDP})
	rmA = New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1", UDP: aUDP})
	return rmA, rmB, h
}

// TestRouteLoopbackUDP drives a full negotiation that selects the UDP datagram profile end-to-end:
// A offers [udp,tcp], B answers udp, A's reachability handshake succeeds, B streams over UDP.
func TestRouteLoopbackUDP(t *testing.T) {
	rmA, rmB, _ := udpRouteManagers(t, true, true)
	const n = 48
	want := make([]*Frame, n)
	for i := range want {
		want[i] = &Frame{Kind: KindAudio, Codec: CodecPCMS16, PTS: int64(i+1) * 5_000_000,
			Payload: []byte{byte(i), 0x5A, byte(i * 2)}}
	}
	src := &sliceSource{frames: want}
	rmB.RegisterSource(SourceDesc{ID: "mic", Name: "Mic", Kind: KindAudio, Codec: CodecPCMS16, Sample: 48000, Channels: 2},
		func(context.Context, Offer) (Source, error) { return src, nil })
	sink := newCollectSink(n)
	rmA.RegisterSink(SinkDesc{ID: "spk", Name: "Speakers", Kind: KindAudio},
		func(context.Context, Answer) (Sink, error) { return sink, nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rmB.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rmB.Stop()
	if err := rmA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rmA.Stop()
	if rmB.mux == nil {
		t.Fatal("answerer did not bind a UDP mux")
	}

	rmB.Advertise()
	waitFor(t, time.Second, func() bool { _, ok := rmA.RemoteAdverts()["B"]; return ok })
	if _, err := rmA.Offer("B", "mic", "spk", CodecPCMS16); err != nil {
		t.Fatal(err)
	}

	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		sink.mu.Lock()
		got := len(sink.got)
		sink.mu.Unlock()
		t.Fatalf("timeout: got %d/%d frames over UDP", got, n)
	}

	// The route must have been UDP on both ends (telemetry), not a silent TCP fallback.
	waitFor(t, 2*time.Second, func() bool {
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" && s.Transport == TransportUDP && s.UDP != nil {
				return true
			}
		}
		return false
	})
	gotUDPSend := false
	for _, s := range rmB.Stats() {
		if s.Direction == "send" && s.Transport == TransportUDP {
			gotUDPSend = true
		}
	}
	if !gotUDPSend {
		t.Fatal("sender route was not UDP")
	}
}

// TestRouteUDPOldPeerCompat: a UDP-capable node talking to a node that does not speak UDP (either
// direction) must fall back to TCP transparently (wire compatibility is mandatory).
func TestRouteUDPOldPeerCompat(t *testing.T) {
	for _, tc := range []struct {
		name       string
		aUDP, bUDP bool
	}{
		{"newOffererOldAnswerer", true, false},
		{"oldOffererNewAnswerer", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rmA, rmB, _ := udpRouteManagers(t, tc.aUDP, tc.bUDP)
			const n = 16
			want := make([]*Frame, n)
			for i := range want {
				want[i] = &Frame{Kind: KindAudio, Codec: CodecPCMS16, PTS: int64(i+1) * 5_000_000, Payload: []byte{byte(i)}}
			}
			rmB.RegisterSource(SourceDesc{ID: "mic", Kind: KindAudio, Codec: CodecPCMS16},
				func(context.Context, Offer) (Source, error) { return &sliceSource{frames: want}, nil })
			sink := newCollectSink(n)
			rmA.RegisterSink(SinkDesc{ID: "spk", Kind: KindAudio},
				func(context.Context, Answer) (Sink, error) { return sink, nil })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := rmB.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer rmB.Stop()
			if err := rmA.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer rmA.Stop()
			rmB.Advertise()
			waitFor(t, time.Second, func() bool { _, ok := rmA.RemoteAdverts()["B"]; return ok })
			if _, err := rmA.Offer("B", "mic", "spk", CodecPCMS16); err != nil {
				t.Fatal(err)
			}
			select {
			case <-sink.done:
			case <-time.After(5 * time.Second):
				t.Fatal("timeout: compat route did not stream")
			}
			for _, s := range rmA.Stats() {
				if s.Direction == "recv" && s.Transport == TransportUDP {
					t.Fatal("route used UDP with a non-UDP peer (should fall back to TCP)")
				}
			}
		})
	}
}

// TestUDPReachabilityFailsToDeadPeer proves the dialer's reachability handshake fails (→ TCP
// fallback) when the UDP peer never answers - the firewall case the fallback exists for.
func TestUDPReachabilityFailsToDeadPeer(t *testing.T) {
	// A UDP addr with nothing listening (bind + immediately close to free the port).
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	dead := probe.LocalAddr().(*net.UDPAddr)
	_ = probe.Close()

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	u, err := newUDPConn(pc, dead, udpConfig{Token: testToken, Initiator: true, Master: testKey(),
		Encrypt: true, Clock: NewMonotonicClock(), Keepalive: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if u.waitReachable(400 * time.Millisecond) {
		t.Fatal("waitReachable reported a dead UDP peer as reachable")
	}
}
