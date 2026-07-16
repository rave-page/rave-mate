package mocapnode

// Node loop + FileSource end-to-end tests (synthetic fixtures written to a temp dir).

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"rave.page/mate/internal/mocappanel"
)

func writePNG(t *testing.T, img image.Image) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "frame.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNodeFileSourceEndToEnd(t *testing.T) {
	path := writePNG(t, goldenPanel())
	var pkts []Packet
	var dump bytes.Buffer
	n := New(Config{
		Source:   &FileSource{Path: path, FPS: 500, Count: 5},
		OnPacket: func(p Packet) { pkts = append(pkts, p) },
		Dump:     &dump,
	})
	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(pkts) != 5 {
		t.Fatalf("packets=%d want 5", len(pkts))
	}
	wantH, wantD := goldenFields()
	for i, p := range pkts {
		if p.Header != wantH {
			t.Fatalf("packet %d header mismatch:\n got %+v\nwant %+v", i, p.Header, wantH)
		}
		if !reflect.DeepEqual(p.Dancers, wantD) {
			t.Fatalf("packet %d dancers mismatch", i)
		}
	}

	h := n.Health()
	if !h.Locked || !h.Identity || !h.Live {
		t.Errorf("health lock state: %+v", h)
	}
	if h.Decoded != 5 || h.Failed != 0 || h.SuccessRate != 1 {
		t.Errorf("health counters: %+v", h)
	}
	if h.LastCounter != wantH.FrameCounter {
		t.Errorf("LastCounter=%d want %d", h.LastCounter, wantH.FrameCounter)
	}

	// JSON-lines dump: one parseable line per packet, header fields intact.
	lines := bytes.Split(bytes.TrimSpace(dump.Bytes()), []byte("\n"))
	if len(lines) != 5 {
		t.Fatalf("dump lines=%d want 5", len(lines))
	}
	var back Packet
	if err := json.Unmarshal(lines[0], &back); err != nil {
		t.Fatalf("dump line not JSON: %v", err)
	}
	if back.Header != wantH || len(back.Dancers) != len(wantD) {
		t.Errorf("dump round-trip mismatch: %+v", back.Header)
	}
}

func TestNodeRawRGBSource(t *testing.T) {
	// imageToFrame's Pix is exactly the tightly packed RGB24 wire format.
	fr := imageToFrame(goldenPanel())
	p := filepath.Join(t.TempDir(), "frame.rgb")
	if err := os.WriteFile(p, fr.Pix, 0o644); err != nil {
		t.Fatal(err)
	}
	var pkts []Packet
	n := New(Config{
		Source:   &FileSource{Path: p, W: 1920, H: 1080, FPS: 500, Count: 2},
		OnPacket: func(pk Packet) { pkts = append(pkts, pk) },
	})
	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(pkts) != 2 {
		t.Fatalf("packets=%d want 2", len(pkts))
	}
	wantH, wantD := goldenFields()
	if pkts[0].Header != wantH || !reflect.DeepEqual(pkts[0].Dancers, wantD) {
		t.Fatal("raw .rgb decode diverges from golden")
	}
}

func TestNodeNoLockNoPanic(t *testing.T) {
	path := writePNG(t, image.NewNRGBA(image.Rect(0, 0, 1920, 1080))) // all black
	var pkts []Packet
	n := New(Config{
		Source:   &FileSource{Path: path, FPS: 500, Count: 3},
		OnPacket: func(p Packet) { pkts = append(pkts, p) },
	})
	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(pkts) != 0 {
		t.Fatalf("packets=%d want 0", len(pkts))
	}
	h := n.Health()
	if h.Locked || h.Live || h.Decoded != 0 || h.Failed != 3 {
		t.Errorf("health: %+v", h)
	}
}

func TestFileSourceErrors(t *testing.T) {
	if err := (&FileSource{Path: "nope.tiff"}).Frames(context.Background(), nil); err == nil {
		t.Error("unsupported extension accepted")
	}
	if err := (&FileSource{Path: "missing.png"}).Frames(context.Background(), nil); err == nil {
		t.Error("missing file accepted")
	}
	p := filepath.Join(t.TempDir(), "bad.rgb")
	if err := os.WriteFile(p, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&FileSource{Path: p, W: 1920, H: 1080}).Frames(context.Background(), nil); err == nil {
		t.Error(".rgb with a partial frame accepted")
	}
	if err := (&FileSource{Path: p}).Frames(context.Background(), nil); err == nil {
		t.Error(".rgb without W/H accepted")
	}
}

// goldenFields returns the golden truth in decoded form (matches mocappanel.GoldenFrame).
func goldenFields() (mocappanel.Header, []mocappanel.Dancer) { return mocappanel.GoldenFrame() }
