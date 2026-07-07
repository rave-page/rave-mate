package mediaplayer

import (
	"context"
	"fmt"
	"image"
	"io"
	"os/exec"
	"time"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

// FrameAt decodes a single video frame at t seconds, scaled to w×h, as an RGBA image. Frame-
// accurate (fast pre-input -ss to the nearest keyframe, then exact decode to t). Powers scrub
// previews + trim cut-point setting. Returns an error for audio-only files / decode failure.
func FrameAt(ctx context.Context, file string, t float64, w, h int) (*image.NRGBA, error) {
	bin, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, fmt.Errorf("ffmpeg not found (install FFmpeg in Settings)")
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("frame size must be positive")
	}
	if t < 0 {
		t = 0
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// -ss before -i = fast (keyframe) seek; -ss after -i would be accurate-but-slow. For a preview
	// the keyframe-near frame is fine and snappy; the player's real decode loop handles exactness.
	cmd := exec.CommandContext(cctx, bin,
		"-hide_banner", "-loglevel", "error",
		"-ss", ftoa(t), "-i", file,
		"-frames:v", "1",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", w, h),
		"pipe:1")
	sysexec.Hide(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	_, readErr := io.ReadFull(stdout, img.Pix)
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("decode frame @%.3fs: %w", t, readErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("ffmpeg frame @%.3fs: %w", t, waitErr)
	}
	return img, nil
}

// ftoa formats seconds for ffmpeg's -ss (millisecond precision is plenty).
func ftoa(sec float64) string { return fmt.Sprintf("%.3f", sec) }
