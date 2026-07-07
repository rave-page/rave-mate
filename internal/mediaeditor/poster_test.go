package mediaeditor

import (
	"bytes"
	"image/color"
	"testing"
)

func TestRenderBoundsMatch(t *testing.T) {
	p := Poster{
		Width:      1080,
		Height:     1350,
		Background: color.NRGBA{R: 0x0a, G: 0x0a, B: 0x0a, A: 0xff},
		Title:      "Friday Night Techno",
		Lines:      []string{"DJ Alpha", "DJ Beta"},
	}
	img, err := p.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if img.Bounds().Dx() != p.Width {
		t.Errorf("width: got %d, want %d", img.Bounds().Dx(), p.Width)
	}
	if img.Bounds().Dy() != p.Height {
		t.Errorf("height: got %d, want %d", img.Bounds().Dy(), p.Height)
	}
}

func TestEncodeProducesPNG(t *testing.T) {
	p := Poster{
		Width:      640,
		Height:     480,
		Background: color.NRGBA{R: 0x14, G: 0x14, B: 0x17, A: 0xff},
		Title:      "Test",
	}
	img, err := p.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var buf bytes.Buffer
	if err := Encode(img, &buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("Encode produced empty output")
	}
	// PNG magic: \x89PNG\r\n\x1a\n
	pngSig := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if !bytes.HasPrefix(buf.Bytes(), pngSig) {
		t.Errorf("output does not start with PNG signature, got: %x", buf.Bytes()[:8])
	}
}

func TestWrapText_Short(t *testing.T) {
	face, err := loadFace(orbitronBoldTTF, 20)
	if err != nil {
		t.Fatalf("loadFace: %v", err)
	}
	defer face.Close()

	lines := wrapText(face, "Hello World", 10000)
	if len(lines) != 1 {
		t.Errorf("expected 1 line for a wide canvas, got %d", len(lines))
	}
	if lines[0] != "Hello World" {
		t.Errorf("unexpected line content: %q", lines[0])
	}
}

func TestWrapText_ForcesBreak(t *testing.T) {
	face, err := loadFace(orbitronBoldTTF, 20)
	if err != nil {
		t.Fatalf("loadFace: %v", err)
	}
	defer face.Close()

	// 1px wide forces one word per line.
	lines := wrapText(face, "Alpha Beta Gamma", 1)
	if len(lines) < 2 {
		t.Errorf("expected multiple lines for tiny width, got %d", len(lines))
	}
}

func TestWrapText_Empty(t *testing.T) {
	face, err := loadFace(orbitronRegularTTF, 14)
	if err != nil {
		t.Fatalf("loadFace: %v", err)
	}
	defer face.Close()

	lines := wrapText(face, "", 500)
	if lines != nil {
		t.Errorf("expected nil for empty input, got %v", lines)
	}
}

func TestTemplates(t *testing.T) {
	tpls := Templates()
	if len(tpls) < 2 {
		t.Fatalf("expected at least 2 templates, got %d", len(tpls))
	}
	for _, tp := range tpls {
		if tp.Width <= 0 || tp.Height <= 0 {
			t.Errorf("template %q has non-positive dimensions: %dx%d", tp.Name, tp.Width, tp.Height)
		}
	}
}

func TestPosterFromEvent(t *testing.T) {
	e := EventData{
		Title: "Club Night",
		Date:  "2025-01-01",
		DJs:   []string{"DJ A", "DJ B"},
	}
	p := PosterFromEvent(e, 1) // thumbnail preset
	if p.Title != e.Title {
		t.Errorf("title: got %q want %q", p.Title, e.Title)
	}
	if p.Width != 1280 || p.Height != 720 {
		t.Errorf("wrong size for thumbnail: %dx%d", p.Width, p.Height)
	}
	if len(p.Lines) < 2 {
		t.Errorf("expected date + DJs in Lines, got %v", p.Lines)
	}
}
