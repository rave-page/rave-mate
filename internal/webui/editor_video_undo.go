package webui

// Editor video-mode undo/redo (Ctrl+Z / Ctrl+Shift+Z, Ctrl+Y). One history for
// everything the tab owns: the project (source, reframe, zoom/pan keyframes,
// layout, effect chain + params + blend/opacity, export preset) AND the trim
// markers, which live in mpSt - an accidental IN/OUT drag is the exact thing
// this is for. Checkpoints are taken BEFORE a mutation; rapid slider/drag
// traffic coalesces so one gesture is one undo step.

import (
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/videoedit"
)

const (
	edvUndoMax      = 60                     // history depth (bounded: project snapshots are small but not free)
	edvUndoCoalesce = 600 * time.Millisecond // within this of the last checkpoint = same gesture
)

// edvSnap is one point in the history.
type edvSnap struct {
	proj   videoedit.Project
	inSec  float64
	outSec float64
}

func init() {
	mpTrimSnap = func(u *UI, host string) {
		if host == "editor" {
			u.edvSnapshot()
		}
	}
	onExact("edv-undo", func(u *UI, m actMsg) { u.edvUndo(false) })
	onExact("edv-redo", func(u *UI, m actMsg) { u.edvUndo(true) })
}

// edvNow captures the current state (project + trim).
func (u *UI) edvNow() edvSnap {
	t := u.mpSnap("editor")
	editor.mu.Lock()
	edvEnsure()
	s := edvSnap{proj: editor.video.proj.Clone(), inSec: t.inSec, outSec: t.outSec}
	editor.mu.Unlock()
	return s
}

// edvSnapshot pushes an undo checkpoint of the CURRENT state and drops the redo
// branch. Called before a mutation; coalesces a burst into one step.
func (u *UI) edvSnapshot() {
	snap := u.edvNow()
	editor.mu.Lock()
	defer editor.mu.Unlock()
	v := &editor.video
	if !v.undoAt.IsZero() && time.Since(v.undoAt) < edvUndoCoalesce && len(v.undo) > 0 {
		v.undoAt = time.Now() // same gesture: keep the state from before it started
		return
	}
	v.undo = append(v.undo, snap)
	if len(v.undo) > edvUndoMax {
		v.undo = v.undo[len(v.undo)-edvUndoMax:]
	}
	v.redo = nil
	v.undoAt = time.Now()
}

// edvUndo restores the previous (or next, when redo) state.
func (u *UI) edvUndo(redo bool) {
	cur := u.edvNow()
	editor.mu.Lock()
	v := &editor.video
	src, dst := &v.undo, &v.redo
	if redo {
		src, dst = &v.redo, &v.undo
	}
	if len(*src) == 0 {
		editor.mu.Unlock()
		key := "editor.video.toast.noUndo"
		if redo {
			key = "editor.video.toast.noRedo"
		}
		u.toast(i18n.T(key))
		return
	}
	snap := (*src)[len(*src)-1]
	*src = (*src)[:len(*src)-1]
	*dst = append(*dst, cur)
	v.proj = snap.proj
	v.undoAt = time.Time{} // a restore never coalesces with the next edit
	edvSave()
	editor.mu.Unlock()

	t := u.mpMut("editor", func(m *mpSt) { m.inSec, m.outSec = snap.inSec, snap.outSec })
	u.edvPatchInsp()
	u.mpPatchWave(t)
	u.mpPatchEdit(t)
	u.mpPatchTransport(t)
	u.mpSyncVidTrim(t)
	u.edvSyncPlayerVars()
	u.edvPatchFrame()
	u.edvPatchKfBox()
	u.edvPrevKick()
	key := "editor.video.toast.undone"
	if redo {
		key = "editor.video.toast.redone"
	}
	u.toast(i18n.T(key))
}
