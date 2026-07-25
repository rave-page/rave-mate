package vroverlay

// paint is the raster target for vroverlay's renders (panels, menu, stats, and the
// small editor textures). Direct mode (dl == nil) draws into img with the stdlib exactly as
// the original code did — that Go path stays the golden reference. Record mode
// captures the same primitives as a zigvr display list executed natively by
// libravevr. Glyphs are Go-rasterized either way (x/image opentype, same faces):
// record mode copies each glyph's alpha mask into the list arena and Zig only
// composites — so both paths produce pixel-identical output (parity-tested).

import (
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"rave.page/mate/internal/zigvr"
)

type paint struct {
	img *image.NRGBA
	dl  *zigvr.List // nil = direct
	ok  bool        // record mode: false once a primitive couldn't be recorded (caps / foreign mask)
}

// paintInto renders fn via the Zig raster lib when linked (record ops, execute
// natively); any record/exec failure falls back to the direct Go path. img must be
// a zero-origin packed-stride canvas (all vroverlay canvases are). rz_vr_render is
// atomic (validates before writing), so the fallback redraw is safe even for renders
// that composite onto existing pixels instead of overwriting the whole canvas.
// Callers may stage several paintInto passes over one canvas (each flushes in order)
// to keep a non-recordable step - e.g. the wrist logo's CatmullRom blit - in Go.
func (r *Renderer) paintInto(img *image.NRGBA, fn func(*paint)) {
	if r.zig && img.Rect.Min == (image.Point{}) && img.Stride == 4*img.Rect.Dx() {
		if r.dl == nil {
			r.dl = zigvr.NewList()
		}
		r.dl.Reset()
		p := &paint{img: img, dl: r.dl, ok: true}
		fn(p)
		if p.ok && zigvr.Render(img.Pix, img.Rect.Dx(), img.Rect.Dy(), r.dl) == nil {
			r.zigOK.Add(1)
			return
		}
		r.zigFB.Add(1)
	}
	fn(&paint{img: img})
}

// clipRect canonicalizes + clips like image.Rect → draw clip.
func (p *paint) clipRect(x, y, w, h int) image.Rectangle {
	return image.Rect(x, y, x+w, y+h).Intersect(p.img.Bounds())
}

// fillSrc replicates draw.Draw(img, r, Uniform(c), Src): the stored bytes go through
// Go's premultiply→unpremultiply round trip (NOT a raw byte store — they differ for
// translucent colors).
func (p *paint) fillSrc(x, y, w, h int, c color.Color) {
	if p.dl == nil {
		draw.Draw(p.img, image.Rect(x, y, x+w, y+h), image.NewUniform(c), image.Point{}, draw.Src)
		return
	}
	rr := p.clipRect(x, y, w, h)
	if rr.Empty() {
		return
	}
	sr, sg, sb, sa := c.RGBA()
	if sa != 0 && sa != 0xffff { // image.NRGBA.SetRGBA64 unpremultiply
		sr = sr * 0xffff / sa
		sg = sg * 0xffff / sa
		sb = sb * 0xffff / sa
	}
	p.push(zigvr.Op{
		X: int32(rr.Min.X), Y: int32(rr.Min.Y), W: int32(rr.Dx()), H: int32(rr.Dy()),
		Kind: zigvr.KStore,
		SR:   uint16(sr >> 8), SG: uint16(sg >> 8), SB: uint16(sb >> 8), SA: uint16(sa >> 8),
	})
}

// fillOver replicates fillRect (draw.Draw over a Uniform, op Over).
func (p *paint) fillOver(x, y, w, h int, c color.Color) {
	if p.dl == nil {
		fillRect(p.img, x, y, w, h, c)
		return
	}
	rr := p.clipRect(x, y, w, h)
	if rr.Empty() {
		return
	}
	sr, sg, sb, sa := c.RGBA()
	p.push(zigvr.Op{
		X: int32(rr.Min.X), Y: int32(rr.Min.Y), W: int32(rr.Dx()), H: int32(rr.Dy()),
		Kind: zigvr.KOver,
		SR:   uint16(sr), SG: uint16(sg), SB: uint16(sb), SA: uint16(sa),
	})
}

// set replicates a per-pixel img.Set row (direct NRGBA byte store — menu separators).
func (p *paint) set(x, y, w, h int, c color.Color) {
	if p.dl == nil {
		for yy := y; yy < y+h; yy++ {
			for xx := x; xx < x+w; xx++ {
				p.img.Set(xx, yy, c)
			}
		}
		return
	}
	rr := p.clipRect(x, y, w, h)
	if rr.Empty() {
		return
	}
	nc := color.NRGBAModel.Convert(c).(color.NRGBA)
	p.push(zigvr.Op{
		X: int32(rr.Min.X), Y: int32(rr.Min.Y), W: int32(rr.Dx()), H: int32(rr.Dy()),
		Kind: zigvr.KStore,
		SR:   uint16(nc.R), SG: uint16(nc.G), SB: uint16(nc.B), SA: uint16(nc.A),
	})
}

// setN replicates an img.SetNRGBA run (raw byte store, no colour-model conversion) —
// the Bresenham/dot rasters in worldpath.go.
func (p *paint) setN(x, y, w, h int, c color.NRGBA) {
	if p.dl == nil {
		for yy := y; yy < y+h; yy++ {
			for xx := x; xx < x+w; xx++ {
				p.img.SetNRGBA(xx, yy, c)
			}
		}
		return
	}
	rr := p.clipRect(x, y, w, h)
	if rr.Empty() {
		return
	}
	p.push(zigvr.Op{
		X: int32(rr.Min.X), Y: int32(rr.Min.Y), W: int32(rr.Dx()), H: int32(rr.Dy()),
		Kind: zigvr.KStore,
		SR:   uint16(c.R), SG: uint16(c.G), SB: uint16(c.B), SA: uint16(c.A),
	})
}

// border replicates Border(img, col, width) as four clipped bands. The Go loop's rows
// and columns overlap in the corners; every write stores the same bytes, so the band
// decomposition (and its op order) is pixel-identical.
func (p *paint) border(width int, c color.Color) {
	if p.dl == nil {
		Border(p.img, c, width)
		return
	}
	w, h := p.img.Rect.Dx(), p.img.Rect.Dy()
	p.set(0, 0, w, width, c)       // top
	p.set(0, h-width, w, width, c) // bottom
	p.set(0, 0, width, h, c)       // left
	p.set(w-width, 0, width, h, c) // right
}

// text replicates font.Drawer.DrawString at (x, baseline) — same kern/advance walk,
// same faces. Record mode captures each glyph's alpha mask (clipped) into the arena.
func (p *paint) text(f font.Face, s string, x, baseline int, c color.Color) {
	if p.dl == nil {
		drawText(p.img, f, s, x, baseline, c)
		return
	}
	sr, sg, sb, sa := c.RGBA()
	dot := fixed.P(x, baseline)
	prevC := rune(-1)
	for _, ch := range s {
		if prevC >= 0 {
			dot.X += f.Kern(prevC, ch)
		}
		dr, maskIm, maskp, advance, _ := f.Glyph(dot, ch)
		if !dr.Empty() {
			am, isAlpha := maskIm.(*image.Alpha)
			if !isAlpha { // foreign face/mask type — can't record byte-exactly
				p.ok = false
				return
			}
			// Clip like draw.DrawMask's clip(): dst bounds ∩ mask bounds shifted to dr.
			rr := dr.Intersect(p.img.Bounds()).Intersect(am.Bounds().Add(dr.Min.Sub(maskp)))
			if !rr.Empty() {
				mp := maskp.Add(rr.Min.Sub(dr.Min))
				// Copy now: the face reuses the mask buffer on the next Glyph call.
				off, added := p.dl.AddMask(am.Pix[am.PixOffset(mp.X, mp.Y):], am.Stride, rr.Dx(), rr.Dy())
				if !added {
					p.ok = false
					return
				}
				p.push(zigvr.Op{
					X: int32(rr.Min.X), Y: int32(rr.Min.Y), W: int32(rr.Dx()), H: int32(rr.Dy()),
					Kind: zigvr.KGlyph,
					SR:   uint16(sr), SG: uint16(sg), SB: uint16(sb), SA: uint16(sa),
					MaskOff: off,
				})
			}
		}
		dot.X += advance
		prevC = ch
	}
}

func (p *paint) push(op zigvr.Op) {
	if !p.dl.Push(op) {
		p.ok = false
	}
}
