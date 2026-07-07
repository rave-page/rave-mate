package medialink

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

// memRW adapts separate reader/writer to an io.ReadWriteCloser for Conn tests.
type memRW struct {
	r io.Reader
	w io.Writer
}

func (m memRW) Read(p []byte) (int, error)  { return m.r.Read(p) }
func (m memRW) Write(p []byte) (int, error) { return m.w.Write(p) }
func (m memRW) Close() error                { return nil }

func testKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i * 7)
	}
	return k
}

func frameEq(t *testing.T, a, b *Frame) {
	t.Helper()
	if a.Stream != b.Stream || a.Kind != b.Kind || a.Codec != b.Codec || a.Flags != b.Flags ||
		a.Seq != b.Seq || a.PTS != b.PTS {
		t.Fatalf("header mismatch:\n a=%+v\n b=%+v", a, b)
	}
	if !bytes.Equal(a.Payload, b.Payload) {
		t.Fatalf("payload mismatch: %d vs %d bytes", len(a.Payload), len(b.Payload))
	}
	if a.TC.String() != b.TC.String() || a.TC.Rate != b.TC.Rate {
		t.Fatalf("timecode mismatch: %v (%+v) vs %v (%+v)", a.TC, a.TC.Rate, b.TC, b.TC.Rate)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	cases := []*Frame{
		{Stream: 1, Kind: KindVideo, Codec: CodecH264, Flags: FlagKeyframe, Seq: 42, PTS: 1_000_000_000,
			TC: Timecode{H: 1, M: 2, S: 3, F: 4, Rate: FPS25}, Payload: []byte("keyframe-nal-units")},
		{Stream: 2, Kind: KindAudio, Codec: CodecPCMS24, Seq: 0, PTS: 0, Payload: bytes.Repeat([]byte{0xAB}, 4096)},
		{Stream: 0, Kind: KindMeta, Codec: CodecNone, Flags: FlagConfig, Seq: 7}, // no payload, no TC
		{Stream: 65535, Kind: KindVideo, Codec: CodecBGRA, Flags: FlagKeyframe, Seq: 1 << 20,
			PTS: -1, TC: Timecode{H: 23, M: 59, S: 59, F: 29, Rate: FPS2997}, Payload: []byte{1, 2, 3}},
	}
	for i, f := range cases {
		got, err := parseFrame(f.marshal(nil))
		if err != nil {
			t.Fatalf("case %d parse: %v", i, err)
		}
		frameEq(t, f, got)
	}
}

func TestTransportRoundTrip(t *testing.T) {
	key := testKey()
	var buf bytes.Buffer
	wc, err := NewConn(memRW{w: &buf}, key, true)
	if err != nil {
		t.Fatal(err)
	}
	frames := []*Frame{
		{Stream: 1, Kind: KindVideo, Codec: CodecH264, Flags: FlagKeyframe, Seq: 1, PTS: 100, Payload: []byte("aaa")},
		{Stream: 1, Kind: KindVideo, Codec: CodecH264, Seq: 2, PTS: 200, Payload: bytes.Repeat([]byte{9}, 10000)},
		{Stream: 2, Kind: KindAudio, Codec: CodecPCMF32, Seq: 1, PTS: 150, Payload: []byte("pcm")},
	}
	for _, f := range frames {
		if err := wc.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	rc, err := NewConn(memRW{r: bytes.NewReader(buf.Bytes())}, key, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range frames {
		got, err := rc.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		frameEq(t, want, got)
	}
	if _, err := rc.ReadFrame(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestTransportTamperRejected(t *testing.T) {
	key := testKey()
	var buf bytes.Buffer
	wc, _ := NewConn(memRW{w: &buf}, key, true)
	if err := wc.WriteFrame(&Frame{Stream: 1, Kind: KindAudio, Codec: CodecPCMS16, Seq: 1, Payload: []byte("secret")}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	b[len(b)-1] ^= 0xFF // corrupt the GCM tag
	rc, _ := NewConn(memRW{r: bytes.NewReader(b)}, key, false)
	if _, err := rc.ReadFrame(); err == nil {
		t.Fatal("expected auth failure on tampered frame")
	}
}

func TestTransportWrongRoleFails(t *testing.T) {
	key := testKey()
	var buf bytes.Buffer
	wc, _ := NewConn(memRW{w: &buf}, key, true)
	_ = wc.WriteFrame(&Frame{Stream: 1, Seq: 1, Payload: []byte("x")})
	// A reader that also claims initiator uses the same send-key for recv → wrong key → auth fail.
	rc, _ := NewConn(memRW{r: bytes.NewReader(buf.Bytes())}, key, true)
	if _, err := rc.ReadFrame(); err == nil {
		t.Fatal("expected auth failure with mismatched roles")
	}
}

// sliceSink collects frames for end-to-end tests.
type sliceSink struct{ got []*Frame }

func (s *sliceSink) Write(f *Frame) error { s.got = append(s.got, f); return nil }
func (s *sliceSink) Close() error         { return nil }

func TestPumpEndToEnd(t *testing.T) {
	key := testKey()
	var buf bytes.Buffer
	wc, _ := NewConn(memRW{w: &buf}, key, true)
	want := []*Frame{
		{Stream: 5, Kind: KindVideo, Codec: CodecBGRA, Flags: FlagKeyframe, Seq: 1, PTS: 0,
			TC: Timecode{S: 1, F: 10, Rate: FPS30}, Payload: bytes.Repeat([]byte{7}, 2048)},
		{Stream: 5, Kind: KindVideo, Codec: CodecBGRA, Seq: 2, PTS: 33_000_000, Payload: bytes.Repeat([]byte{8}, 2048)},
	}
	for _, f := range want {
		if err := wc.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	rc, _ := NewConn(memRW{r: bytes.NewReader(buf.Bytes())}, key, false)
	sink := &sliceSink{}
	if err := Pump(context.Background(), rc, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != len(want) {
		t.Fatalf("got %d frames, want %d", len(sink.got), len(want))
	}
	for i := range want {
		frameEq(t, want[i], sink.got[i])
	}
}

func TestChanSourcePump(t *testing.T) {
	ch := make(chan *Frame, 3)
	ch <- &Frame{Stream: 1, Seq: 1, Payload: []byte("a")}
	ch <- &Frame{Stream: 1, Seq: 2, Payload: []byte("b")}
	close(ch)
	sink := &sliceSink{}
	if err := Pump(context.Background(), NewChanSource(ch), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 2 {
		t.Fatalf("got %d, want 2", len(sink.got))
	}
}

func TestTimecodeFramesRoundTrip(t *testing.T) {
	for _, rate := range []Rate{FPS24, FPS25, FPS30, FPS2997, FPS50, FPS5994} {
		for _, n := range []int64{0, 1, 24, 25, 30, 59, 1798, 17982, 108000, 2_000_000} {
			tc := TimecodeFromFrames(n, rate)
			if got := tc.Frames(); got != n {
				t.Fatalf("rate %v frame %d: round-trip got %d (tc=%s)", rate, n, got, tc)
			}
		}
	}
}

func TestTimecodeBasics(t *testing.T) {
	tc := Timecode{H: 1, M: 2, S: 3, F: 4, Rate: FPS25}
	if want := int64(((1*60+2)*60+3)*25 + 4); tc.Frames() != want {
		t.Fatalf("frames=%d want %d", tc.Frames(), want)
	}
	if tc.String() != "01:02:03:04" {
		t.Fatalf("string=%q", tc.String())
	}
	if TimecodeFromFrames(0, FPS2997).String() != "00:00:00;00" {
		t.Fatal("drop-frame separator wrong")
	}
	if d := TimecodeFromFrames(25, FPS25).ToDuration(); d != time.Second {
		t.Fatalf("25 frames @25 = %v, want 1s", d)
	}
	if n := TimecodeFromDuration(time.Second, FPS25).Frames(); n != 25 {
		t.Fatalf("1s @25 = %d frames, want 25", n)
	}
}

func TestLTCRoundTrip(t *testing.T) {
	const sr = 48000
	cases := []Timecode{
		{H: 1, M: 23, S: 45, F: 12, Rate: FPS25},
		{H: 10, M: 0, S: 0, F: 20, Rate: FPS30},
		{H: 2, M: 30, S: 15, F: 15, Rate: FPS2997},
		{H: 0, M: 0, S: 59, F: 23, Rate: FPS24},
	}
	// >30 fps isn't carryable by SMPTE 12M-1 LTC - must refuse rather than emit a wrong signal.
	if EncodeLTC(Timecode{H: 1, Rate: FPS60}, sr) != nil {
		t.Fatal("EncodeLTC must return nil for 60fps (needs SMPTE 12M-2)")
	}
	for _, tc := range cases {
		one := EncodeLTC(tc, sr)
		if len(one) == 0 {
			t.Fatalf("encode %s empty", tc)
		}
		pcm := append(append([]int16{}, one...), one...) // 2 continuous frames → first fully bracketed
		got, ok := DecodeLTC(pcm, tc.Rate)
		if !ok {
			t.Fatalf("decode %s failed", tc)
		}
		if got.H != tc.H || got.M != tc.M || got.S != tc.S || got.F != tc.F || got.Rate.Drop != tc.Rate.Drop {
			t.Fatalf("LTC round-trip %s -> %s (drop got %v want %v)", tc, got, got.Rate.Drop, tc.Rate.Drop)
		}
	}
}
