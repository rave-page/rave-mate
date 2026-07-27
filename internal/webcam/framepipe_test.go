package webcam

import (
	"bytes"
	"io"
	"testing"
	"testing/iotest"
)

func TestFrameSize(t *testing.T) {
	if n, err := frameSize(1920, 1080); err != nil || n != 1920*1080*4 {
		t.Fatalf("got %d, %v", n, err)
	}
	for _, bad := range [][2]int{{0, 720}, {1280, 0}, {-1, 1}, {20000, 20000}} {
		if _, err := frameSize(bad[0], bad[1]); err == nil {
			t.Fatalf("expected error for %v", bad)
		}
	}
}

// readFrames adapts framePipe to the old test shape: fresh buffers, recycling counted so the
// ownership contract (every non-emitted buffer comes back) can be asserted.
func readFrames(r io.Reader, size int, emit func(buf []byte) bool) error {
	return framePipe{size: size, alloc: func() []byte { return make([]byte, size) },
		emit: emit, recycle: func([]byte) {}}.run(r)
}

// TestReadFrames verifies stride math: exact frame boundaries, fresh buffers, torn tail dropped.
func TestReadFrames(t *testing.T) {
	const w, h = 4, 2
	size, _ := frameSize(w, h)
	var in []byte
	for i := 0; i < 2; i++ {
		f := bytes.Repeat([]byte{byte(i + 1)}, size)
		in = append(in, f...)
	}
	in = append(in, 0xEE, 0xEE) // torn trailing partial frame

	var got [][]byte
	// half-reads exercise io.ReadFull reassembly
	err := readFrames(iotest.HalfReader(bytes.NewReader(in)), size, func(buf []byte) bool {
		got = append(got, buf)
		return true
	})
	if err != nil {
		t.Fatalf("readFrames: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("frames: got %d want 2", len(got))
	}
	for i, f := range got {
		if len(f) != size {
			t.Fatalf("frame %d size %d want %d", i, len(f), size)
		}
		for _, b := range f {
			if b != byte(i+1) {
				t.Fatalf("frame %d corrupted", i)
			}
		}
	}
	// fresh buffers: mutating one frame must not touch the other
	got[0][0] = 0xFF
	if got[1][0] == 0xFF {
		t.Fatal("frames share a buffer")
	}
}

func TestReadFramesStop(t *testing.T) {
	size := 8
	in := bytes.Repeat([]byte{1}, size*3)
	n := 0
	err := readFrames(bytes.NewReader(in), size, func([]byte) bool { n++; return false })
	if err != nil || n != 1 {
		t.Fatalf("stop: n=%d err=%v", n, err)
	}
}

func TestReadFramesError(t *testing.T) {
	size := 8
	r := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte{1}, size)), iotest.ErrReader(io.ErrClosedPipe))
	n := 0
	err := readFrames(r, size, func([]byte) bool { n++; return true })
	if n != 1 || err == nil {
		t.Fatalf("want 1 frame + error, got n=%d err=%v", n, err)
	}
}

// TestFramePipeRecyclesUnemittedBuffers is the ownership contract: every buffer the pipe takes from
// alloc is either EMITTED or handed to recycle. A buffer that is neither would leak - and with a
// pooled allocator, leaking pins the pool's live ceiling until the process exits.
func TestFramePipeRecyclesUnemittedBuffers(t *testing.T) {
	const size = 8
	cases := []struct {
		name string
		r    io.Reader
	}{
		{"clean EOF", bytes.NewReader(bytes.Repeat([]byte{1}, size*2))},
		{"torn tail", bytes.NewReader(bytes.Repeat([]byte{1}, size*2+3))},
		{"read error", io.MultiReader(bytes.NewReader(bytes.Repeat([]byte{1}, size)), iotest.ErrReader(io.ErrClosedPipe))},
	}
	for _, c := range cases {
		allocated, emitted, recycled := 0, 0, 0
		_ = framePipe{
			size:    size,
			alloc:   func() []byte { allocated++; return make([]byte, size) },
			emit:    func([]byte) bool { emitted++; return true },
			recycle: func([]byte) { recycled++ },
		}.run(c.r)
		if allocated != emitted+recycled {
			t.Fatalf("%s: allocated %d but emitted %d + recycled %d - a buffer leaked",
				c.name, allocated, emitted, recycled)
		}
	}
}

// TestFramePipeStopKeepsTheEmittedBuffer: when emit says stop it has ALREADY taken that buffer, so
// the pipe must not also recycle it (a double return would hand one buffer to two writers).
func TestFramePipeStopKeepsTheEmittedBuffer(t *testing.T) {
	const size = 8
	allocated, emitted, recycled := 0, 0, 0
	_ = framePipe{
		size:    size,
		alloc:   func() []byte { allocated++; return make([]byte, size) },
		emit:    func([]byte) bool { emitted++; return false },
		recycle: func([]byte) { recycled++ },
	}.run(bytes.NewReader(bytes.Repeat([]byte{1}, size*3)))
	if allocated != 1 || emitted != 1 || recycled != 0 {
		t.Fatalf("alloc=%d emit=%d recycle=%d, want 1/1/0", allocated, emitted, recycled)
	}
}
