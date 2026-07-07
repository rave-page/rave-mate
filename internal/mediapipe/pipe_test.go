package mediapipe

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediatools"
)

// pipe_test.go - end-to-end encode child → decode child over REAL ffmpeg. Skips cleanly when
// ffmpeg (or a usable encoder) is absent, so CI without media tools stays green.

type rawSink struct {
	mu   sync.Mutex
	got  []*medialink.Frame
	want int
	done chan struct{}
	once sync.Once
}

func (s *rawSink) Write(f *medialink.Frame) error {
	s.mu.Lock()
	s.got = append(s.got, f)
	if len(s.got) >= s.want {
		s.once.Do(func() { close(s.done) })
	}
	s.mu.Unlock()
	return nil
}
func (s *rawSink) Close() error { return nil }

func TestEncodeDecodeFFmpegPipePair(t *testing.T) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg not available - skipping live pipe-pair test")
	}
	caps, _ := Probe(context.Background(), nil)
	var encName string
	var codec medialink.Codec
	switch {
	case contains(caps.Encoders, "libx264"):
		encName, codec = "libx264", medialink.CodecH264
	case contains(caps.Encoders, "mjpeg"):
		encName, codec = "mjpeg", medialink.CodecJPEG
	default:
		t.Skip("no usable video encoder in this ffmpeg build")
	}

	const w, h, n = 64, 48, 16
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	log := logbus.New(64)

	// Source: n gradient frames, then EOF.
	ch := make(chan *medialink.Frame, n)
	for i := 0; i < n; i++ {
		buf := make([]byte, w*h*4)
		for p := 0; p < len(buf); p += 4 {
			buf[p], buf[p+1], buf[p+2], buf[p+3] = byte(i*16), byte(p), 0x40, 0xFF
		}
		ch <- &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
			PTS: int64(i+1) * 33_000_000, Payload: buf}
	}
	close(ch)

	espec := medialink.EncodeSpec{Encoder: encName, Codec: codec, Width: w, Height: h, FPS: 30,
		BitrateKbps: 1000}
	enc, err := newEncoder(ctx, log, ffmpeg, espec, medialink.NewChanSource(ch))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = enc.Close() }()

	sink := &rawSink{want: 1, done: make(chan struct{})}
	dec := newDecoder(ctx, log, ffmpeg, medialink.DecodeSpec{Codec: codec, Width: w, Height: h, FPS: 30}, sink)
	// Software-only for determinism: the hw tiers are probed live on the target machines.
	dec.accels = []string{""}
	defer func() { _ = dec.Close() }()

	// Pump encoded AUs into the decoder until the encoder EOFs (source drained + child flushed).
	var encoded int
	firstKey := false
	for {
		f, err := enc.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("encoder: %v", err)
		}
		if f.Codec != codec || len(f.Payload) == 0 {
			t.Fatalf("bad encoded frame: %+v", f)
		}
		if encoded == 0 && f.Keyframe() {
			firstKey = true
		}
		encoded++
		if err := dec.Write(f); err != nil {
			t.Fatalf("decoder: %v", err)
		}
	}
	if encoded == 0 {
		t.Fatal("no encoded AUs produced")
	}
	if !firstKey {
		t.Fatal("first AU must be a keyframe (fresh encoder opens with IDR)")
	}

	select {
	case <-sink.done:
	case <-ctx.Done():
		t.Fatalf("no decoded frames (encoded=%d)", encoded)
	}
	// Give the decoder a moment to flush more, then assert shape.
	time.Sleep(300 * time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.got) == 0 {
		t.Fatal("no raw frames out")
	}
	for _, f := range sink.got {
		if f.Codec != medialink.CodecNRGBA || len(f.Payload) != w*h*4 {
			t.Fatalf("raw frame shape: codec=%d len=%d", f.Codec, len(f.Payload))
		}
	}
	if sink.got[0].PTS == 0 {
		t.Fatal("PTS not mapped through the pipe pair")
	}
	t.Logf("pipe pair: %d raw frames from %d AUs via %s", len(sink.got), encoded, encName)
}
