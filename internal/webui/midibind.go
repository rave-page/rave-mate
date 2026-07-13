package webui

// MIDI-mapped desktop-UI actions: the webview registers handlers for the vrbind ui.* catalog
// (cue-editor transport, library browsing, nav history) on the shared app dispatcher
// (Services.BindDispatcher, fed by the MIDI child's forward tap). Handlers fire on the MIDI
// event goroutine, so they only enqueue an act onto the shell's serial act worker - the actual
// UI mutation runs there, ordered with clicks/keys, via the "midiui:" handlers below. Scope
// gating mirrors the keyboard path exactly: keyScope() must name the surface, so a pad mapped
// to "audition" is inert until the cue editor is open.

import (
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/vrbind"
)

// postAct enqueues a Go-originated action on the shell's serial act worker (the same lane page
// clicks/keys arrive on). Drops when the queue is full or the shell is gone.
func (u *UI) postAct(act, val string) {
	if u.shell == nil {
		return
	}
	u.shell.post(`{"act":` + jsQuote(act) + `,"val":` + jsQuote(val) + `}`)
}

// registerUIBinds wires the vrbind ui.* actions to this window. Called from New for the
// primary window only (headless mirror UIs never register - a MIDI pad must not drive a
// remote session's cursor).
func (u *UI) registerUIBinds() {
	d := u.svc.BindDispatcher
	if d == nil {
		return
	}
	// step actions: emit one keyboard-twin act per step, in the step's direction
	stepper := func(act, neg, pos string) vrbind.StepHandler {
		return func(_ string, delta int) {
			val, n := pos, delta
			if delta < 0 {
				val, n = neg, -delta
			}
			for i := 0; i < n; i++ {
				u.postAct(act, val)
			}
		}
	}
	d.RegisterHold(vrbind.ActCEAudition, func(_ string, down bool) {
		if down {
			u.postAct("midiui:ce", "space")
		} else {
			u.postAct("midiui:ce", "spaceup")
		}
	})
	d.RegisterStep(vrbind.ActCECursor, stepper("midiui:ce", "left", "right"))
	d.RegisterStep(vrbind.ActCECursorJump, stepper("midiui:ce", "sleft", "sright"))
	d.RegisterStep(vrbind.ActCEJumpSize, stepper("midiui:ce", "sdown", "sup"))
	d.RegisterStep(vrbind.ActCETrack, stepper("midiui:ce", "up", "down"))
	d.RegisterStep(vrbind.ActCEGridNudge, stepper("midiui:ce", "cleft", "cright"))
	d.RegisterStep(vrbind.ActCEGridNudgeFine, stepper("midiui:ce", "csleft", "csright"))
	d.Register(vrbind.ActCEDropAdd, func(string) { u.postAct("midiui:ce", "enter") })
	d.Register(vrbind.ActCEDropDel, func(string) { u.postAct("midiui:ce", "senter") })
	d.Register(vrbind.ActCECue, func(string) { u.postAct("midiui:ce", "sspace") })
	d.Register(vrbind.ActCEDelSel, func(string) { u.postAct("midiui:ce", "del") })
	d.Register(vrbind.ActCEUndo, func(string) { u.postAct("midiui:ce", "cz") })
	d.RegisterStep(vrbind.ActLibNav, stepper("midiui:lib", "up", "down"))
	d.Register(vrbind.ActLibOpen, func(string) { u.postAct("midiui:lib", "open") })
	d.Register(vrbind.ActNavBack, func(string) { u.postAct("midiui:nav", "back") })
	d.Register(vrbind.ActNavFwd, func(string) { u.postAct("midiui:nav", "fwd") })
}

func init() {
	// Runs on the act worker (postAct → acts chan). Scope-gated like shell.go's keydown:
	// cue-editor acts need the editor open on the Library tab, library acts need the
	// collection list showing; nav history is global.
	onPrefix("midiui:", func(u *UI, m actMsg) {
		switch m.arg("midiui:") {
		case "ce":
			if u.keyScope() != "cueedit" {
				return
			}
			u.ceKey(m.Val)
		case "lib":
			if u.keyScope() != "library" {
				return
			}
			switch m.Val {
			case "up":
				u.libKeyNav(false)
			case "down":
				u.libKeyNav(true)
			case "open":
				u.midiLibOpen()
			}
		case "nav":
			if m.Val == "back" {
				u.navBack()
			} else {
				u.navFwd()
			}
		}
	})
}

// midiLibOpen opens the current collection selection in the cue editor (MIDI "load" button).
func (u *UI) midiLibOpen() {
	s := u.lib()
	s.mu.Lock()
	path := ""
	if s.sel != nil {
		path = s.sel.path
	}
	s.mu.Unlock()
	if path == "" {
		u.toast(i18n.T("midictl.uimap.noSelection"))
		return
	}
	u.ceEnter(path)
}
