package webcam

import (
	"os/exec"
	"slices"
	"testing"

	"rave.page/mate/internal/mediatools"
)

// The capture argv's OUTPUT side over REAL ffmpeg (skips without it): a 30-declared source that
// really delivers 22.5 fps (every 4th frame missing - a camera under exposure priority) must come out
// as 22.5 fps of real frames, never padded to 30 with back-to-back duplicates.
func TestCaptureOutputNoCFRDuplicates(t *testing.T) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg not available")
	}
	full := captureArgsFor("linux", capDesc{Device: "x", W: 64, H: 48, FPS: 30}, "/dev/null")
	out := full[slices.Index(full, "-i")+2:] // everything after "-i <input>"
	args := append([]string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"testsrc=size=64x48:rate=30:duration=4,select='not(eq(mod(n\\,4)\\,2))'"}, out...)
	raw, err := exec.Command(ffmpeg, args...).Output()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v", args, err)
	}
	n := len(raw) / (64 * 48 * 4)
	if n != 90 { // 120 source frames, 1 in 4 missing
		t.Fatalf("capture output emitted %d frames from 90 real ones (CFR duplication)", n)
	}
}
