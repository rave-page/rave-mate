package mediaplayer

import (
	"context"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/mediatools"
)

// TestProbeAndFrameReal generates a real test clip with ffmpeg, then exercises Probe + FrameAt.
// Gated: set MEDIAPLAYER_INTEGRATION=1 (needs ffmpeg on PATH / managed). Set MEDIAPLAYER_FRAME_OUT
// to also write the extracted frame as a PNG for visual inspection.
func TestProbeAndFrameReal(t *testing.T) {
	if os.Getenv("MEDIAPLAYER_INTEGRATION") == "" {
		t.Skip("set MEDIAPLAYER_INTEGRATION=1 to run (needs ffmpeg)")
	}
	ff, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg not found")
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "test.mp4")
	// 5s 1280x720 30fps testsrc + a sine tone → H.264/AAC mp4 (what OBS-ish recordings look like).
	gen := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=1280x720:rate=30:duration=5",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=5",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate clip: %v\n%s", err, out)
	}

	in, err := Probe(context.Background(), clip)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("probe: %+v", in)
	if !in.HasVideo || !in.HasAudio {
		t.Fatalf("want video+audio, got %+v", in)
	}
	if in.Width != 1280 || in.Height != 720 {
		t.Fatalf("size = %dx%d, want 1280x720", in.Width, in.Height)
	}
	if in.Duration < 4.5 || in.Duration > 5.5 {
		t.Fatalf("duration = %.2f, want ~5", in.Duration)
	}

	img, err := FrameAt(context.Background(), clip, 2.5, 640, 360)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 640 || img.Bounds().Dy() != 360 {
		t.Fatalf("frame size = %v, want 640x360", img.Bounds())
	}
	// Sanity: the frame isn't all-zero (testsrc has colour bars).
	nonzero := false
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0 || img.Pix[i+1] != 0 || img.Pix[i+2] != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("extracted frame is all black")
	}
	if out := os.Getenv("MEDIAPLAYER_FRAME_OUT"); out != "" {
		f, _ := os.Create(out)
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote frame → %s", out)
	}
}
