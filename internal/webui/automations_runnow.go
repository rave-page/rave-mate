package webui

// Automations "Run now" - an on-demand single-file run of one automation's chain: the manual
// counterpart to the file-arrival watcher and the schedules. One modal, gated in stages: pick the
// target file (the repo's native pick-file primitive), acknowledge an erasing chain, then run.
//
// Run now deliberately BYPASSES the match rules - RunManual hands the file straight to the engine
// (Service.execute, with no eligible() check, unlike the watch + sweep paths) - so the modal says
// so: extension, size and age gates do not protect the file you point it at.
//
// Off the actWorker: Get reads bbolt, and RunManual drives ffmpeg through the worker pool for as
// long as the file takes; both run in u.bg. RunManual records the run + the automation's last-run
// summary through the Manager, so Version() bumps and the automations tick repaints the list.

import (
	"context"
	"strings"
	"sync"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/zigui"
)

// arSt is the run-now modal's state. acts is an independent copy (from Get), used for the chain
// summary + the delete verdict. runningID is the automation whose run is in flight (not a bool):
// the modal is single-slot but a run outlives it, so re-opening for a DIFFERENT automation must
// show a live button, and re-opening for the SAME one must show "Running…" instead of a button
// that actStart would silently swallow.
type arSt struct {
	mu        sync.Mutex
	autoID    string
	label     string
	watch     string
	acts      []automation.Action
	file      string
	ack       bool // destructive-chain acknowledgement
	runningID string
	errTx     string
}

// arOwner is this modal's slot-owner key (ui.go modalTok): a run outlives its modal by design, so
// every patch its completion makes is guarded by the session token openModalAs hands back.
const arOwner = "auto-run"

func init() {
	onPrefix("auto-run:", func(u *UI, m actMsg) { u.arOpen(m.arg("auto-run:")) })
	onExact("auto-run-file", func(u *UI, m actMsg) { u.arSetFile(u.actTok(m), m.Val) })
	onExact("auto-run-ack", func(u *UI, m actMsg) { u.arSetAck(u.actTok(m), m.Val == "true") })
	onExact("auto-run-go", func(u *UI, m actMsg) { u.arGo(u.actTok(m)) })
}

// arOpen loads the automation into the run modal. Get re-reads + unmarshals from bbolt, so the
// modal owns an independent copy of the chain - List() elements alias the service cache.
// prev pins the slot as it was at click time; the load runs inside claimModalWith, so a read that
// lands after the user moved on neither renders NOR seeds the modal they moved to with this
// automation's chain (which is what its Run button would then erase a file with).
func (u *UI) arOpen(id string) {
	if u.svc.Automations == nil {
		return
	}
	prev := u.modalCur()
	u.bg(func() {
		a, ok := u.svc.Automations.Get(id)
		if !ok || u.stopped() {
			return
		}
		u.claimModalWith(prev, arOwner, func() string {
			s := &u.ar
			s.mu.Lock()
			defer s.mu.Unlock()
			s.autoID, s.label, s.watch = a.ID, autoLabelOf(a.Label), a.WatchDir
			s.acts = append([]automation.Action(nil), a.Actions...)
			s.file, s.ack, s.errTx = "", false, "" // runningID belongs to the run, not the modal - left alone
			return u.arModalHTMLLocked(s)
		})
	})
}

// arSetFile stores the target under tok. pick-file:auto-run-file lands here with the chosen path,
// pinned to the session that opened the dialog: the file was picked to be run through THAT
// automation's chain, and this modal's chain can end in a delete - dropping the path is the only
// safe answer once the session is gone. Always re-opens - the picked path has to show in the field.
func (u *UI) arSetFile(tok modalTok, p string) {
	u.updateModalIf(tok, func() string {
		s := &u.ar
		s.mu.Lock()
		defer s.mu.Unlock()
		s.file, s.errTx = strings.TrimSpace(p), ""
		return u.arModalHTMLLocked(s)
	})
}

// arSetAck records the erase acknowledgement; it gates the Run button, so re-open.
func (u *UI) arSetAck(tok modalTok, on bool) {
	u.updateModalIf(tok, func() string {
		s := &u.ar
		s.mu.Lock()
		defer s.mu.Unlock()
		s.ack = on
		return u.arModalHTMLLocked(s)
	})
}

// arGo runs the chain over the chosen file off the actWorker. Keyed per automation: two different
// automations may run at once (the watcher already does that), the same one may not. tok is the
// session that clicked Run: the chain can erase the file, so an act that arrives when that modal
// is gone starts nothing - it is stale, and what is on screen now is not what was consented to.
func (u *UI) arGo(tok modalTok) {
	if u.svc.Automations == nil {
		return
	}
	s := &u.ar
	s.mu.Lock()
	if !s.runnable() { // the button is gated; a stale click must not start a run
		s.mu.Unlock()
		return
	}
	id, label, file := s.autoID, s.label, s.file
	s.mu.Unlock()
	if !u.actStart("auto-run:" + id) {
		return
	}
	// Mark running + repaint the footer as "Running…" - and prove the modal is still tok's while
	// doing it. Refused = the run was never armed, so undo the actStart and drop the click.
	if !u.updateModalIf(tok, func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.runningID, s.errTx = id, ""
		return u.arModalHTMLLocked(s)
	}) {
		u.actEnd("auto-run:" + id)
		return
	}
	u.pendingAct("auto-run-go")
	u.toast(i18n.T("automations.run.started", i18n.A{"label": label}))

	u.bg(func() {
		defer u.actEnd("auto-run:" + id)
		ctx, cancel := u.arRunCtx()
		defer cancel()
		run, err := u.svc.Automations.RunManual(ctx, id, file)
		s.mu.Lock()
		if s.runningID == id { // a later run for another automation owns the slot now - don't clear it
			s.runningID = ""
		}
		s.mu.Unlock()
		if u.stopped() {
			return
		}
		if err != nil {
			u.logErr("automation run", err)
			u.arRunFailed(tok, err)
			return
		}
		if !u.closeModalIf(tok) {
			// Our modal is gone: cancelled, or another feature owns the slot. Never force it shut -
			// that is how an unrelated open form (and its unsaved edits) got destroyed. If a LATER
			// run-now session is up, its footer gates on runningID, which this run just cleared:
			// refresh it so "Running…" becomes a live button again.
			u.openModalIfOwner(arOwner, u.arModalHTML())
		}
		u.patchMain() // the run + the automation's last-run badge are both on this tab
		u.toast(i18n.T("automations.run.finished", i18n.A{"label": label, "status": run.Status}))
	})
}

// arRunFailed reports a run failure into the modal that started it, or - if the user cancelled it
// and moved on - as a toast. A completion must never re-open a modal on a user who closed it, but
// the failure of a run they asked for must still surface somewhere. The error text is written only
// under tok: run A's failure parked in the shared state would surface on run B's next re-render,
// blaming B's automation for it.
func (u *UI) arRunFailed(tok modalTok, err error) {
	ok := u.updateModalIf(tok, func() string {
		s := &u.ar
		s.mu.Lock()
		defer s.mu.Unlock()
		s.errTx = err.Error()
		return u.arModalHTMLLocked(s)
	})
	if !ok {
		u.toast(i18n.T("automations.run.failed", i18n.A{"error": err.Error()}))
	}
}

// arRunCtx is u.actx() without the deadline: the watch + schedule triggers run this same chain on
// the daemon's own unbounded context, and transcoding a 3-hour set outlasts any deadline worth
// picking. Still cancelled by Stop() so an abandoned run dies with the window.
func (u *UI) arRunCtx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { // exits with the ctx (callers defer cancel)
		select {
		case <-u.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// runnable reports that the footer would render a live Run button. Caller holds s.mu.
func (s *arSt) runnable() bool {
	if s.busy() || strings.TrimSpace(s.file) == "" {
		return false
	}
	return !autoChainDeletes(s.acts) || s.ack
}

// busy reports this modal's own automation is mid-run. Caller holds s.mu.
func (s *arSt) busy() bool { return s.runningID != "" && s.runningID == s.autoID }

// ── render: impure state builder (the modal's own chain copy, no I/O) + the Zig bridge ──
//
// The pure renderer lives in render_automations_run.go, mirrored in dialogs_b.zig.

func (u *UI) arModalHTML() string {
	s := &u.ar
	s.mu.Lock()
	defer s.mu.Unlock()
	return u.arModalHTMLLocked(s)
}

// arModalHTMLLocked renders the modal. Caller holds s.mu - the mutators already do, so the write
// and the render it produces stay one atomic step under the slot lock.
func (u *UI) arModalHTMLLocked(s *arSt) string {
	st := arModalState(s)
	if zigui.Available() {
		if h, ok := zigui.RenderAutoRunNow(stateJSON(st)); ok {
			return h
		}
	}
	return arModalHTMLOf(st)
}

// arModalState resolves the dialog. Caller holds s.mu.
func arModalState(s *arSt) arModalSt {
	st := arModalSt{
		Title:        i18n.T("automations.run.title"),
		Auto:         newKV(i18n.T("automations.run.automation"), s.label),
		Watch:        newKV(i18n.T("automations.ed.watchDir"), s.watch),
		Chain:        newKV(i18n.T("automations.run.chain"), autoChainSummary(s.acts)),
		IgnoresMatch: i18n.T("automations.run.ignoresMatch"),
		File: newDlgField(i18n.T("automations.run.file"), "auto-run-file", s.file, "text",
			i18n.T("automations.run.filePH"), tipTopic("auto-run-now")),
		Browse: uiBtn{Label: i18n.T("common.browse"), Variant: "ghost", Act: "pick-file:auto-run-file"},
		Erases: autoChainDeletes(s.acts),
	}
	if s.errTx != "" {
		st.HasErr, st.Err = true, s.errTx
	}
	if st.Erases {
		st.DeleteWarn = i18n.T("automations.run.deleteWarn")
		st.DeleteScope = i18n.T("automations.run.deleteScope")
		st.DeleteTip = tipTopic("auto-delete-action")
		st.Ack = newToggle(i18n.T("automations.run.ack"), "auto-run-ack", s.ack)
	}
	st.Foot = arFooterState(s, st.Erases)
	return st
}

// arFooterState gates the Run button on each missing precondition in turn, naming it in the
// disabled button's title (btnGated) rather than hiding the control. Caller holds s.mu.
func arFooterState(s *arSt, erases bool) arFootSt {
	f := arFootSt{Cancel: i18n.T("common.cancel"), Label: i18n.T("automations.run.go")}
	if erases {
		f.Label = i18n.T("automations.run.goDestructive")
	}
	switch {
	case s.busy():
		f.Gated, f.Label, f.Why = true, i18n.T("automations.run.running"), i18n.T("automations.run.runningWhy")
	case strings.TrimSpace(s.file) == "":
		f.Gated, f.Why = true, i18n.T("automations.run.needFile")
	case erases && !s.ack:
		f.Gated, f.Why = true, i18n.T("automations.run.needAck")
	case erases:
		f.Variant = "destructive"
	default:
		f.Variant = "primary"
	}
	return f
}
