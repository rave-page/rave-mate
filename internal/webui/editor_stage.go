package webui

import (
	"strconv"
	"strings"

	"rave.page/mate/internal/visualeditor"
)

// Direct manipulation on the editor stage (image mode). The stage div carries
// data-actpos=ed-stage; shell.go forwards fractional down/move/up coords and Go
// owns hit-testing, drag state and snapping (visualeditor/geometry.go). During
// a drag only #ed-stage-body is patched - replacing the stage element itself
// would drop the pointer capture (shell.go __pcur).

// edTolFrac/edRotOffFrac size the handle hit zone + rotate-handle offset as a
// fraction of doc width (the stage is responsive; CSS mirrors these in cqw).
const (
	edTolFrac    = 0.012
	edRotOffFrac = 0.03
	edSnapFrac   = 0.007
)

// edDrag is the active stage drag (guarded by editor.mu).
type edDrag struct {
	active  bool
	moved   bool
	handle  visualeditor.Handle // HandleNone = move drag
	id      string
	orig    visualeditor.FlatBox
	origRot float64
	downPX  float64
	downPY  float64
	downAng float64 // rotate: pointer angle at down
	shift   bool
	boxes   []visualeditor.FlatBox // down-time snapshot (snap candidates)
	moveIdx int
}

func init() {
	onExact("ed-stage", func(u *UI, m actMsg) { u.edStage(m.Val) })
	onPrefix("ed-align:", func(u *UI, m actMsg) { u.edAlign(m.arg("ed-align:")) })
	onExact("ed-dup", func(u *UI, m actMsg) { u.edDuplicate() })
	onExact("key:ed", func(u *UI, m actMsg) { u.edKey(m.Val) })
}

// edFlatBoxOf snapshots a leaf's placement (caller holds editor.mu).
func edFlatBoxOf(l *visualeditor.Layer) visualeditor.FlatBox {
	sx, sy := l.Transform.ScaleX, l.Transform.ScaleY
	if sx == 0 {
		sx = 1
	}
	if sy == 0 {
		sy = 1
	}
	return visualeditor.FlatBox{
		X: l.Transform.X, Y: l.Transform.Y, W: l.W, H: l.H,
		SX: sx, SY: sy, Rot: l.Transform.Rotation, Locked: l.Locked,
	}
}

func (u *UI) edStage(val string) {
	phase, rest, ok := strings.Cut(val, ":")
	if !ok {
		return
	}
	parts := strings.Split(rest, ",")
	if len(parts) < 2 {
		return
	}
	fx, e1 := strconv.ParseFloat(parts[0], 64)
	fy, e2 := strconv.ParseFloat(parts[1], 64)
	if e1 != nil || e2 != nil {
		return
	}
	switch phase {
	case "down":
		mods := ""
		if len(parts) >= 3 {
			mods = parts[2]
		}
		u.edStageDown(fx, fy, mods)
	case "move":
		u.edStageMove(fx, fy)
	case "up":
		u.edStageUp()
	}
}

func (u *UI) edStageDown(fx, fy float64, mods string) {
	edEnsure()
	editor.mu.Lock()
	d := editor.doc
	px, py := fx*float64(d.W), fy*float64(d.H)
	tol, rotOff := edTolFrac*float64(d.W), edRotOffFrac*float64(d.W)
	boxes, ids := visualeditor.FlattenLeaves(d)
	shift := strings.Contains(mods, "s")

	// a selected leaf's handles win over any layer body
	if editor.selID != "" {
		if si := edIdxOf(ids, editor.selID); si >= 0 && !boxes[si].Locked {
			if hd := visualeditor.HandleAt(boxes[si], px, py, tol, rotOff); hd != visualeditor.HandleNone {
				editor.snapshot(true)
				editor.drag = edDrag{
					active: true, handle: hd, id: editor.selID,
					orig: boxes[si], origRot: boxes[si].Rot,
					downPX: px, downPY: py,
					downAng: visualeditor.AngleAt(boxes[si], px, py),
					shift:   shift, boxes: boxes, moveIdx: si,
				}
				editor.mu.Unlock()
				return
			}
		}
	}

	idx := visualeditor.HitTest(boxes, px, py)
	if idx < 0 {
		changed := editor.selID != ""
		editor.selID = ""
		editor.mu.Unlock()
		if changed {
			u.patchMain()
		}
		return
	}
	editor.selID = ids[idx]
	if !boxes[idx].Locked {
		editor.snapshot(true)
		editor.drag = edDrag{
			active: true, handle: visualeditor.HandleNone, id: ids[idx],
			orig: boxes[idx], origRot: boxes[idx].Rot,
			downPX: px, downPY: py, shift: shift,
			boxes: boxes, moveIdx: idx,
		}
	}
	editor.mu.Unlock()
	// only the stage body: a main patch here would replace the captured actpos div
	u.edPatchStageBody()
}

func (u *UI) edStageMove(fx, fy float64) {
	editor.mu.Lock()
	if !editor.drag.active {
		editor.mu.Unlock()
		return
	}
	d := editor.doc
	dr := &editor.drag
	l, _ := d.Find(dr.id)
	if l == nil {
		editor.drag = edDrag{}
		editor.guides = nil
		editor.mu.Unlock()
		return
	}
	px, py := fx*float64(d.W), fy*float64(d.H)
	switch dr.handle {
	case visualeditor.HandleNone: // move
		dx, dy := px-dr.downPX, py-dr.downPY
		if dr.shift { // constrain to dominant axis
			if absF(dx) >= absF(dy) {
				dy = 0
			} else {
				dx = 0
			}
		}
		thresh := edSnapFrac * float64(d.W)
		ndx, ndy, guides := visualeditor.SnapMove(dr.boxes, dr.moveIdx, dx, dy, thresh, float64(d.W), float64(d.H))
		l.Transform.X = dr.orig.X + ndx
		l.Transform.Y = dr.orig.Y + ndy
		editor.guides = guides
	case visualeditor.HandleRotate:
		ang := visualeditor.AngleAt(dr.orig, px, py)
		l.Transform.Rotation = visualeditor.RotateFrom(dr.origRot, dr.downAng, ang, dr.shift)
		editor.guides = nil
	default: // resize
		nw, nh, nx, ny := visualeditor.ResizeBox(dr.orig, dr.handle, px, py, dr.shift)
		l.W, l.H = nw, nh
		l.Transform.X, l.Transform.Y = nx, ny
		editor.guides = nil
	}
	dr.moved = true
	editor.mu.Unlock()
	u.edPatchStageBody()
}

func (u *UI) edStageUp() {
	editor.mu.Lock()
	if !editor.drag.active {
		editor.mu.Unlock()
		return
	}
	moved := editor.drag.moved
	editor.drag = edDrag{}
	editor.guides = nil
	if moved {
		editor.autosave()
	}
	editor.mu.Unlock()
	u.patchMain() // full refresh: inspector numbers + layers panel + handles
}

// edPatchStageBody re-renders only the stage interior (keeps pointer capture).
func (u *UI) edPatchStageBody() {
	editor.mu.Lock()
	body := edStageBodyHTML(u.edLayerStates(editor.doc.Root.Children, editor.doc), edGuideStates(editor.guides, editor.doc))
	editor.mu.Unlock()
	u.eval("window.__patch('ed-stage-body'," + jsQuote(body) + ")")
}

// ── align / duplicate / keyboard ──

// edAlign aligns the selected leaf's transformed bounds to the canvas.
// which ∈ l|ch|r|t|cv|b (left, center-h, right, top, center-v, bottom).
func (u *UI) edAlign(which string) {
	u.edEdit(func() {
		l := editor.sel()
		if l == nil || l.IsGroup() || l.Locked {
			return
		}
		b := edFlatBoxOf(l)
		minX, minY, maxX, maxY := b.Bounds()
		w, h := float64(editor.doc.W), float64(editor.doc.H)
		editor.snapshot(true)
		switch which {
		case "l":
			l.Transform.X -= minX
		case "ch":
			l.Transform.X += w/2 - (minX+maxX)/2
		case "r":
			l.Transform.X += w - maxX
		case "t":
			l.Transform.Y -= minY
		case "cv":
			l.Transform.Y += h/2 - (minY+maxY)/2
		case "b":
			l.Transform.Y += h - maxY
		default:
			return
		}
		editor.autosave()
	})
}

func (u *UI) edDuplicate() {
	u.edEdit(func() {
		l, parent := editor.doc.Find(editor.selID)
		if l == nil || parent == nil {
			return
		}
		editor.snapshot(true)
		cl := l.Clone()
		cl.Name = l.Name + " copy"
		cl.Transform.X += 16
		cl.Transform.Y += 16
		idx := edIndexOf(parent.Children, l.ID)
		parent.Children = append(parent.Children[:idx+1],
			append([]*visualeditor.Layer{cl}, parent.Children[idx+1:]...)...)
		editor.selID = cl.ID
		editor.autosave()
	})
}

// edKey handles the "ed" keyscope: arrow nudge (shift ×10), Del, ctrl+z.
func (u *UI) edKey(val string) {
	if val == "cz" {
		u.edUndo()
		return
	}
	step := 1.0
	v := strings.TrimPrefix(val, "c")
	if strings.HasPrefix(v, "s") {
		step, v = 10, v[1:]
	}
	if v == "del" {
		u.edDelete()
		return
	}
	var dx, dy float64
	switch v {
	case "up":
		dy = -step
	case "down":
		dy = step
	case "left":
		dx = -step
	case "right":
		dx = step
	default:
		return
	}
	u.edEdit(func() {
		l := editor.sel()
		if l == nil || l.Locked {
			return
		}
		editor.snapshot(false) // coalesces key repeats into one undo step
		l.Transform.X += dx
		l.Transform.Y += dy
		editor.autosave()
	})
}

func edIdxOf(ids []string, id string) int {
	for i, v := range ids {
		if v == id {
			return i
		}
	}
	return -1
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
