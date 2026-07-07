package setedit

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/mediatools"
)

// TestDetectSilence generates [2s silence | 3s tone | 2s silence] and checks the music region.
func TestDetectSilence(t *testing.T) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg not resolvable")
	}
	path := filepath.Join(t.TempDir(), "s.wav")
	// 3s 440Hz tone, delayed 2s (leading silence), padded 2s (trailing silence) → 7s total.
	gen := exec.Command(ffmpeg, "-hide_banner", "-y", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=3",
		"-af", "adelay=2000|2000,apad=pad_dur=2", "-ar", "48000", "-ac", "2", path)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	s, err := DetectSilence(context.Background(), path, 7.0)
	if err != nil {
		t.Fatal(err)
	}
	if s.LeadEndSec < 1.5 || s.LeadEndSec > 2.7 {
		t.Errorf("LeadEndSec=%.2f, want ~2.0", s.LeadEndSec)
	}
	if s.TailStartSec < 4.4 || s.TailStartSec > 5.6 {
		t.Errorf("TailStartSec=%.2f, want ~5.0", s.TailStartSec)
	}
}
