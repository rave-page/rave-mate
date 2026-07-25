package giokit

import (
	"image"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"rave.page/mate/internal/zignative"
)

// Wave is a dense waveform strip: uint8 peak buckets (probe.peaks) rendered as
// per-pixel-column vector bars - clip.Path fills, no image round-trip per frame.
// Played part brand pink, rest dimmed fg; hairline center line; fg playhead.
// Press/drag scrubs (playhead previews at the pointer); OnSeek fires on release
// with the track fraction. Height comes from the max constraint.
type Wave struct {
	ID     string // registry label
	OnSeek func(frac float32)

	dragging bool
	dragFrac float32
}

// Dragging reports whether the user is scrubbing the strip.
func (w *Wave) Dragging() bool { return w.dragging }

// Layout renders peaks with the playhead at played (fraction of the track). reg may
// be nil. Zero-len peaks draws just the base strip (loading / no analysis).
func (w *Wave) Layout(gtx layout.Context, th *Theme, reg *Registry, peaks []byte, played float32) layout.Dimensions {
	size := gtx.Constraints.Max
	if size.X <= 0 || size.Y <= 0 {
		return layout.Dimensions{}
	}

	// Input: press/drag = scrub preview, release = seek.
	defer clip.Rect(image.Rectangle{Max: size}).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, w)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: w, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press, pointer.Drag:
			w.dragFrac = clampFrac(pe.Position.X / float32(size.X))
			w.dragging = true
		case pointer.Release:
			if w.dragging && w.OnSeek != nil {
				w.OnSeek(w.dragFrac)
			}
			w.dragging = false
		case pointer.Cancel:
			w.dragging = false
		}
	}
	if w.dragging {
		played = w.dragFrac
	}

	paint.FillShape(gtx.Ops, th.Surface, clip.Rect(image.Rectangle{Max: size}).Op())

	mid := size.Y / 2
	curX := int(played * float32(size.X))
	if curX < 0 {
		curX = 0
	}
	if curX > size.X {
		curX = size.X
	}
	if len(peaks) > 0 {
		cols := WaveColumns(peaks, size.X)
		half := float32(mid - 1)
		if half < 1 {
			half = 1
		}
		bars := func(x0, x1 int) clip.PathSpec { // one outline for all bars in [x0,x1)
			var p clip.Path
			p.Begin(gtx.Ops)
			for x := x0; x < x1; x++ {
				h := float32(cols[x]) / 255 * half
				if cols[x] > 0 && h < 1 {
					h = 1
				}
				if h == 0 {
					continue
				}
				fx := float32(x)
				p.MoveTo(f32.Pt(fx, float32(mid)-h))
				p.LineTo(f32.Pt(fx+1, float32(mid)-h))
				p.LineTo(f32.Pt(fx+1, float32(mid)+h))
				p.LineTo(f32.Pt(fx, float32(mid)+h))
				p.Close()
			}
			return p.End()
		}
		if curX > 0 {
			paint.FillShape(gtx.Ops, th.BrandBase, clip.Outline{Path: bars(0, curX)}.Op())
		}
		if curX < size.X {
			paint.FillShape(gtx.Ops, WithAlpha(th.Fg, 0x52), clip.Outline{Path: bars(curX, size.X)}.Op())
		}
	}

	// Center hairline + playhead.
	hair := gtx.Dp(th.Hairline)
	paint.FillShape(gtx.Ops, WithAlpha(th.Muted, 0x40), clip.Rect(image.Rect(0, mid, size.X, mid+hair)).Op())
	if played >= 0 {
		px := curX
		if px > size.X-hair {
			px = size.X - hair
		}
		paint.FillShape(gtx.Ops, th.Fg, clip.Rect(image.Rect(px, 0, px+hair, size.Y)).Op())
	}

	reg.Add("wave", w.ID, size, nil)
	return layout.Dimensions{Size: size}
}

// WaveColumns folds peak buckets into per-column maxima (column x covers buckets
// [x·n/cols, (x+1)·n/cols)). cols ≤ 0 or empty peaks → nil. Zig kernel when linked
// (-tags zigdsp; byte-exact, parity-tested), else the Go loop stays authoritative.
func WaveColumns(peaks []byte, cols int) []byte {
	n := len(peaks)
	if n == 0 || cols <= 0 {
		return nil
	}
	out := make([]byte, cols)
	if zignative.Available() {
		zignative.WaveColumns(peaks, cols, out)
		return out
	}
	waveColumnsGo(peaks, out)
	return out
}

// waveColumnsGo is the pure-Go fold (authoritative; parity reference).
func waveColumnsGo(peaks []byte, out []byte) {
	n, cols := len(peaks), len(out)
	for x := 0; x < cols; x++ {
		b0, b1 := x*n/cols, (x+1)*n/cols
		if b1 <= b0 {
			b1 = b0 + 1
		}
		if b1 > n {
			b1 = n
		}
		peak := byte(0)
		for b := b0; b < b1; b++ {
			if peaks[b] > peak {
				peak = peaks[b]
			}
		}
		out[x] = peak
	}
}

// clampFrac clamps to [0,1].
func clampFrac(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
