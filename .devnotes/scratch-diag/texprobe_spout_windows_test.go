//go:build spout

package mfenc

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/videoshare"
)

// TestSpoutTexProbe opens an EXISTING sender's DX shared texture zero-copy, encodes ~1.5s,
// decodes the first frame and prints band luma stats. Diagnostic, opt-in:
// RAVE_SPOUT_TEXPROBE=<sender name>. Answers "does the TEXTURE hold content" independent of
// the CPU ReceiveImage readback.
func TestSpoutTexProbe(t *testing.T) {
	name := os.Getenv("RAVE_SPOUT_TEXPROBE")
	if name == "" {
		t.Skip("set RAVE_SPOUT_TEXPROBE=<sender name>")
	}
	if !Available() {
		t.Skip("no hardware MFT")
	}
	requireEncExe(t)
	handle, dxgi, w, h := uint64(0), uint32(0), 0, 0
	deadline := time.Now().Add(10 * time.Second)
	for {
		hh, f, ww, hgt, ok := videoshare.SenderShare(name)
		if ok {
			handle, dxgi, w, h = hh, f, ww, hgt
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sender %q: no usable shared texture in registry", name)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("sender %q: handle=%#x fmt=%d %dx%d", name, handle, dxgi, w, h)

	s, err := OpenProcSessionOpts(ProcOpts{
		LUID: 0, InW: w, InH: h, OutW: w, OutH: h, FPS: 60, Kbps: 20000, Gop: 60,
		Spout: &SpoutSource{Name: name, Resolve: func() (uint64, uint32, int, int, bool) {
			return videoshare.SenderShare(name)
		}},
	})
	if err != nil {
		t.Fatalf("zero-copy open: %v", err)
	}
	var aus []AU
	done := make(chan struct{})
	go func() {
		for au := range s.Output() {
			aus = append(aus, au)
		}
		close(done)
	}()
	time.Sleep(1500 * time.Millisecond)
	st := s.Stats()
	s.Close()
	<-done
	t.Logf("aus=%d capFrames=%d capFPS=%.1f srcErrors=%d capFlags=%#x", len(aus), st.CapFrames, st.CapFPS, st.SrcErrors, st.CapFlags)
	if len(aus) == 0 {
		t.Fatal("no AUs")
	}

	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("no ffmpeg")
	}
	var in bytes.Buffer
	for _, au := range aus {
		in.Write(au.Data)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0",
		"-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	cmd.Stdin = &in
	out, err := cmd.Output()
	if err != nil || len(out) < w*h*3 {
		t.Fatalf("decode failed (%v), got %d bytes", err, len(out))
	}
	// Per-band mean + max over 3 horizontal bands.
	for band := 0; band < 3; band++ {
		y0, y1 := h*band/3, h*(band+1)/3
		var sum, n uint64
		var mx byte
		for y := y0; y < y1; y += 16 {
			for x := 0; x < w; x += 64 {
				for c := 0; c < 3; c++ {
					v := out[(y*w+x)*3+c]
					sum += uint64(v)
					if v > mx {
						mx = v
					}
					n++
				}
			}
		}
		fmt.Fprintf(os.Stderr, "[texprobe] band %d: mean=%.1f max=%d samples=%d\n", band, float64(sum)/float64(n), mx, n)
	}
}
