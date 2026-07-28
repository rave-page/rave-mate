package framedebug

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func solid(w, h int, r, g, b byte) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, 255
	}
	return img
}

// The hash must be a pure function of the pixels: the whole oracle rests on "same value => the
// picture did not change", so a per-run seed or map iteration order leaking in would make every
// stall verdict noise.
func TestHashIsDeterministicAndSensitive(t *testing.T) {
	a, b := solid(64, 36, 10, 20, 30), solid(64, 36, 10, 20, 30)
	if Hash(a.Pix) != Hash(b.Pix) {
		t.Fatal("identical pictures hashed differently")
	}
	c := solid(64, 36, 10, 20, 31)
	if Hash(a.Pix) == Hash(c.Pix) {
		t.Fatal("a changed picture hashed the same")
	}
	// A one-pixel change at a sampled offset must register. Offset 0 is always sampled.
	d := solid(64, 36, 10, 20, 30)
	d.Pix[0] = 99
	if Hash(a.Pix) == Hash(d.Pix) {
		t.Fatal("single sampled byte change did not move the hash")
	}
}

// The bug this package exists for: frames keep arriving while the picture is frozen. Frames must
// climb and StalledMs must grow, because a "frames are flowing" counter cannot tell them apart.
func TestFrozenPictureStalesWhileFramesFlow(t *testing.T) {
	r := For("test-frozen")
	img := solid(32, 32, 1, 2, 3)
	r.Frame(img)
	first := r.Stats()
	if first.Frames != 1 || first.Changes != 0 {
		t.Fatalf("first frame: frames=%d changes=%d, want 1/0", first.Frames, first.Changes)
	}
	time.Sleep(25 * time.Millisecond)
	for range 20 {
		r.Frame(img) // same content, over and over: exactly the field failure
	}
	s := r.Stats()
	if s.Frames != 21 {
		t.Fatalf("frames=%d, want 21", s.Frames)
	}
	if s.Changes != 0 {
		t.Fatalf("changes=%d on a frozen picture, want 0", s.Changes)
	}
	if s.StalledMs < 20 {
		t.Fatalf("stalledMs=%d after ~25ms frozen, want >= 20 - a frozen stage must AGE", s.StalledMs)
	}

	// ...and a real content change must reset it.
	r.Frame(solid(32, 32, 9, 9, 9))
	if s2 := r.Stats(); s2.Changes != 1 || s2.StalledMs > 20 {
		t.Fatalf("after a change: changes=%d stalledMs=%d, want 1 and ~0", s2.Changes, s2.StalledMs)
	}
}

func TestArmCapturesPNGsThenDisarms(t *testing.T) {
	SetDir(t.TempDir())
	r := For("test-arm")
	if err := r.Arm(2, 1, image.Rectangle{}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		r.Frame(solid(16, 16, 7, 7, 7))
	}
	shots := r.Shots()
	if len(shots) != 2 {
		t.Fatalf("got %d shots, want exactly 2 - arming must not keep capturing", len(shots))
	}
	for _, s := range shots {
		st, err := os.Stat(s.Path)
		if err != nil || st.Size() == 0 {
			t.Fatalf("shot %s not written: %v", s.Path, err)
		}
		if filepath.Dir(s.Path) != Dir() {
			t.Fatalf("shot landed in %s, want %s", filepath.Dir(s.Path), Dir())
		}
	}
	if got := r.Stats().Armed; got != 0 {
		t.Fatalf("armed=%d after the capture completed, want 0", got)
	}
}

func TestArmRefusesOutOfRangeCounts(t *testing.T) {
	SetDir(t.TempDir())
	r := For("test-arm-bounds")
	for _, n := range []int{0, -1, maxShots + 1} {
		if err := r.Arm(n, 0, image.Rectangle{}); err == nil {
			t.Fatalf("Arm(%d) accepted; a silently trimmed capture reads as a complete one", n)
		}
	}
}

// A crop must be full-resolution: it exists to read small in-frame text (an OS clock in a captured
// desktop), which a downsample destroys.
func TestCropIsFullResolution(t *testing.T) {
	dir := t.TempDir()
	src := solid(200, 100, 5, 5, 5)
	path := filepath.Join(dir, "crop.png")
	if err := writePNG(path, src, 4, image.Rect(10, 10, 50, 30)); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 40 || cfg.Height != 20 {
		t.Fatalf("crop wrote %dx%d, want 40x20 (scale must not apply to a crop)", cfg.Width, cfg.Height)
	}
}

func TestSnapshotCoversRegisteredStages(t *testing.T) {
	For("test-snap-a").Frame(solid(8, 8, 1, 1, 1))
	For("test-snap-b").Frame(solid(8, 8, 2, 2, 2))
	snap := Snapshot()
	for _, want := range []string{"test-snap-a", "test-snap-b"} {
		if _, ok := snap[want]; !ok {
			t.Fatalf("stage %q missing from Snapshot()", want)
		}
	}
}

// The distinction the boolean hash could not make: a picture that CHANGES on nearly every frame but
// only in a tiny region is a static source with a live element, not moving footage. This is the
// field shape - a 4K desktop capture with a ticking tray clock read "changed" ~3x a second and still
// looked like a still image in Resolume.
func TestStaticSourceWithALiveElementIsNotCalledMoving(t *testing.T) {
	r := For("test-clock")
	img := solid(200, 100, 30, 30, 30)
	r.Frame(img)
	for i := range 10 {
		// Perturb ONE sampled byte: the smallest possible "it changed".
		img.Pix[0] = byte(100 + i)
		r.Frame(img)
	}
	s := r.Stats()
	if s.Changes < 5 {
		t.Fatalf("changes=%d, want the tiny edits to register", s.Changes)
	}
	if s.StalledMs > 50 {
		t.Fatalf("stalledMs=%d: a changing picture must not read as frozen", s.StalledMs)
	}
	if !s.Static() {
		t.Fatalf("Static()=false at peak %.4f%%: a one-byte delta is not motion", s.PeakFrac*100)
	}

	// ...and wholesale motion must NOT be called static, or the verdict is useless.
	m := For("test-motion")
	for i := range 6 {
		m.Frame(solid(200, 100, byte(i*40), byte(i*30), byte(i*20)))
	}
	if ms := m.Stats(); ms.Static() {
		t.Fatalf("Static()=true for full-frame changes (peak %.2f%%)", ms.PeakFrac*100)
	}
}

func TestDiffFracMeasuresProportion(t *testing.T) {
	a := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	b := append([]byte(nil), a...)
	if f := DiffFrac(a, b); f != 0 {
		t.Fatalf("identical samples: %v, want 0", f)
	}
	b[0], b[1] = 99, 99
	if f := DiffFrac(a, b); f != 0.2 {
		t.Fatalf("2 of 10 differing: %v, want 0.2", f)
	}
}
