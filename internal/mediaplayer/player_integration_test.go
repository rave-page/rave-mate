package mediaplayer

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/mediatools"
)

// TestPlayerReal exercises the streaming Player against a generated testsrc+sine clip: open, play,
// confirm the clock advances + a frame decodes, seek, then close cleanly. Gated by
// MEDIAPLAYER_INTEGRATION=1 (needs ffmpeg + an audio device; not part of the normal suite).
func TestPlayerReal(t *testing.T) {
	if os.Getenv("MEDIAPLAYER_INTEGRATION") == "" {
		t.Skip("set MEDIAPLAYER_INTEGRATION=1 to run (needs ffmpeg + audio out)")
	}
	ff, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg not found")
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "test.mp4")
	gen := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=30:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate clip: %v\n%s", err, out)
	}

	p := New(nil)
	if err := p.Open(clip, 320, 180); err != nil {
		t.Fatalf("open: %v", err)
	}
	st := p.State()
	if !st.HasVideo || st.Total < 2.5 {
		t.Fatalf("unexpected state after open: %+v", st)
	}

	p.Play()
	time.Sleep(600 * time.Millisecond)

	st = p.State()
	if st.Cur <= 0 {
		t.Fatalf("clock did not advance: cur=%.3f", st.Cur)
	}
	if !st.Playing {
		t.Fatal("want Playing after Play()")
	}
	if p.Frame() == nil {
		t.Fatal("want a decoded video frame, got nil")
	}

	p.Seek(2.0)
	time.Sleep(300 * time.Millisecond)
	if cur := p.State().Cur; cur < 1.9 {
		t.Fatalf("seek did not take effect: cur=%.3f want >=1.9", cur)
	}

	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return (leaked ffmpeg / goroutine)")
	}
}
