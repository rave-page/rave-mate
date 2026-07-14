package audio

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/mediatools"
)

// TestPlayable: native decoders play always; ffmpeg-only formats play exactly when ffmpeg resolves;
// video/non-audio never (external Open).
func TestPlayable(t *testing.T) {
	// Native decoders (incl. AIFF, which internal/audio decodes natively) - always playable.
	for _, p := range []string{"a.mp3", "A.WAV", "x.flac", "y.ogg", "z.oga", "t.aiff", "t.aif"} {
		if !Playable(p) {
			t.Errorf("Playable(%q) = false, want true (native)", p)
		}
	}
	// Video / non-audio: never playable, regardless of ffmpeg.
	for _, p := range []string{"a.mp4", "a.mkv", "a.txt", "a.mov"} {
		if Playable(p) {
			t.Errorf("Playable(%q) = true, want false", p)
		}
	}
	// ffmpeg-only audio: playable exactly when ffmpeg resolves (native transport, not external Open).
	wantFF := ffmpegAvailable()
	for _, p := range []string{"a.m4a", "a.aac", "a.opus", "a.alac", "a.wma", "a.mka", "a.caf"} {
		if got := Playable(p); got != wantFF {
			t.Errorf("Playable(%q) = %v, want %v (ffmpeg available=%v)", p, got, wantFF, wantFF)
		}
	}
}

// TestFFmpegDecodeRealAAC generates a real AAC file and decodes it through the ffmpeg-backed
// audio.Decoder, proving aac/m4a play on the native transport (frames out, total known, seek moves).
// Skips when ffmpeg isn't resolvable (CI without the managed binary).
func TestFFmpegDecodeRealAAC(t *testing.T) {
	ff, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg not resolvable - skipping real-decode test")
	}
	dir := t.TempDir()
	aac := filepath.Join(dir, "tone.aac")
	// 2s 440Hz stereo sine → AAC (a codec the native decoders don't handle).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gen := exec.CommandContext(ctx, ff, "-nostdin", "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=2:sample_rate=48000", "-ac", "2", "-c:a", "aac", aac)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate aac: %v\n%s", err, out)
	}

	if !Playable(aac) || !FFmpegPlayable(aac) {
		t.Fatal("Playable/FFmpegPlayable(aac) = false with ffmpeg present")
	}
	dec, err := OpenFFmpeg(aac)
	if err != nil {
		t.Fatalf("OpenFFmpeg: %v", err)
	}
	defer dec.Close()

	if f := dec.Format(); f.SampleRate != deviceRate || f.Channels != deviceChannels {
		t.Fatalf("format = %d Hz / %dch, want %d / %d", f.SampleRate, f.Channels, deviceRate, deviceChannels)
	}
	if tf := dec.TotalFrames(); tf < deviceRate { // ~2s → ~96k frames; at least ~1s
		t.Fatalf("TotalFrames = %d, want >= %d (~duration)", tf, deviceRate)
	}

	// Pull ~0.5s; frames must flow and a non-silent sample must appear.
	buf := make([]float32, (deviceRate/2)*deviceChannels)
	n, err := dec.ReadFrames(buf)
	if n == 0 || (err != nil && err != io.EOF) {
		t.Fatalf("ReadFrames = (%d,%v), want frames", n, err)
	}
	nonzero := false
	for i := 0; i < n*deviceChannels; i++ {
		if buf[i] != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("decoded a full block of pure silence - decode likely failed")
	}

	// Seek to ~1s and confirm frames still flow from the jump.
	if err := dec.SeekTo(int64(deviceRate)); err != nil {
		t.Fatalf("SeekTo: %v", err)
	}
	if n2, err2 := dec.ReadFrames(make([]float32, 1024*deviceChannels)); n2 == 0 || (err2 != nil && err2 != io.EOF) {
		t.Fatalf("ReadFrames after seek = (%d,%v), want frames", n2, err2)
	}
}
