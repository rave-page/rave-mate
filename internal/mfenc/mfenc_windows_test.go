//go:build windows && cgo

package mfenc

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
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

// encodeOneAndClose opens luid0, feeds one gradient frame, closes. Any failure must be a
// clean error - never a process fault (the 4K60 field crash killed the media child inside
// mf_enc_open).
func encodeOneAndClose(t *testing.T, inW, inH, outW, outH int, fps float64, kbps, gop int) error {
	t.Helper()
	enc, err := NewOn(0, inW, inH, outW, outH, fps, kbps, gop)
	if err != nil {
		return err
	}
	go func() {
		for range enc.Output() { //nolint:revive // drain
		}
	}()
	frame := make([]byte, inW*inH*4)
	for i := range frame {
		frame[i] = byte(i)
	}
	encErr := enc.Encode(frame, 0)
	closed := make(chan struct{})
	go func() { enc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(20 * time.Second):
		t.Fatal("Close hung")
	}
	return encErr
}

// TestOpenCrashTuple4K60 is the exact field-crash tuple (build 157): 3840x2160@60,
// 50 Mbps, gop 120, luid 0. Open→encode→close must complete or fail cleanly.
func TestOpenCrashTuple4K60(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	if err := encodeOneAndClose(t, 3840, 2160, 3840, 2160, 60, 50000, 120); err != nil {
		t.Logf("clean open/encode failure (acceptable, must degrade upstream): %v", err)
	}
}

// TestOpenSizeTable sweeps odd/edge geometries through open→encode-one→close. Odd OUTPUT
// dims may fail (caller contract: pre-clamp to even) but must fail cleanly; even outputs
// must encode.
func TestOpenSizeTable(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	cases := []struct {
		name                 string
		inW, inH, outW, outH int
		fps                  float64
		kbps, gop            int
		mustWork             bool
	}{
		{"1080p60", 1920, 1080, 1920, 1080, 60, 8000, 120, true},
		{"4k30", 3840, 2160, 3840, 2160, 30, 25000, 60, true},
		{"4k60-scale-1080", 3840, 2160, 1920, 1080, 60, 8000, 120, true},
		{"odd-in-even-out", 1919, 1079, 1918, 1078, 30, 4000, 60, true},
		{"odd-out-w", 1280, 720, 1279, 720, 30, 4000, 60, false},
		{"odd-out-h", 1280, 720, 1280, 719, 30, 4000, 60, false},
		{"tiny", 320, 240, 320, 240, 30, 500, 30, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := encodeOneAndClose(t, c.inW, c.inH, c.outW, c.outH, c.fps, c.kbps, c.gop)
			if err != nil && c.mustWork {
				t.Fatalf("%s: %v", c.name, err)
			}
			if err != nil {
				t.Logf("clean failure: %v", err)
			}
		})
	}
}

// TestOpenFailKnob: RAVE_MATE_MFENC_OPEN_FAIL forces a clean Go-side open failure (field
// kill-switch + the degrade-path simulation hook).
func TestOpenFailKnob(t *testing.T) {
	t.Setenv("RAVE_MATE_MFENC_OPEN_FAIL", "1")
	if _, err := NewOn(0, 320, 240, 320, 240, 30, 500, 30); err == nil {
		t.Fatal("NewOn succeeded despite RAVE_MATE_MFENC_OPEN_FAIL")
	}
}

// TestFaultGuardSubprocess proves the crash→degrade chain BY EXECUTION: a child test process
// injects a REAL access violation inside mf_enc_open (RAVE_MATE_MFENC_FAULT_INJECT, read at
// CRT startup); the VEH guard must turn it into a clean Go error, poison the shim so the next
// open fast-fails, and leave the process alive. No hardware needed - the injection fires
// before device creation.
func TestFaultGuardSubprocess(t *testing.T) {
	if os.Getenv("MFENC_FAULT_HELPER") == "1" {
		_, err := NewOn(0, 320, 240, 320, 240, 30, 500, 30)
		if err == nil || !strings.Contains(err.Error(), "driver fault") {
			t.Fatalf("want driver-fault error, got %v", err)
		}
		_, err = NewOn(0, 320, 240, 320, 240, 30, 500, 30)
		if err == nil || !strings.Contains(err.Error(), "disabled by earlier driver fault") {
			t.Fatalf("want poisoned fast-fail, got %v", err)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run", "^TestFaultGuardSubprocess$", "-test.v")
	cmd.Env = append(os.Environ(), "MFENC_FAULT_HELPER=1", "RAVE_MATE_MFENC_FAULT_INJECT=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper died (guard failed to contain the fault): %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("PASS")) {
		t.Fatalf("helper did not pass:\n%s", out)
	}
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
