package mediapipe

import (
	"bytes"
	"io"
	"testing"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

// decodering_test.go - the receive-side framing must stay byte-exact now that reads land straight
// in the recycled frame buffer (no staging slice, no residual memmove).

// copySink records a COPY of every frame (a real sink must not retain Payload - the buffer is
// recycled the moment Write returns, which is what these tests exercise).
type copySink struct {
	got  [][]byte
	live [][]byte // the payload slices as handed over (identity check for recycling)
	err  error
}

func (s *copySink) Write(f *medialink.Frame) error {
	s.got = append(s.got, append([]byte(nil), f.Payload...))
	s.live = append(s.live, f.Payload)
	return s.err
}

func (s *copySink) Close() error { return nil }

// chunkReader hands out the stream in fixed-size chunks that deliberately do NOT align with frame
// boundaries, then EOFs.
type chunkReader struct {
	data []byte
	step int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.step
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func testDecoder(size int, sink medialink.Sink) *decoder {
	return &decoder{log: logbus.New(64), size: size, sink: sink,
		free: make(chan []byte, decFreeRingDepth(size))}
}

// TestDecFreeRingIsBoundedInBytes: the recycle ring's cap must be BYTES, not frames. Fixed at depth
// 4 it parked 33 MB at 1080p and 132 MB per receive route at 4K - a bound whose stated number
// ("32 MB at 1080p") was only ever true at one geometry, while the native decode path parks 4 MiB.
func TestDecFreeRingIsBoundedInBytes(t *testing.T) {
	cases := []struct {
		name      string
		w, h      int
		wantDepth int
	}{
		{"640x480", 640, 480, decFreeRingMax},
		{"1280x720", 1280, 720, decFreeRingMax},
		{"1920x1080", 1920, 1080, 4}, // 8.3 MB × 4 = 33 MB … see the byte assertion below
		{"3840x2160", 3840, 2160, 1},
		{"7680x4320", 7680, 4320, 1}, // 8K: one frame already exceeds the cap - never 0
	}
	for _, c := range cases {
		size := c.w * c.h * 4
		got := decFreeRingDepth(size)
		if got != c.wantDepth {
			t.Errorf("%s: depth %d, want %d", c.name, got, c.wantDepth)
		}
		if got < 1 { // 0 would make getBuf allocate a full frame per decoded frame
			t.Fatalf("%s: depth %d", c.name, got)
		}
		if parked := got * size; parked > decFreeRingBytes && got > 1 {
			t.Errorf("%s: depth %d parks %d MB, over the %d MB cap",
				c.name, got, parked>>20, decFreeRingBytes>>20)
		}
		// Non-vacuity, stated as a number: what the OLD fixed-depth policy parked at this geometry.
		old := decFreeRingMax * size
		if c.w >= 3840 && old <= decFreeRingBytes {
			t.Fatalf("%s: the old fixed depth parked %d MB, which is inside the cap - this test proves nothing here",
				c.name, old>>20)
		}
		if c.w == 3840 && old>>20 < 120 {
			t.Fatalf("test premise wrong: 4 × 4K frames should be ~132 MB, got %d MB", old>>20)
		}
	}
	// A zero/garbage geometry must not divide by zero or return 0.
	if got := decFreeRingDepth(0); got != decFreeRingMax {
		t.Fatalf("zero frame size gives depth %d", got)
	}
}

// TestPumpFramesByteExactAcrossFragments: 5 frames of distinct bytes arrive in chunks that straddle
// every frame boundary; each frame must come out byte-identical and in order.
func TestPumpFramesByteExactAcrossFragments(t *testing.T) {
	const size, frames = 97, 5 // deliberately prime-ish so no chunk size aligns
	var stream []byte
	want := make([][]byte, frames)
	for i := 0; i < frames; i++ {
		f := make([]byte, size)
		for j := range f {
			f[j] = byte(i*31 + j)
		}
		want[i] = f
		stream = append(stream, f...)
	}
	for _, step := range []int{1, 7, size - 1, size, size + 1, 2*size + 3, len(stream)} {
		sink := &copySink{}
		d := testDecoder(size, sink)
		d.pumpFrames(&chunkReader{data: append([]byte(nil), stream...), step: step})
		if len(sink.got) != frames {
			t.Fatalf("step %d: got %d frames, want %d", step, len(sink.got), frames)
		}
		for i := range want {
			if !bytes.Equal(sink.got[i], want[i]) {
				t.Fatalf("step %d frame %d: payload mismatch\n got %v\nwant %v", step, i, sink.got[i], want[i])
			}
		}
	}
}

// TestPumpFramesPartialTailDropped: a truncated final frame is never emitted (a short frame would
// reach the Spout sink as a wrong-size payload and be counted as a drop there).
func TestPumpFramesPartialTailDropped(t *testing.T) {
	const size = 64
	stream := make([]byte, size+size/2)
	for i := range stream {
		stream[i] = byte(i)
	}
	sink := &copySink{}
	d := testDecoder(size, sink)
	d.pumpFrames(&chunkReader{data: stream, step: 13})
	if len(sink.got) != 1 {
		t.Fatalf("got %d frames from 1.5 frames of data, want 1", len(sink.got))
	}
	if !bytes.Equal(sink.got[0], stream[:size]) {
		t.Fatal("first frame is not byte-exact")
	}
}

// TestPumpFramesRecyclesBuffers: the ring is reused, so the process does not allocate a full frame
// per decoded frame (the receiving PC's hot path). Buffer identity repeats after the first cycle.
func TestPumpFramesRecyclesBuffers(t *testing.T) {
	const size, frames = 32, 8
	stream := make([]byte, size*frames)
	for i := range stream {
		stream[i] = byte(i)
	}
	sink := &copySink{}
	d := testDecoder(size, sink)
	d.pumpFrames(&chunkReader{data: stream, step: size})
	if len(sink.live) != frames {
		t.Fatalf("got %d frames, want %d", len(sink.live), frames)
	}
	first := &sink.live[0][0]
	reused := 0
	for _, p := range sink.live[1:] {
		if &p[0] == first {
			reused++
		}
	}
	if reused != frames-1 {
		t.Fatalf("frame buffer reused %d of %d times - the ring is not recycling", reused, frames-1)
	}
	// Exactly one buffer circulates: it is recycled then immediately taken back for the next
	// frame, so the ring is empty at rest and never grows past its cap.
	if len(d.free) != 0 {
		t.Fatalf("free ring holds %d buffers at rest, want 0 (the one buffer is in flight)", len(d.free))
	}
}

// TestPumpFramesPTSOrder: PTS/TC pop in output order (no B-frames), unchanged by the new framing.
func TestPumpFramesPTSOrder(t *testing.T) {
	const size = 16
	sink := &copySink{}
	d := testDecoder(size, sink)
	d.ptsq = []ptsEntry{{pts: 11}, {pts: 22}}
	stream := make([]byte, size*2)
	var pts []int64
	inner := &ptsCaptureSink{sink: sink, pts: &pts}
	d.sink = inner
	d.pumpFrames(&chunkReader{data: stream, step: 5})
	if len(pts) != 2 || pts[0] != 11 || pts[1] != 22 {
		t.Fatalf("PTS sequence = %v, want [11 22]", pts)
	}
	if len(d.ptsq) != 0 {
		t.Fatalf("ptsq still holds %d entries", len(d.ptsq))
	}
}

type ptsCaptureSink struct {
	sink medialink.Sink
	pts  *[]int64
}

func (s *ptsCaptureSink) Write(f *medialink.Frame) error {
	*s.pts = append(*s.pts, f.PTS)
	return s.sink.Write(f)
}

func (s *ptsCaptureSink) Close() error { return s.sink.Close() }
