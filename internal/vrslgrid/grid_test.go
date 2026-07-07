package vrslgrid

import (
	"image/color"
	"testing"
)

// fakeReader is a test universe store.
type fakeReader map[uint16][512]byte

func (f fakeReader) Get(u uint16) ([512]byte, bool) {
	d, ok := f[u]
	return d, ok
}

// cellCenter returns the pixel at the centre of cell (cx,cy) - what the VRSL decoder samples.
func cellCenter(img interface {
	At(x, y int) color.Color
}, cx, cy int) color.RGBA {
	c := img.At(cx*CellPx+CellPx/2, cy*CellPx+CellPx/2)
	r, g, b, a := c.RGBA()
	return color.RGBA{byte(r >> 8), byte(g >> 8), byte(b >> 8), byte(a >> 8)}
}

func TestGeometryConstants(t *testing.T) {
	if CellsPerUni != 520 || DeadCells != 8 || GridWidthPx != 208 {
		t.Fatalf("geometry: cells=%d dead=%d width=%d", CellsPerUni, DeadCells, GridWidthPx)
	}
}

func TestMonoAddressing(t *testing.T) {
	var d [512]byte
	d[0] = 255  // ch0 → cell (0,0)
	d[13] = 128 // ch13 → cell (0,1)
	d[27] = 64  // ch27 → cell (1,2)
	d[511] = 9  // last ch → cell (511%13, 511/13) = (4,39)
	r := fakeReader{1: d}
	img := Render(r, []int{1}, ModeMono)
	if img.Bounds().Dx() != GridWidthPx || img.Bounds().Dy() != RowsPerUni*CellPx {
		t.Fatalf("bounds=%v", img.Bounds())
	}
	checks := []struct {
		cx, cy int
		v      byte
	}{{0, 0, 255}, {0, 1, 128}, {1, 2, 64}, {4, 39, 9}}
	for _, c := range checks {
		got := cellCenter(img, c.cx, c.cy)
		want := color.RGBA{c.v, c.v, c.v, 255}
		if got != want {
			t.Errorf("cell(%d,%d)=%v want %v", c.cx, c.cy, got, want)
		}
	}
	// Whole 16×16 block uniform (corner too, not just centre).
	if got := img.RGBAAt(0, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("block corner=%v", got)
	}
}

func TestDeadPaddingBoundary(t *testing.T) {
	var d [512]byte
	for i := range d {
		d[i] = 200 // light everything
	}
	r := fakeReader{1: d, 2: d}
	img := Render(r, []int{1, 2}, ModeMono)
	// ch511 (last real channel of u1) = cell (4,39) → lit.
	if got := cellCenter(img, 4, 39); got.R != 200 {
		t.Fatalf("ch511 cell=%v want lit", got)
	}
	// Cells 512..519 (x=5..12, y=39) are dead padding → black even with all channels lit.
	for x := 5; x < ColsPerUni; x++ {
		if got := cellCenter(img, x, 39); got != (color.RGBA{0, 0, 0, 255}) {
			t.Errorf("dead cell(%d,39)=%v want black", x, got)
		}
	}
	// Universe 2's ch0 lands at cell (0,40) - the row right after u1's block (520-cell stride).
	if got := cellCenter(img, 0, RowsPerUni); got.R != 200 {
		t.Fatalf("u2 ch0 cell=%v want lit", got)
	}
}

func TestRGB9Packing(t *testing.T) {
	mk := func(v byte) [512]byte {
		var d [512]byte
		d[0] = v
		return d
	}
	// u1→R, u4→G, u7→B all fold onto block 0 cell (0,0).
	r := fakeReader{1: mk(10), 4: mk(20), 7: mk(30), 2: mk(40), 5: mk(50), 8: mk(60)}
	img := Render(r, []int{1, 2, 3, 4, 5, 6, 7, 8, 9}, ModeRGB9)
	if img.Bounds().Dy() != 3*RowsPerUni*CellPx {
		t.Fatalf("rgb9 height=%d want %d", img.Bounds().Dy(), 3*RowsPerUni*CellPx)
	}
	if got := cellCenter(img, 0, 0); got != (color.RGBA{10, 20, 30, 255}) {
		t.Fatalf("block0 cell(0,0)=%v want {10 20 30 255}", got)
	}
	// u2/u5/u8 fold onto block 1 (second 40-row band).
	if got := cellCenter(img, 0, RowsPerUni); got != (color.RGBA{40, 50, 60, 255}) {
		t.Fatalf("block1 cell(0,0)=%v want {40 50 60 255}", got)
	}
	// Missing u3/u6/u9 → block 2 black.
	if got := cellCenter(img, 0, 2*RowsPerUni); got != (color.RGBA{0, 0, 0, 255}) {
		t.Fatalf("block2 cell(0,0)=%v want black", got)
	}
	// Dead padding in an RGB block is black too.
	if got := cellCenter(img, 12, RowsPerUni-1); got != (color.RGBA{0, 0, 0, 255}) {
		t.Fatalf("rgb dead cell=%v want black", got)
	}
}

func TestParseMode(t *testing.T) {
	if ParseMode("rgb9") != ModeRGB9 || ParseMode("RGB9") != ModeRGB9 {
		t.Fatal("rgb9 parse")
	}
	if ParseMode("") != ModeMono || ParseMode("mono") != ModeMono || ParseMode("junk") != ModeMono {
		t.Fatal("mono default")
	}
}
