package flipbook

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTierFor(t *testing.T) {
	cases := map[int]Tier{
		4:  {Frames: 4, Grid: 2, FrameRes: 512},
		16: {Frames: 16, Grid: 4, FrameRes: 256},
		64: {Frames: 64, Grid: 8, FrameRes: 128},
	}
	for n, want := range cases {
		got, err := TierFor(n)
		if err != nil || got != want {
			t.Errorf("TierFor(%d) = %+v, %v; want %+v", n, got, err, want)
		}
	}
	for _, bad := range []int{0, 1, 8, 32, 65, 100} {
		if _, err := TierFor(bad); err == nil {
			t.Errorf("TierFor(%d): expected error", bad)
		}
	}
	// Every tier fills exactly the 1024² sheet.
	for _, tier := range Tiers() {
		if tier.Grid*tier.FrameRes != SheetSize {
			t.Errorf("tier %+v does not tile to %d px", tier, SheetSize)
		}
		if tier.Grid*tier.Grid != tier.Frames {
			t.Errorf("tier %+v grid² != frames", tier)
		}
	}
}

func TestOutFileName(t *testing.T) {
	cases := []struct {
		name   string
		frames int
		fps    float64
		want   string
	}{
		{"MyEmoji", 14, 20, "MyEmoji_14frames_20fps.png"},
		{"wave", 16, 12.5, "wave_16frames_12.5fps.png"},
		{"  spaced  ", 4, 8, "spaced_4frames_8fps.png"},
		{"bad/name:x", 64, 30, "bad-name-x_64frames_30fps.png"},
		{"", 4, 10, "emoji_4frames_10fps.png"},
	}
	for _, c := range cases {
		if got := OutFileName(c.name, c.frames, c.fps); got != c.want {
			t.Errorf("OutFileName(%q,%d,%g) = %q; want %q", c.name, c.frames, c.fps, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	base := Options{Input: "in.mp4", OutName: "e", Frames: 16, FPS: 20, OutDir: "out"}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	bad := []Options{
		{OutName: "e", Frames: 16, FPS: 20, OutDir: "out"},                              // no input
		{Input: "in.mp4", Frames: 5, FPS: 20, OutDir: "out"},                            // bad tier
		{Input: "in.mp4", Frames: 16, FPS: 0, OutDir: "out"},                            // bad fps
		{Input: "in.mp4", Frames: 16, FPS: 200, OutDir: "out"},                          // fps too high
		{Input: "in.mp4", Frames: 16, FPS: 20, OutDir: "out", TrimStart: -1},            // neg trim
		{Input: "in.mp4", Frames: 16, FPS: 20, OutDir: "out", TrimStart: 5, TrimEnd: 5}, // end<=start
		{Input: "in.mp4", Frames: 16, FPS: 20},                                          // no out dir
		{Input: "in.mp4", Frames: 16, FPS: 20, OutDir: "out", Crop: &Rect{W: 0, H: 5}},  // bad crop
	}
	for i, o := range bad {
		if err := o.Validate(); err == nil {
			t.Errorf("bad options[%d] accepted: %+v", i, o)
		}
	}
}

func TestFramesToExtract(t *testing.T) {
	cases := []struct {
		n        int
		pingPong bool
		want     int
	}{
		{4, false, 4}, {16, false, 16}, {64, false, 64},
		{4, true, 3}, {16, true, 9}, {64, true, 33},
	}
	for _, c := range cases {
		if got := framesToExtract(c.n, c.pingPong); got != c.want {
			t.Errorf("framesToExtract(%d,%v) = %d; want %d", c.n, c.pingPong, got, c.want)
		}
	}
}

func TestFrameSequence(t *testing.T) {
	// Normal: identity, length n.
	if got := frameSequence(4, false); !eqInts(got, []int{0, 1, 2, 3}) {
		t.Errorf("normal seq = %v", got)
	}
	// Ping-pong length == n, mirrors without repeating endpoints, max index == n/2.
	for _, n := range []int{4, 16, 64} {
		seq := frameSequence(n, true)
		if len(seq) != n {
			t.Fatalf("ping-pong(%d) length = %d; want %d", n, len(seq), n)
		}
		max := 0
		for _, v := range seq {
			if v > max {
				max = v
			}
		}
		if max != n/2 {
			t.Errorf("ping-pong(%d) max index = %d; want %d", n, max, n/2)
		}
	}
	if got := frameSequence(4, true); !eqInts(got, []int{0, 1, 2, 1}) {
		t.Errorf("ping-pong(4) = %v; want [0 1 2 1]", got)
	}
}

func TestVideoFilters(t *testing.T) {
	got := videoFilters(20, 256, nil)
	want := "fps=20,scale=256:256:force_original_aspect_ratio=decrease:flags=lanczos,format=rgba,pad=256:256:(ow-iw)/2:(oh-ih)/2:color=black@0.0"
	if got != want {
		t.Errorf("filters = %q\nwant %q", got, want)
	}
	gotCrop := videoFilters(12.5, 128, &Rect{X: 10, Y: 20, W: 100, H: 50})
	if want := "crop=100:50:10:20"; !contains(gotCrop, want) {
		t.Errorf("crop filter missing %q in %q", want, gotCrop)
	}
	if !contains(gotCrop, "fps=12.5") {
		t.Errorf("fps token missing in %q", gotCrop)
	}
}

func TestFFmpegArgs(t *testing.T) {
	o := Options{Input: "src.mp4", Frames: 16, FPS: 20, TrimStart: 2, OutDir: "out"}
	args := ffmpegArgs(o, 256, 16, "tmp/f_%05d.png")
	// -ss must precede -i; -frames:v must equal extract.
	si, ii := indexOf(args, "-ss"), indexOf(args, "-i")
	if si < 0 || ii < 0 || si > ii {
		t.Errorf("expected -ss before -i, args=%v", args)
	}
	if fi := indexOf(args, "-frames:v"); fi < 0 || args[fi+1] != "16" {
		t.Errorf("expected -frames:v 16, args=%v", args)
	}
}

func TestAssemble(t *testing.T) {
	tier := Tier{Frames: 4, Grid: 2, FrameRes: 8}
	// Four solid 8×8 frames, one per primary corner color.
	cols := []color.RGBA{
		{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}, {R: 255, G: 255, A: 255},
	}
	frames := make([]image.Image, 4)
	for i, c := range cols {
		frames[i] = solid(tier.FrameRes, c)
	}
	// Override SheetSize math: assemble uses package SheetSize; build a private sheet via the
	// same layout by checking cell centers map to the right color (grid 2, res 8 → 16px sheet
	// region used; rest transparent).
	sheet := assemble(frames, tier, frameSequence(4, false))
	// Cell (col,row) center pixel == cols[row*grid+col].
	at := func(col, row int) color.Color {
		x := col*tier.FrameRes + tier.FrameRes/2
		y := row*tier.FrameRes + tier.FrameRes/2
		return sheet.At(x, y)
	}
	check := func(col, row, want int) {
		got := at(col, row)
		gr, gg, gb, _ := got.RGBA()
		w := cols[want]
		if uint8(gr>>8) != w.R || uint8(gg>>8) != w.G || uint8(gb>>8) != w.B {
			t.Errorf("cell (%d,%d) = %v; want %v", col, row, got, w)
		}
	}
	check(0, 0, 0) // top-left
	check(1, 0, 1) // top-right
	check(0, 1, 2) // bottom-left
	check(1, 1, 3) // bottom-right
}

func TestGenerateRequiresFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH; skipping integration generate")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	// Synthesize a 1s test source via ffmpeg's lavfi testsrc.
	mk := exec.Command(ffmpeg, "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=30:duration=1", src)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Skipf("could not synthesize test source: %v: %s", err, out)
	}
	out, err := Generate(ffmpeg, Options{Input: src, OutName: "t", Frames: 16, FPS: 12, OutDir: dir})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode sheet: %v", err)
	}
	if b := img.Bounds(); b.Dx() != SheetSize || b.Dy() != SheetSize {
		t.Errorf("sheet size = %dx%d; want %dx%d", b.Dx(), b.Dy(), SheetSize, SheetSize)
	}
}

// ── test helpers ─────────────────────────────────────────────────────────────

func solid(n int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool { return indexOfStr(s, sub) >= 0 }

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}
