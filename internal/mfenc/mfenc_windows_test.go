//go:build windows && cgo

package mfenc

import (
	"bytes"
	"testing"
	"time"
)

// nalTypes collects H.264 NAL unit types present in an annex-B AU.
func nalTypes(au []byte) map[int]bool {
	out := map[int]bool{}
	for i := 0; i+3 < len(au); i++ {
		if au[i] == 0 && au[i+1] == 0 && (au[i+2] == 1 || (au[i+2] == 0 && i+4 < len(au) && au[i+3] == 1)) {
			off := i + 3
			if au[i+2] == 0 {
				off = i + 4
			}
			if off < len(au) {
				out[int(au[off]&0x1f)] = true
			}
		}
	}
	return out
}

// TestEncodeRealFrames drives the REAL hardware pipeline: 60 moving-gradient frames at
// 720p60 → assert annex-B AUs with SPS+IDR up front, more AUs after, ForceKeyframe
// produces a later keyframe, Close drains without hanging.
func TestEncodeRealFrames(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	const w, h = 1280, 720
	enc, err := New(w, h, w, h, 60, 6000, 120)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Logf("encoder=%q bgraIn=%v", enc.Name(), enc.InputIsBGRA())

	var aus []AU
	done := make(chan struct{})
	go func() {
		for au := range enc.Output() {
			aus = append(aus, au)
		}
		close(done)
	}()

	frame := make([]byte, w*h*4)
	for i := 0; i < 60; i++ {
		for y := 0; y < h; y++ { // moving gradient = real motion for the encoder
			row := frame[y*w*4:]
			for x := 0; x < w; x++ {
				row[x*4] = byte(x + i*8)
				row[x*4+1] = byte(y + i*4)
				row[x*4+2] = byte(i * 16)
				row[x*4+3] = 255
			}
		}
		if i == 40 {
			enc.ForceKeyframe()
		}
		if err := enc.Encode(frame, int64(i)*16_666_667); err != nil {
			t.Fatalf("Encode frame %d: %v", i, err)
		}
	}
	closed := make(chan struct{})
	go func() { enc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("Close hung")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}

	if len(aus) < 30 {
		t.Fatalf("only %d AUs for 60 frames", len(aus))
	}
	first := aus[0]
	if !bytes.HasPrefix(first.Data, []byte{0, 0, 0, 1}) && !bytes.HasPrefix(first.Data, []byte{0, 0, 1}) {
		t.Fatalf("first AU not annex-B: % x", first.Data[:8])
	}
	nt := nalTypes(first.Data)
	if !nt[7] || !nt[5] {
		t.Fatalf("first AU missing SPS(7)/IDR(5): %v", nt)
	}
	if !first.Keyframe {
		t.Fatal("first AU not flagged keyframe")
	}
	var later int
	total := 0
	for i, au := range aus[1:] {
		total += len(au.Data)
		if au.Keyframe && i+1 >= 30 {
			later++
		}
	}
	if later == 0 {
		t.Fatal("ForceKeyframe produced no later keyframe")
	}
	t.Logf("aus=%d firstAU=%dB avg=%dB laterKeyframes=%d", len(aus), len(first.Data), total/len(aus[1:]), later)
}

// TestSwizzleCanary pins the RGBA→BGRA upload swizzle on a known 4-px pattern (used
// only when the VP negotiates ARGB32; ABGR32 negotiation needs no swizzle).
func TestSwizzleCanary(t *testing.T) {
	src := []byte{
		0xFF, 0x00, 0x00, 0xAA, // pure red RGBA
		0x00, 0xFF, 0x00, 0xBB, // green
		0x00, 0x00, 0xFF, 0xCC, // blue
		0x11, 0x22, 0x33, 0x44,
	}
	dst := make([]byte, len(src))
	SwizzleRGBAToBGRA(dst, src)
	want := []byte{
		0x00, 0x00, 0xFF, 0xAA, // red in BGRA memory
		0x00, 0xFF, 0x00, 0xBB,
		0xFF, 0x00, 0x00, 0xCC,
		0x33, 0x22, 0x11, 0x44,
	}
	if !bytes.Equal(dst, want) {
		t.Fatalf("swizzle: got % x want % x", dst, want)
	}
}
