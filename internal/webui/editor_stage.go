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
	boxes   []visualeditor.FlatBox // down-time snapshot (snap candidates, selection excluded)
	moveIdx int

	origs         map[string][2]float64 // multi-move: selected id → orig Transform.X/Y
	pendingRemove string                // shift-click on a member: remove at up if not moved
	collapse      bool                  // plain click on a member: collapse to it at up if not moved
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
		changed := editor.selID != "" || len(editor.selMore) > 0
		editor.selID, editor.selMore = "", nil
		editor.mu.Unlock()
		if changed {
			u.patchMain()
		}
		return
	}
	id := ids[idx]
	pendingRemove, collapse := "", false
	switch {
	case shift && edSelHas(id):
		pendingRemove = id // remove on up unless the group gets dragged
	case shift:
		edSelToggle(id) // add + becomes primary
	case edSelHas(id):
		// plain down on a member: promote to primary, keep the group for the
		// drag; a plain CLICK (no move) collapses the selection to it at up
		if id != editor.selID {
			editor.selMore[editor.selID] = true
			delete(editor.selMore, id)
			editor.selID = id
		}
		collapse = len(editor.selMore) > 0
	default:
		editor.selID, editor.selMore = id, nil
	}
	if !boxes[idx].Locked {
		editor.snapshot(true)
		// snap candidates: everything OUTSIDE the moving selection + the primary
		// box appended last (a co-moving layer must not act as a snap line)
		selSet := map[string]bool{}
		origs := map[string][2]float64{}
		for _, sid := range edSelIDs() {
			selSet[sid] = true
			if sl, _ := d.Find(sid); sl != nil {
				origs[sid] = [2]float64{sl.Transform.X, sl.Transform.Y}
			}
		}
		cands := make([]visualeditor.FlatBox, 0, len(boxes))
		for i, b := range boxes {
			if !selSet[ids[i]] {
				cands = append(cands, b)
			}
		}
		cands = append(cands, boxes[idx])
		editor.drag = edDrag{
			active: true, handle: visualeditor.HandleNone, id: id,
			orig: boxes[idx], origRot: boxes[idx].Rot,
			downPX: px, downPY: py, shift: shift,
			boxes: cands, moveIdx: len(cands) - 1,
			origs: origs, pendingRemove: pendingRemove, collapse: collapse,
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
	case visualeditor.HandleNone: // move (the whole selection)
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
		for id, o := range dr.origs {
			if ml, _ := d.Find(id); ml != nil && !ml.Locked {
				ml.Transform.X = o[0] + ndx
				ml.Transform.Y = o[1] + ndy
			}
		}
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
	dr := editor.drag
	editor.drag = edDrag{}
	editor.guides = nil
	if dr.moved {
		editor.autosave()
	} else if dr.pendingRemove != "" { // shift-click on a member: deselect it
		edSelToggle(dr.pendingRemove)
	} else if dr.collapse { // plain click on a member: collapse to it
		editor.selID, editor.selMore = dr.id, nil
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

// edAlign aligns the selection: a single leaf against the canvas, 2+ selected
// leaves mutually against the selection's bounding box (Photoshop-style).
// which ∈ l|ch|r|t|cv|b (left, center-h, right, top, center-v, bottom).
func (u *UI) edAlign(which string) {
	u.edEdit(func() {
		type item struct {
			l                      *visualeditor.Layer
			minX, minY, maxX, maxY float64
		}
		var items []item
		for _, id := range edSelIDs() {
			l, _ := editor.doc.Find(id)
			if l == nil || l.IsGroup() || l.Locked {
				continue
			}
			b := edFlatBoxOf(l)
			minX, minY, maxX, maxY := b.Bounds()
			items = append(items, item{l, minX, minY, maxX, maxY})
		}
		if len(items) == 0 {
			return
		}
		refMinX, refMinY := 0.0, 0.0
		refMaxX, refMaxY := float64(editor.doc.W), float64(editor.doc.H)
		if len(items) > 1 { // mutual: the selection AABB is the reference
			refMinX, refMinY = items[0].minX, items[0].minY
			refMaxX, refMaxY = items[0].maxX, items[0].maxY
			for _, it := range items[1:] {
				refMinX, refMaxX = minF(refMinX, it.minX), maxF(refMaxX, it.maxX)
				refMinY, refMaxY = minF(refMinY, it.minY), maxF(refMaxY, it.maxY)
			}
		}
		editor.snapshot(true)
		for _, it := range items {
			switch which {
			case "l":
				it.l.Transform.X += refMinX - it.minX
			case "ch":
				it.l.Transform.X += (refMinX+refMaxX)/2 - (it.minX+it.maxX)/2
			case "r":
				it.l.Transform.X += refMaxX - it.maxX
			case "t":
				it.l.Transform.Y += refMinY - it.minY
			case "cv":
				it.l.Transform.Y += (refMinY+refMaxY)/2 - (it.minY+it.maxY)/2
			case "b":
				it.l.Transform.Y += refMaxY - it.maxY
			default:
				return
			}
		}
		editor.autosave()
	})
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func (u *UI) edDuplicate() {
	u.edEdit(func() {
		ids := edSelIDs()
		if len(ids) == 0 {
			return
		}
		editor.snapshot(true)
		editor.selID, editor.selMore = "", nil
		for _, id := range ids {
			l, parent := editor.doc.Find(id)
			if l == nil || parent == nil {
				continue
			}
			cl := l.Clone()
			cl.Name = l.Name + " copy"
			cl.Transform.X += 16
			cl.Transform.Y += 16
			idx := edIndexOf(parent.Children, l.ID)
			parent.Children = append(parent.Children[:idx+1],
				append([]*visualeditor.Layer{cl}, parent.Children[idx+1:]...)...)
			if editor.selID == "" { // the copies become the selection
				editor.selID = cl.ID
				continue
			}
			if editor.selMore == nil {
				editor.selMore = map[string]bool{}
			}
			editor.selMore[cl.ID] = true
		}
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
		moved := false
		for _, id := range edSelIDs() {
			l, _ := editor.doc.Find(id)
			if l == nil || l.Locked {
				continue
			}
			if !moved {
				editor.snapshot(false) // coalesces key repeats into one undo step
				moved = true
			}
			l.Transform.X += dx
			l.Transform.Y += dy
		}
		if moved {
			editor.autosave()
		}
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
