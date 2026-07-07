package playerwin

import (
	"fmt"
	"image"
	"image/color"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"rave.page/mate/internal/giokit"
)

// frame lays out one window frame: video rect on top, then seek row, tool strip,
// status strip (event-loop goroutine). Also drives the mpv lifecycle: lazy launch,
// failed-embed cleanup, and child-window positioning.
func (w *Window) frame(gtx layout.Context) {
	w.mu.Lock()
	if dh := w.deadHost; dh != nil { // failed embed: destroy on this (creation) thread
		w.deadHost = nil
		w.mu.Unlock()
		dh.Destroy()
		w.mu.Lock()
	}
	w.mu.Unlock()
	w.ensureOpen()
	w.mu.Lock()
	if !w.opened { // waiting for the HWND - make sure the deadline fallback gets a frame
		gtx.Execute(op.InvalidateCmd{At: w.created.Add(hwndDeadline + 50*time.Millisecond)})
	}
	w.mu.Unlock()

	th, reg := w.th, w.kw.Reg
	size := gtx.Constraints.Max
	seekH := gtx.Dp(th.ControlHeight) + 2*gtx.Dp(th.PadY)
	toolH := gtx.Dp(th.ToolStripH)
	statH := gtx.Dp(th.StatusStripH)
	w.mu.Lock()
	note := w.note
	cur, total, paused := w.cur, w.total, w.paused
	waveOpen := w.waveOpen
	w.mu.Unlock()
	waveH := 0
	if waveOpen {
		waveH = gtx.Dp(waveStripH)
	}
	videoH := size.Y - waveH - seekH - toolH - statH
	if videoH < 0 {
		videoH = 0
	}

	// Video rect (mpv child HWND floats above this fill; the note shows through when
	// there is no embedded video).
	paint.FillShape(gtx.Ops, color.NRGBA{A: 0xff}, clip.Rect(image.Rect(0, 0, size.X, videoH)).Op())
	if note != "" && videoH > 0 {
		m := op.Record(gtx.Ops)
		ngtx := gtx
		ngtx.Constraints = layout.Constraints{Max: size}
		nd := giokit.DrawText(ngtx, th, th.Sans, th.TextSize, th.Muted, 2, note)
		call := m.Stop()
		tr := op.Offset(image.Pt((size.X-nd.Size.X)/2, (videoH-nd.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		tr.Pop()
	}
	w.positionHost(size.X, videoH)

	// Waveform strip (collapsible) under the video rect.
	if waveOpen {
		section(gtx, reg, image.Pt(0, videoH), image.Pt(size.X, waveH), func(gtx layout.Context) {
			w.waveStrip(gtx, cur, total, paused)
		})
	}

	// Seek row.
	section(gtx, reg, image.Pt(0, videoH+waveH), image.Pt(size.X, seekH), func(gtx layout.Context) {
		w.seekRow(gtx, cur, total)
	})

	// Transport tool strip.
	w.playBtn.Label = "Pause"
	if paused {
		w.playBtn.Label = "Play"
	}
	children := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions { return w.playBtn.Layout(gtx, th, reg) },
		func(gtx layout.Context) layout.Dimensions { return w.backBtn.Layout(gtx, th, reg) },
		func(gtx layout.Context) layout.Dimensions { return w.fwdBtn.Layout(gtx, th, reg) },
		giokit.Sep(th),
		giokit.Caption(th, "VOL"),
		func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = gtx.Dp(90)
			return w.vol.Layout(gtx, th, reg)
		},
		giokit.Sep(th),
		func(gtx layout.Context) layout.Dimensions { return w.waveBtn.Layout(gtx, th, reg) },
		giokit.Sep(th),
		func(gtx layout.Context) layout.Dimensions { return w.inBtn.Layout(gtx, th, reg) },
		func(gtx layout.Context) layout.Dimensions { return w.outBtn.Layout(gtx, th, reg) },
		func(gtx layout.Context) layout.Dimensions { return w.clrBtn.Layout(gtx, th, reg) },
		giokit.Caption(th, w.trim.String()),
	}
	if w.cfg.OnExportCut != nil {
		children = append(children, giokit.Sep(th),
			func(gtx layout.Context) layout.Dimensions { return w.expBtn.Layout(gtx, th, reg) })
	}
	section(gtx, reg, image.Pt(0, videoH+waveH+seekH), image.Pt(size.X, toolH), func(gtx layout.Context) {
		giokit.ToolStrip(gtx, th, reg, children...)
	})

	// Status strip: file title left, playback state right.
	state := "playing"
	switch {
	case note != "":
		state = "popout"
	case paused:
		state = "paused"
	}
	section(gtx, reg, image.Pt(0, videoH+waveH+seekH+toolH), image.Pt(size.X, statH), func(gtx layout.Context) {
		giokit.StatusStrip(gtx, th, giokit.Caption(th, w.cfg.Title), nil, giokit.Caption(th, state))
	})
}

// waveStripH is the collapsible waveform strip height (dp).
const waveStripH = 56

// waveStrip renders the waveform strip (loop goroutine): shared peaks data, playhead
// smoothed by the velocity-PLL deck clock (the same smoothness contract as the overlay
// outputs), press/drag scrub → seek. Captions cover the loading/error states.
func (w *Window) waveStrip(gtx layout.Context, cur, total float64, paused bool) {
	th, reg := w.th, w.kw.Reg
	w.mu.Lock()
	peaks, loading, errS := w.peaks, w.peaksLoading, w.peaksErr
	w.mu.Unlock()

	played := float32(-1)
	if total > 0 {
		played = float32(w.clk.Tick(cur, !paused, total, gtx.Now) / total)
		if !paused {
			gtx.Execute(op.InvalidateCmd{}) // animate the playhead between mpv ticks
		}
	}
	w.wave.Layout(gtx, th, reg, peaks, played)

	msg := ""
	switch {
	case loading:
		msg = "Analyzing waveform…"
	case errS != "":
		msg = errS
	case len(peaks) == 0:
		msg = "No waveform data"
	}
	if msg == "" {
		return
	}
	m := op.Record(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints = layout.Constraints{Max: gtx.Constraints.Max}
	cd := giokit.DrawText(cgtx, th, th.Sans, th.CaptionSize, th.Muted, 1, msg)
	call := m.Stop()
	tr := op.Offset(image.Pt((gtx.Constraints.Max.X-cd.Size.X)/2, (gtx.Constraints.Max.Y-cd.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	tr.Pop()
}

// section lays out fn translated to pos with exact size, keeping registry offsets in sync.
func section(gtx layout.Context, reg *giokit.Registry, pos, size image.Point, fn func(gtx layout.Context)) {
	tr := op.Offset(pos).Push(gtx.Ops)
	reg.PushOffset(pos)
	cgtx := gtx
	cgtx.Constraints = layout.Constraints{Max: size}
	fn(cgtx)
	reg.PopOffset()
	tr.Pop()
}

// seekRow lays out the seek slider with the time readout on the right.
func (w *Window) seekRow(gtx layout.Context, cur, total float64) {
	th, reg := w.th, w.kw.Reg
	pad := gtx.Dp(th.PadX)
	wpx, hpx := gtx.Constraints.Max.X, gtx.Constraints.Max.Y

	txt := clock(cur) + " / " + clock(total)
	m := op.Record(gtx.Ops)
	tg := gtx
	tg.Constraints = layout.Constraints{Max: gtx.Constraints.Max}
	td := giokit.DrawText(tg, th, th.Sans, th.CaptionSize, th.Muted, 1, txt)
	call := m.Stop()

	sw := wpx - td.Size.X - 3*pad
	if sw < 0 {
		sw = 0
	}
	if total > 0 && !w.seek.Dragging() {
		w.seek.Float.Value = float32(cur / total)
	}
	spos := image.Pt(pad, (hpx-gtx.Dp(th.ControlHeight))/2)
	str := op.Offset(spos).Push(gtx.Ops)
	reg.PushOffset(spos)
	sg := gtx
	sg.Constraints = layout.Constraints{Max: image.Pt(sw, hpx)}
	w.seek.Layout(sg, th, reg)
	reg.PopOffset()
	str.Pop()

	ttr := op.Offset(image.Pt(wpx-td.Size.X-pad, (hpx-td.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	ttr.Pop()
}

// positionHost keeps the mpv child window aligned to the video rect (loop thread).
func (w *Window) positionHost(wpx, hpx int) {
	w.mu.Lock()
	host := w.host
	w.mu.Unlock()
	if host == nil {
		return
	}
	if hpx < 2 || wpx < 2 {
		if w.shown {
			host.Hide()
			w.shown = false
		}
		return
	}
	r := [4]int{0, 0, wpx, hpx}
	if r != w.lastRect {
		host.Move(0, 0, wpx, hpx)
		w.lastRect = r
	}
	if !w.shown {
		host.Show()
		w.shown = true
	}
}

// clock formats seconds as m:ss (h:mm:ss past an hour).
func clock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(sec)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
