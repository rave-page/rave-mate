package webcam

import (
	"slices"
	"testing"
)

// captureArgs emits -input_format as an INPUT option (before -i) when the chosen mode names a
// codec, and omits it entirely when the descriptor leaves it unset.
func TestCaptureArgsInputFormat(t *testing.T) {
	withFmt := captureArgs(capDesc{Device: "Cam", W: 1920, H: 1080, FPS: 30, InputFormat: "mjpeg"})
	fi := slices.Index(withFmt, "-input_format")
	if fi < 0 || fi+1 >= len(withFmt) || withFmt[fi+1] != "mjpeg" {
		t.Fatalf("-input_format mjpeg missing: %v", withFmt)
	}
	if ii := slices.Index(withFmt, "-i"); ii < 0 || fi > ii {
		t.Fatalf("-input_format must precede -i: %v", withFmt)
	}

	noFmt := captureArgs(capDesc{Device: "Cam", W: 1280, H: 720, FPS: 30})
	if slices.Contains(noFmt, "-input_format") {
		t.Fatalf("-input_format must be absent when unset: %v", noFmt)
	}
}
