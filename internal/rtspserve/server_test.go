package rtspserve

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
)

// fakeSource publishes params + a keyframe AU stream into the feed.
func fakeSource(t *testing.T) func(ctx context.Context, cfg config.RTSPServeFeature, fd *feed) {
	return func(ctx context.Context, cfg config.RTSPServeFeature, fd *feed) {
		fd.setParams(nal(nalSPS, 0x42, 0x00, 0x1F), nal(nalPPS, 0xCE))
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		i := byte(0)
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				fd.publish(accessUnit{nals: [][]byte{nal(nalSliceIDR, 0x80, i)}, key: true})
				i++
			}
		}
	}
}

// readResp reads one RTSP response (status line, headers, body per Content-Length).
func readResp(t *testing.T, br *bufio.Reader) (status string, header map[string]string, body string) {
	t.Helper()
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	status = strings.TrimSpace(line)
	header = map[string]string{}
	for {
		h, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		h = strings.TrimRight(h, "\r\n")
		if h == "" {
			break
		}
		if i := strings.IndexByte(h, ':'); i > 0 {
			header[strings.ToLower(strings.TrimSpace(h[:i]))] = strings.TrimSpace(h[i+1:])
		}
	}
	if n, _ := strconv.Atoi(header["content-length"]); n > 0 {
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			t.Fatalf("read body: %v", err)
		}
		body = string(b)
	}
	return status, header, body
}

func TestRTSPHandshakeAndStream(t *testing.T) {
	srv := New(logbus.New(64), func() config.RTSPServeFeature {
		return config.RTSPServeFeature{Enabled: true, Source: "fake", ListenAddr: "127.0.0.1:0", Path: "/live", FPS: 30}
	})
	srv.runSource = fakeSource(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	addr := srv.Status().Addr

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	u := "rtsp://" + addr + "/live"

	send := func(format string, a ...any) {
		t.Helper()
		if _, err := fmt.Fprintf(c, format, a...); err != nil {
			t.Fatal(err)
		}
	}

	send("OPTIONS %s RTSP/1.0\r\nCSeq: 1\r\n\r\n", u)
	status, hdr, _ := readResp(t, br)
	if !strings.Contains(status, "200") || !strings.Contains(hdr["public"], "DESCRIBE") {
		t.Fatalf("OPTIONS: %s %v", status, hdr)
	}

	send("DESCRIBE %s RTSP/1.0\r\nCSeq: 2\r\nAccept: application/sdp\r\n\r\n", u)
	status, _, body := readResp(t, br)
	if !strings.Contains(status, "200") {
		t.Fatalf("DESCRIBE: %s", status)
	}
	if !strings.Contains(body, "H264/90000") || !strings.Contains(body, "sprop-parameter-sets=") {
		t.Fatalf("SDP missing H264 attrs:\n%s", body)
	}

	send("SETUP %s/streamid=0 RTSP/1.0\r\nCSeq: 3\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n\r\n", u)
	status, hdr, _ = readResp(t, br)
	if !strings.Contains(status, "200") || hdr["session"] == "" {
		t.Fatalf("SETUP: %s %v", status, hdr)
	}
	sess := strings.SplitN(hdr["session"], ";", 2)[0]

	send("PLAY %s RTSP/1.0\r\nCSeq: 4\r\nSession: %s\r\n\r\n", u, sess)
	status, _, _ = readResp(t, br)
	if !strings.Contains(status, "200") {
		t.Fatalf("PLAY: %s", status)
	}

	// First interleaved RTP packet: keyframe join → payload must be the SPS.
	pkt := readInterleaved(t, br)
	if pt := pkt[1] & 0x7F; pt != 96 {
		t.Fatalf("payload type %d", pt)
	}
	if payload := pkt[12:]; payload[0]&0x1F != nalSPS {
		t.Fatalf("first NAL type %d, want SPS", payload[0]&0x1F)
	}
	// Next: PPS, then the IDR with the marker on the AU's last packet.
	if p := readInterleaved(t, br)[12:]; p[0]&0x1F != nalPPS {
		t.Fatalf("second NAL not PPS")
	}
	pkt = readInterleaved(t, br)
	if pkt[1]&0x80 == 0 {
		t.Fatal("marker not set on AU end")
	}
	if p := pkt[12:]; p[0]&0x1F != nalSliceIDR {
		t.Fatalf("third NAL type %d, want IDR", p[0]&0x1F)
	}

	if got := srv.Status(); !got.Running || got.Clients != 1 || got.AUs == 0 {
		t.Fatalf("status %+v", got)
	}

	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for srv.Status().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.Status().Running {
		t.Fatal("still running after cancel")
	}
}

// readInterleaved reads one $-framed packet and returns the RTP packet bytes.
func readInterleaved(t *testing.T, br *bufio.Reader) []byte {
	t.Helper()
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		t.Fatalf("interleave header: %v", err)
	}
	if hdr[0] != '$' || hdr[1] != 0 {
		t.Fatalf("bad interleave header % x", hdr)
	}
	n := binary.BigEndian.Uint16(hdr[2:])
	pkt := make([]byte, n)
	if _, err := io.ReadFull(br, pkt); err != nil {
		t.Fatalf("interleave payload: %v", err)
	}
	return pkt
}

func TestRTSPRejectsUDPTransportAndWrongPath(t *testing.T) {
	srv := New(logbus.New(64), func() config.RTSPServeFeature {
		return config.RTSPServeFeature{Enabled: true, Source: "fake", ListenAddr: "127.0.0.1:0"}
	})
	srv.runSource = fakeSource(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", srv.Status().Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	u := "rtsp://" + srv.Status().Addr

	if _, err := fmt.Fprintf(c, "SETUP %s/live/streamid=0 RTSP/1.0\r\nCSeq: 1\r\nTransport: RTP/AVP;unicast;client_port=8000-8001\r\n\r\n", u); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := readResp(t, br); !strings.Contains(status, "461") {
		t.Fatalf("UDP SETUP: %s", status)
	}
	if _, err := fmt.Fprintf(c, "DESCRIBE %s/nope RTSP/1.0\r\nCSeq: 2\r\n\r\n", u); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := readResp(t, br); !strings.Contains(status, "404") {
		t.Fatalf("wrong path DESCRIBE: %s", status)
	}
}

func TestStartFailsWithoutSource(t *testing.T) {
	srv := New(logbus.New(16), func() config.RTSPServeFeature { return config.RTSPServeFeature{Enabled: true} })
	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("Start must fail with no source configured")
	}
}

// TestEndToEndWithFFmpeg drives the real chain - ffmpeg lavfi testsrc → NAL/AU → RTSP
// handshake → decodable RTP. Skips when ffmpeg isn't installed.
func TestEndToEndWithFFmpeg(t *testing.T) {
	if _, ok := mediatools.Resolve("ffmpeg"); !ok {
		t.Skip("ffmpeg not available")
	}
	srv := New(logbus.New(64), func() config.RTSPServeFeature {
		return config.RTSPServeFeature{
			Enabled: true, Source: "testsrc=size=320x240:rate=30", InputFormat: "lavfi",
			ListenAddr: "127.0.0.1:0", FPS: 30, BitrateKbps: 500,
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", srv.Status().Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(c)
	u := "rtsp://" + srv.Status().Addr + "/live"

	if _, err := fmt.Fprintf(c, "DESCRIBE %s RTSP/1.0\r\nCSeq: 1\r\n\r\n", u); err != nil {
		t.Fatal(err)
	}
	status, _, body := readResp(t, br)
	if !strings.Contains(status, "200") || !strings.Contains(body, "sprop-parameter-sets=") {
		t.Fatalf("DESCRIBE: %s\n%s\nlastErr=%s", status, body, srv.Status().LastErr)
	}
	if _, err := fmt.Fprintf(c, "SETUP %s/streamid=0 RTSP/1.0\r\nCSeq: 2\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n\r\n", u); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := readResp(t, br); !strings.Contains(status, "200") {
		t.Fatalf("SETUP: %s", status)
	}
	if _, err := fmt.Fprintf(c, "PLAY %s RTSP/1.0\r\nCSeq: 3\r\n\r\n", u); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := readResp(t, br); !strings.Contains(status, "200") {
		t.Fatalf("PLAY: %s", status)
	}
	// Stream joins at a keyframe: SPS first, and a marker within the first AU's packets.
	pkt := readInterleaved(t, br)
	if p := pkt[12:]; p[0]&0x1F != nalSPS {
		t.Fatalf("first NAL type %d, want SPS", p[0]&0x1F)
	}
	sawMarker := false
	for i := 0; i < 200 && !sawMarker; i++ {
		sawMarker = readInterleaved(t, br)[1]&0x80 != 0
	}
	if !sawMarker {
		t.Fatal("no AU marker within 200 packets")
	}
}

func TestFeedLaggardResyncsOnKey(t *testing.T) {
	fd := newFeed()
	sub := fd.subscribe()
	defer fd.unsubscribe(sub)
	// A fresh subscriber waits for its first keyframe.
	fd.publish(accessUnit{nals: [][]byte{nal(nalSliceNonIDR, 0x80)}, key: false})
	if len(sub.ch) != 0 {
		t.Fatal("delivered before first keyframe")
	}
	// Overfill: capacity 32 → later AUs drop, subscriber flips back to waitKey.
	fd.publish(accessUnit{nals: [][]byte{nal(nalSliceIDR, 0x80)}, key: true})
	for i := 0; i < 40; i++ {
		fd.publish(accessUnit{nals: [][]byte{nal(nalSliceNonIDR, 0x80)}, key: false})
	}
	drained := 0
	for len(sub.ch) > 0 {
		<-sub.ch
		drained++
	}
	if drained != 32 {
		t.Fatalf("drained %d, want 32", drained)
	}
	fd.publish(accessUnit{nals: [][]byte{nal(nalSliceNonIDR, 0x80)}, key: false})
	if len(sub.ch) != 0 {
		t.Fatal("non-key AU delivered while waiting for key")
	}
	fd.publish(accessUnit{nals: [][]byte{nal(nalSliceIDR, 0x80)}, key: true})
	if len(sub.ch) != 1 {
		t.Fatal("key AU not delivered")
	}
}
