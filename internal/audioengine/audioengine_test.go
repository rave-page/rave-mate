package audioengine

import "testing"

func TestIsPlayable(t *testing.T) {
	// beep built-ins: always playable.
	for _, p := range []string{"a.mp3", "A.WAV", "x.flac", "y.ogg", "z.oga"} {
		if !IsPlayable(p) {
			t.Errorf("IsPlayable(%q) = false, want true", p)
		}
	}
	// Video / non-audio: never playable, regardless of ffmpeg.
	for _, p := range []string{"a.mp4", "a.mkv", "a.txt", "a.mov"} {
		if IsPlayable(p) {
			t.Errorf("IsPlayable(%q) = true, want false", p)
		}
	}
	// ffmpeg-only audio: playable exactly when ffmpeg resolves (native transport, not external Open).
	wantFF := ffmpegAvailable()
	for _, p := range []string{"a.m4a", "a.aac", "a.opus", "a.aiff", "a.alac", "a.wma"} {
		if got := IsPlayable(p); got != wantFF {
			t.Errorf("IsPlayable(%q) = %v, want %v (ffmpeg available=%v)", p, got, wantFF, wantFF)
		}
	}
}

type panicStreamer struct{ calls int }

func (p *panicStreamer) Stream([][2]float64) (int, bool) { p.calls++; panic("decoder boom") }
func (p *panicStreamer) Err() error                      { return nil }

// TestSafeStreamerContainsPanic: a codec panic must be recovered at the streamer boundary
// (returns end-of-stream), not propagate into oto's audio goroutine and crash the child.
func TestSafeStreamerContainsPanic(t *testing.T) {
	inner := &panicStreamer{}
	s := &safeStreamer{inner: inner, e: &Engine{}, path: "corrupt.flac"} // nil log/callbacks ok

	n, ok := s.Stream(make([][2]float64, 8))
	if n != 0 || ok {
		t.Fatalf("expected drained (0,false) after panic, got (%d,%v)", n, ok)
	}
	if !s.dead {
		t.Fatal("streamer should be marked dead")
	}
	if n, ok := s.Stream(make([][2]float64, 8)); n != 0 || ok {
		t.Fatal("dead streamer must stay drained")
	}
	if inner.calls != 1 {
		t.Fatalf("inner should be called once then skipped, got %d", inner.calls)
	}
}
