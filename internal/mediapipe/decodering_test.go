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
	return &decoder{log: logbus.New(64), size: size, sink: sink, free: make(chan []byte, decFreeRing)}
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
