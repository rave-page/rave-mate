package testcard

import "image"

// Seven-segment digit renderer: rectangles only, no font dependency (stdlib has no rasterizer and
// this repo does not take a dep for a diagnostic). Reads fine in Resolume at any card size.

// Segment bit order: a(top) b(tr) c(br) d(bottom) e(bl) f(tl) g(mid).
var segDigits = map[byte]byte{
	'0': 0b1111110, '1': 0b0110000, '2': 0b1101101, '3': 0b1111001, '4': 0b0110011,
	'5': 0b1011011, '6': 0b1011111, '7': 0b1110000, '8': 0b1111111, '9': 0b1111011,
}

// drawDigits renders s (digits and ':') across box. Each glyph gets an equal slot; ':' takes a
// half slot of two dots.
func drawDigits(img *image.NRGBA, box image.Rectangle, s string) {
	if len(s) == 0 || box.Empty() {
		return
	}
	slot := box.Dx() / len(s)
	for i := range len(s) {
		g := image.Rect(box.Min.X+i*slot, box.Min.Y, box.Min.X+(i+1)*slot, box.Max.Y)
		// Inset for spacing between glyphs.
		g.Min.X += slot / 8
		g.Max.X -= slot / 8
		drawGlyph(img, g, s[i])
	}
}

func drawGlyph(img *image.NRGBA, g image.Rectangle, ch byte) {
	if ch == ':' {
		d := g.Dy() / 5
		cx := (g.Min.X + g.Max.X) / 2
		fillRect(img, image.Rect(cx-d/2, g.Min.Y+d, cx+d/2, g.Min.Y+2*d), 255, 255, 255)
		fillRect(img, image.Rect(cx-d/2, g.Max.Y-2*d, cx+d/2, g.Max.Y-d), 255, 255, 255)
		return
	}
	segs, ok := segDigits[ch]
	if !ok {
		return
	}
	t := max(2, g.Dy()/8) // segment thickness
	mid := g.Min.Y + g.Dy()/2
	seg := [7]image.Rectangle{
		image.Rect(g.Min.X, g.Min.Y, g.Max.X, g.Min.Y+t), // a
		image.Rect(g.Max.X-t, g.Min.Y, g.Max.X, mid),     // b
		image.Rect(g.Max.X-t, mid, g.Max.X, g.Max.Y),     // c
		image.Rect(g.Min.X, g.Max.Y-t, g.Max.X, g.Max.Y), // d
		image.Rect(g.Min.X, mid, g.Min.X+t, g.Max.Y),     // e
		image.Rect(g.Min.X, g.Min.Y, g.Min.X+t, mid),     // f
		image.Rect(g.Min.X, mid-t/2, g.Max.X, mid-t/2+t), // g
	}
	for i, r := range seg {
		if segs&(1<<(6-i)) != 0 {
			fillRect(img, r, 255, 255, 255)
		}
	}
}
