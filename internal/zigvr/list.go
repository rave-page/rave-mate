// Package zigvr binds the ravevr VR-overlay raster lib (native/zigvr, C ABI static
// lib). Tag-gated like zignative: build with -tags zigvr after scripts/build-zig
// produced native/zigvr/zig-out/lib/libravevr.a. Untagged builds use the pure-Go
// stub (Available()=false) and the existing vroverlay Go raster path.
package zigvr

// Op kinds (mirror RzVrOp.kind in native/zigvr/include/ravevr.h).
const (
	KStore uint32 = 0 // fill rect with exact NRGBA bytes (SR..SA low bytes)
	KOver  uint32 = 1 // uniform source-over fill (SR..SA premult 16-bit)
	KGlyph uint32 = 2 // alpha-mask source-over; mask rows at MaskOff, stride = W
)

// Op is one display-list op. Layout mirrors RzVrOp exactly (32 bytes, align 4).
type Op struct {
	X, Y, W, H     int32
	Kind           uint32
	SR, SG, SB, SA uint16
	MaskOff        uint32
}

// Caps bound the per-render display list (cap + drop policy: exceeding either cap
// fails the record and the caller falls back to the Go raster path for that frame).
// A worst-case overlay render (640×480 panel / ~30-row menu) is ≤ ~3k ops and
// ≤ ~1 MiB of glyph mask bytes — the caps leave generous headroom without letting
// a bug grow unbounded.
const (
	maxOps  = 65536   // ops cap (~2 MiB of Op structs)
	maskCap = 4 << 20 // glyph mask arena cap (bytes)
)

// List is a reusable display list: ops + glyph-mask arena. Not concurrency-safe —
// one owner goroutine (the VR render goroutine) records + renders per frame.
type List struct {
	Ops  []Op
	Mask []byte
}

// NewList returns an empty list (backing arrays grow on first use, then recycle).
func NewList() *List { return &List{} }

// Reset clears the list for the next frame, keeping capacity.
func (l *List) Reset() {
	l.Ops = l.Ops[:0]
	l.Mask = l.Mask[:0]
}

// Push appends an op; false when the ops cap is hit (caller falls back to Go).
func (l *List) Push(op Op) bool {
	if len(l.Ops) >= maxOps {
		return false
	}
	l.Ops = append(l.Ops, op)
	return true
}

// AddMask copies h rows of w bytes (row starts src[row*stride]) into the arena and
// returns the arena offset; false when the arena cap is hit.
func (l *List) AddMask(src []byte, stride, w, h int) (uint32, bool) {
	n := w * h
	if n <= 0 || len(l.Mask)+n > maskCap {
		return 0, false
	}
	off := uint32(len(l.Mask))
	for row := 0; row < h; row++ {
		l.Mask = append(l.Mask, src[row*stride:row*stride+w]...)
	}
	return off, true
}
