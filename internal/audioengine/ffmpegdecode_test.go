package audioengine

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/mediatools"
)

// TestFFmpegDecodeRealAAC generates a real AAC file and decodes it through the ffmpeg source,
// proving aac/m4a play on the native transport (frames out, total known, seek moves position).
// Skips when ffmpeg isn't resolvable (CI without the managed binary).
func TestFFmpegDecodeRealAAC(t *testing.T) {
	ff, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg not resolvable - skipping real-decode test")
	}
	dir := t.TempDir()
	aac := filepath.Join(dir, "tone.aac")
	// 2s 440Hz stereo sine → AAC (the exact codec beep can't decode).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gen := exec.CommandContext(ctx, ff, "-nostdin", "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=2:sample_rate=48000", "-ac", "2", "-c:a", "aac", aac)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate aac: %v\n%s", err, out)
	}

	if !IsPlayable(aac) {
		t.Fatal("IsPlayable(aac) = false with ffmpeg present")
	}
	s, format, err := decodeAudio(aac)
	if err != nil {
		t.Fatalf("decodeAudio: %v", err)
	}
	defer s.Close()

	if format.SampleRate != ffRate || format.NumChannels != ffChannels {
		t.Fatalf("format = %d Hz / %dch, want %d / %d", format.SampleRate, format.NumChannels, ffRate, ffChannels)
	}
	if s.Len() < ffRate { // ~2s → ~96k frames; at least 1s of frames
		t.Fatalf("Len = %d frames, want >= %d (~duration)", s.Len(), ffRate)
	}

	// Pull ~0.5s of audio; frames must flow and a non-silent sample must appear.
	buf := make([][2]float64, ffRate/2)
	n, streamOK := s.Stream(buf)
	if n == 0 || !streamOK {
		t.Fatalf("Stream returned (%d,%v), want frames", n, streamOK)
	}
	nonzero := false
	for i := range n {
		if buf[i][0] != 0 || buf[i][1] != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("decoded a full block of pure silence - decode likely failed")
	}
	if s.Position() != n {
		t.Fatalf("Position = %d after %d frames streamed", s.Position(), n)
	}

	// Seek to ~1s and confirm position reflects the jump.
	if err := s.Seek(ffRate); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if s.Position() != ffRate {
		t.Fatalf("Position after seek = %d, want %d", s.Position(), ffRate)
	}
	if n2, ok2 := s.Stream(make([][2]float64, 1024)); n2 == 0 || !ok2 {
		t.Fatalf("Stream after seek = (%d,%v), want frames", n2, ok2)
	}
}
