package mocapnode

// ffmpeg source tests: pure command-line construction only - tests never spawn ffmpeg.

import (
	"image"
	"strings"
	"testing"
)

func desktopArgs(t *testing.T, s *FFmpegDesktopSource) string {
	t.Helper()
	args, err := s.args()
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	return strings.Join(args, " ")
}

func TestDesktopArgsDdagrab(t *testing.T) {
	s := &FFmpegDesktopSource{Monitor: 1, FPS: 30, W: 2560, H: 1440}
	joined := desktopArgs(t, s)
	if !strings.Contains(joined, "-f lavfi") {
		t.Fatalf("missing lavfi input: %s", joined)
	}
	if !strings.Contains(joined, "ddagrab=output_idx=1:framerate=30,hwdownload,format=bgra") {
		t.Fatalf("bad ddagrab filter graph: %s", joined)
	}
	if !strings.HasSuffix(joined, "-f rawvideo -pix_fmt bgra -") {
		t.Fatalf("missing raw bgra pipe: %s", joined)
	}
	// No scaling/colour filters - contract bytes must arrive as captured.
	for _, bad := range []string{"scale", "colorspace", "-vf"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("unexpected %q (would resample contract bytes): %s", bad, joined)
		}
	}
	if w, h, err := s.frameSize(); err != nil || w != 2560 || h != 1440 {
		t.Fatalf("frameSize=(%d,%d,%v)", w, h, err)
	}
}

func TestDesktopArgsDdagrabCrop(t *testing.T) {
	s := &FFmpegDesktopSource{Crop: image.Rect(100, 200, 1380, 920)}
	joined := desktopArgs(t, s)
	if !strings.Contains(joined, "ddagrab=output_idx=0:framerate=30:video_size=1280x720:offset_x=100:offset_y=200,hwdownload,format=bgra") {
		t.Fatalf("crop not mapped to ddagrab options: %s", joined)
	}
	if w, h, err := s.frameSize(); err != nil || w != 1280 || h != 720 {
		t.Fatalf("frameSize=(%d,%d,%v)", w, h, err)
	}
}

func TestDesktopArgsGdigrab(t *testing.T) {
	s := &FFmpegDesktopSource{Grabber: "gdigrab", FPS: 24, Crop: image.Rect(10, 20, 650, 500)}
	joined := desktopArgs(t, s)
	if !strings.Contains(joined, "-f gdigrab -framerate 24") {
		t.Fatalf("missing gdigrab input: %s", joined)
	}
	if !strings.Contains(joined, "-offset_x 10 -offset_y 20 -video_size 640x480") {
		t.Fatalf("crop not mapped to gdigrab flags: %s", joined)
	}
	if !strings.Contains(joined, "-i desktop") {
		t.Fatalf("missing desktop input: %s", joined)
	}
	if !strings.HasSuffix(joined, "-f rawvideo -pix_fmt bgra -") {
		t.Fatalf("missing raw bgra pipe: %s", joined)
	}

	// Full-desktop gdigrab still needs explicit geometry for the raw pipe.
	full := &FFmpegDesktopSource{Grabber: "gdigrab", W: 1920, H: 1080}
	if joined := desktopArgs(t, full); !strings.Contains(joined, "-video_size 1920x1080") {
		t.Fatalf("full-desktop gdigrab missing video_size: %s", joined)
	}
}

func TestDesktopArgsErrors(t *testing.T) {
	if _, err := (&FFmpegDesktopSource{Grabber: "quartz"}).args(); err == nil {
		t.Error("unknown grabber accepted")
	}
	if _, _, err := (&FFmpegDesktopSource{}).frameSize(); err == nil {
		t.Error("missing geometry accepted (raw pipe cannot be chunked)")
	}
}

func TestDShowArgs(t *testing.T) {
	s := &FFmpegDShowSource{Device: "OBS Virtual Camera", FPS: 30}
	joined := strings.Join(s.args(1920, 1080), " ")
	if !strings.Contains(joined, "-f dshow") {
		t.Fatalf("missing dshow input: %s", joined)
	}
	if !strings.Contains(joined, "-framerate 30") {
		t.Fatalf("missing framerate: %s", joined)
	}
	if !strings.Contains(joined, "-video_size 1920x1080") {
		t.Fatalf("missing video_size: %s", joined)
	}
	if !strings.Contains(joined, "-i video=OBS Virtual Camera") {
		t.Fatalf("device not addressed as video=<name>: %s", joined)
	}
	if !strings.HasSuffix(joined, "-f rawvideo -pix_fmt bgra -") {
		t.Fatalf("missing raw bgra pipe: %s", joined)
	}

	// FPS 0 = device default: no -framerate.
	if joined := strings.Join((&FFmpegDShowSource{Device: "x"}).args(1920, 1080), " "); strings.Contains(joined, "-framerate") {
		t.Fatalf("unexpected -framerate at device default: %s", joined)
	}
}

func TestFrameRGBFormats(t *testing.T) {
	rgb := Frame{Pix: []byte{1, 2, 3, 4, 5, 6}, W: 2, H: 1, Stride: 6, Fmt: FmtRGB24}
	if r, g, b := rgb.RGB(1, 0); r != 4 || g != 5 || b != 6 {
		t.Errorf("RGB24 read: %d %d %d", r, g, b)
	}
	bgra := Frame{Pix: []byte{10, 20, 30, 255, 40, 50, 60, 255}, W: 2, H: 1, Stride: 8, Fmt: FmtBGRA}
	if r, g, b := bgra.RGB(1, 0); r != 60 || g != 50 || b != 40 {
		t.Errorf("BGRA read: %d %d %d", r, g, b)
	}
}
