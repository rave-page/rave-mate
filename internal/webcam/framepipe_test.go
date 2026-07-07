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
