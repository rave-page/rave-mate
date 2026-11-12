package medialink

import (
	"net"
	"testing"
)

// bench_test.go measures the four transport/crypto combinations at 4K60-like encoded frame sizes
// (brief Unit 6): per-frame round-trip latency + allocations. "P-frame" ≈ 80 KB (a 40 Mbps/60 fps
// inter frame), "keyframe" ≈ 500 KB (a periodic intra frame). Run:
//
//	GOWORK=off go test ./internal/medialink -run '^$' -bench 'BenchmarkTransport' -benchmem
//
// The honest comparison the owner asked for: is the AEAD cost on the LAN worth dropping, and does
// UDP beat TCP for this traffic. Numbers are per round-trip over loopback (localhost), so they
// isolate codec + crypto + framing cost, not real NIC/switch latency.

func benchFrame(size int, keyframe bool) *Frame {
	p := make([]byte, size)
	for i := range p {
		p[i] = byte(i)
	}
	f := &Frame{Stream: 1, Kind: KindVideo, Codec: CodecHEVC, Seq: 0, PTS: 1, Payload: p}
	if keyframe {
		f.Flags = FlagKeyframe
	}
	return f
}

// tcpConnPair returns two *Conn over a real loopback TCP socket (encrypt chooses AEAD vs plaintext).
func tcpConnPair(b *testing.B, encrypt bool) (a, bb *Conn) {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = ln.Close() })
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()
	dc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		b.Fatal(r.err)
	}
	setNoDelay(dc)
	setNoDelay(r.c)
	master := testKey()
	if encrypt {
		a, _ = NewConn(dc, master, nil, true)
		bb, _ = NewConn(r.c, master, nil, false)
	} else {
		a, bb = NewPlainConn(dc), NewPlainConn(r.c)
	}
	b.Cleanup(func() { _ = a.Close(); _ = bb.Close() })
	return a, bb
}

func udpConnPairBench(b *testing.B, encrypt bool) (a, bb *udpConn) {
	b.Helper()
	pa, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		b.Fatal(err)
	}
	pb, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		b.Fatal(err)
	}
	master := testKey()
	a, err = newUDPConn(pa, pb.LocalAddr(), udpConfig{Token: testToken, Initiator: true, Master: master, Encrypt: encrypt, Clock: NewMonotonicClock()})
	if err != nil {
		b.Fatal(err)
	}
	bb, err = newUDPConn(pb, pa.LocalAddr(), udpConfig{Token: testToken, Initiator: false, Master: master, Encrypt: encrypt, Clock: NewMonotonicClock()})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = a.Close(); _ = bb.Close() })
	return a, bb
}

func benchRoundTrip(b *testing.B, write func(*Frame) error, read func() (*Frame, error), f *Frame) {
	b.SetBytes(int64(len(f.Payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := write(f); err != nil {
			b.Fatal(err)
		}
		if _, err := read(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTransport(b *testing.B) {
	sizes := []struct {
		name     string
		size     int
		keyframe bool
	}{
		{"Pframe80K", 80 << 10, false},
		{"Keyframe500K", 500 << 10, true},
	}
	for _, sz := range sizes {
		f := benchFrame(sz.size, sz.keyframe)
		b.Run(sz.name+"/TCP_AEAD", func(b *testing.B) {
			a, bb := tcpConnPair(b, true)
			benchRoundTrip(b, a.WriteFrame, bb.ReadFrame, f)
		})
		b.Run(sz.name+"/TCP_clear", func(b *testing.B) {
			a, bb := tcpConnPair(b, false)
			benchRoundTrip(b, a.WriteFrame, bb.ReadFrame, f)
		})
		b.Run(sz.name+"/UDP_clear", func(b *testing.B) {
			a, bb := udpConnPairBench(b, false)
			benchRoundTrip(b, a.WriteFrame, bb.ReadFrame, f)
		})
		b.Run(sz.name+"/UDP_AEAD", func(b *testing.B) {
			a, bb := udpConnPairBench(b, true)
			benchRoundTrip(b, a.WriteFrame, bb.ReadFrame, f)
		})
	}
}

// BenchmarkDatagramCodec isolates the per-datagram encode+decode cost (no socket): the pure framing
// + AEAD overhead on the hot path.
func BenchmarkDatagramCodec(b *testing.B) {
	for _, enc := range []bool{false, true} {
		name := "clear"
		var crypto, rcrypto *dgCrypto
		if enc {
			name = "AEAD"
			crypto, rcrypto = newCryptoPairB(b)
		}
		b.Run(name, func(b *testing.B) {
			e := newDgEncoder(testToken, crypto, 1200)
			f := benchFrame(80<<10, false)
			r := newReassembler(0, 0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f.Seq = uint32(i)
				dgs, _, _, _ := e.encodeFrame(nil, f)
				for _, dg := range dgs {
					h, off, _ := parseDatagramHeader(dg)
					chunk, _ := decodeBody(h, dg[off:], testToken, rcrypto, dg[:off])
					r.accept(h, chunk, 0)
				}
			}
		})
	}
}

func newCryptoPairB(b *testing.B) (send, recv *dgCrypto) {
	master := testKey()
	s, err := newDgCrypto(master, nil, true)
	if err != nil {
		b.Fatal(err)
	}
	r, err := newDgCrypto(master, nil, false)
	if err != nil {
		b.Fatal(err)
	}
	return s, r
}
