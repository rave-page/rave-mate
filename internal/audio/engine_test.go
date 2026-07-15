package audio

import (
	"io"
	"path/filepath"
	"testing"
)

// fakePlayer is a testable outputPlayer: it never touches a device. pump(nFrames) simulates the
// device pulling from the source (advancing its cursor) so we can assert transport behavior.
type fakePlayer struct {
	r        io.Reader
	playing  bool
	buffered int
	vol      float64
}

func (p *fakePlayer) Play()               { p.playing = true }
func (p *fakePlayer) Pause()              { p.playing = false }
func (p *fakePlayer) Reset()              { p.buffered = 0 }
func (p *fakePlayer) IsPlaying() bool     { return p.playing }
func (p *fakePlayer) BufferedSize() int   { return p.buffered }
func (p *fakePlayer) SetVolume(v float64) { p.vol = v }
func (p *fakePlayer) Close() error        { return nil }

// pump reads nFrames worth of device bytes from the source (simulates playback advancing).
func (p *fakePlayer) pump(t *testing.T, nFrames int) {
	t.Helper()
	buf := make([]byte, nFrames*deviceBytes*deviceChannels)
	_, _ = p.r.Read(buf)
}

func TestEngineTransportPreview(t *testing.T) {
	// Route the engine's output to a fake device for this test.
	var fake *fakePlayer
	orig := newOutput
	newOutput = func(r io.Reader) (outputPlayer, error) { fake = &fakePlayer{r: r, vol: 1}; return fake, nil }
	defer func() { newOutput = orig }()

	// 2s of 48k stereo => preloads to RAM.
	path := filepath.Join(t.TempDir(), "t.wav")
	writeWAV(t, path, 2*deviceRate, deviceChannels, 16, false)

	e := NewEngine()
	if err := e.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if e.src.ram == nil {
		t.Fatal("expected RAM preload for a 2s file")
	}
	if got := e.Loaded(); got != path {
		t.Fatalf("Loaded=%q", got)
	}

	// Hold-to-preview from 1.0s: playing, cursor at the 1s frame.
	e.PreviewFrom(1.0)
	if !fake.IsPlaying() {
		t.Fatal("PreviewFrom should start playback")
	}
	if got := e.src.Pos(); got != deviceRate {
		t.Fatalf("cursor after PreviewFrom = %d want %d", got, deviceRate)
	}
	// Simulate 0.25s of playback: the playhead advances.
	fake.pump(t, deviceRate/4)
	if got := e.src.Pos(); got != deviceRate+deviceRate/4 {
		t.Fatalf("cursor after pump = %d want %d", got, deviceRate+deviceRate/4)
	}
	// Release: stop + snap back to where the preview started (1.0s).
	e.PreviewRelease(-1)
	if fake.IsPlaying() {
		t.Fatal("PreviewRelease should pause")
	}
	if got := e.src.Pos(); got != deviceRate {
		t.Fatalf("cursor after PreviewRelease = %d want %d (jump-back failed)", got, deviceRate)
	}

	// SeekTo is sample-accurate via the cursor.
	e.SeekTo(1.5, true)
	if got := e.src.Pos(); got != deviceRate+deviceRate/2 {
		t.Fatalf("cursor after SeekTo(1.5) = %d want %d", got, deviceRate+deviceRate/2)
	}

	// Position reports audible = cursor - buffered.
	fake.buffered = 4800 * deviceBytes * deviceChannels // 0.1s still in the device buffer
	cur, total, ok := e.Position()
	if !ok {
		t.Fatal("Position !ok")
	}
	if want := 1.5 - 0.1; cur < want-1e-3 || cur > want+1e-3 {
		t.Fatalf("audible cur = %v want ~%v", cur, want)
	}
	if total < 1.999 || total > 2.001 {
		t.Fatalf("total = %v want ~2", total)
	}

	e.Stop()
	if e.Loaded() != "" {
		t.Fatal("Stop should clear the track")
	}
}

func TestEngineStreamFallback(t *testing.T) {
	// Force streaming by shrinking nothing — instead verify a stream source seeks + reads a WAV.
	path := filepath.Join(t.TempDir(), "s.wav")
	writeWAV(t, path, deviceRate, deviceChannels, 16, false)
	dec, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s := newStreamSource(dec)
	defer s.Close()
	if err := s.SeekTo(deviceRate/2, true); err != nil {
		t.Fatalf("seek: %v", err)
	}
	buf := make([]byte, 100*deviceBytes*deviceChannels)
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if n == 0 {
		t.Fatal("stream read returned 0")
	}
	if got := s.Pos(); got < deviceRate/2 {
		t.Fatalf("stream cursor = %d", got)
	}
}
