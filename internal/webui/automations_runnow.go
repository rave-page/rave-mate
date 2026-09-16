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
	mu           sync.Mutex
	autoID       string
	label        string
	watch        string
	acts         []automation.Action
	preview      automation.SweepPreview // rules-first: what the match rules select right now
	specificFile bool                    // secondary mode: run over one hand-picked file
	file         string
	ack          bool // destructive-chain acknowledgement
	runningID    string
	errTx        string
	conflict     bool   // the run coordinator will make this run wait (populated by the coordinator)
	conflictText string // "Will wait — <auto> is transcoding <file>."
}

// arOwner is this modal's slot-owner key (ui.go modalTok): a run outlives its modal by design, so
// every patch its completion makes is guarded by the session token openModalAs hands back.
const arOwner = "auto-run"

func init() {
	onPrefix("auto-run:", func(u *UI, m actMsg) { u.arOpen(m.arg("auto-run:")) })
	onExact("auto-run-mode", func(u *UI, m actMsg) { u.arSetMode(u.actTok(m), m.Val == "true") })
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
	pin := u.modalCur()
	u.bg(func() {
		a, ok := u.svc.Automations.Get(id)
		if !ok || u.stopped() {
			return
		}
		// Preview is read-only (ReadDir + stat) - the default rules-first mode renders it. Runs
		// off-thread here, before the slot is claimed, so a slow watch dir never blocks the UI.
		prev, _ := u.svc.Automations.Preview(id)
		u.claimModalWith(pin, arOwner, func() string {
			s := &u.ar
			s.mu.Lock()
			defer s.mu.Unlock()
			s.autoID, s.label, s.watch = a.ID, autoLabelOf(a.Label), a.WatchDir
			s.acts = append([]automation.Action(nil), a.Actions...)
			s.preview = prev
			// runningID belongs to the run, not the modal - left alone. Default to rules-first.
			s.file, s.ack, s.errTx, s.specificFile = "", false, "", false
			s.conflict, s.conflictText = u.arConflict(a)
			return u.arModalHTMLLocked(s)
		})
	})
}

// arSetMode flips between the rules-first default and the secondary single-file flow.
func (u *UI) arSetMode(tok modalTok, specific bool) {
	u.updateModalIf(tok, func() string {
		s := &u.ar
		s.mu.Lock()
		defer s.mu.Unlock()
		s.specificFile, s.errTx = specific, ""
		return u.arModalHTMLLocked(s)
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
	id, label, file, specific := s.autoID, s.label, s.file, s.specificFile
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
		// Default = the rules-first sweep (schedule semantics); secondary = one hand-picked file.
		var doneToast string
		var err error
		if specific {
			var run automation.Run
			run, err = u.svc.Automations.RunManual(ctx, id, file)
			if err == nil {
				doneToast = i18n.T("automations.run.finished", i18n.A{"label": label, "status": run.Status})
			}
		} else {
			var res automation.SweepResult
			res, err = u.svc.Automations.RunSweep(ctx, id)
			if err == nil {
				doneToast = arSweepToast(label, res)
			}
		}
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
		u.toast(doneToast)
	})
}

// arSweepToast reports the outcome of a rules-first sweep: coalesced into a running one, queued
// behind a conflict, or the count of files swept.
func arSweepToast(label string, res automation.SweepResult) string {
	switch {
	case res.Coalesced:
		return i18n.T("automations.run.coalesced", i18n.A{"label": label})
	case res.Queued:
		return i18n.T("automations.run.conflictWait", i18n.A{"label": label, "activity": res.QueueReason})
	default:
		return i18n.T("automations.run.sweepFinished", i18n.A{
			"label": label, "n": i18n.Tn("automations.run.matchedCount", len(res.Runs))})
	}
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

// runnable reports that the footer would render a live Run button. Caller holds s.mu. Default mode
// needs at least one matching file; the secondary mode needs a picked file; both need the erase ack.
func (s *arSt) runnable() bool {
	if s.busy() {
		return false
	}
	if s.specificFile {
		if strings.TrimSpace(s.file) == "" {
			return false
		}
	} else if s.preview.Total == 0 {
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
		if h, ok := zigWire("RenderAutoRunNowV2", wireAutoRunNow(st), zigui.RenderAutoRunNowV2,
			zigui.RenderAutoRunNow, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return arModalHTMLOf(st)
}

// arConflict asks the run coordinator whether a rules-first sweep of a would have to wait right
// now, and builds the human reason line shown above the primary ("Will wait — <auto> is …").
func (u *UI) arConflict(a automation.Automation) (bool, string) {
	if u.svc.Automations == nil {
		return false, ""
	}
	sc, ok := u.svc.Automations.CoordConflict(a.ID)
	if !ok || !sc.Blocked {
		return false, ""
	}
	activity := i18n.T("automations.run.actRunning")
	if sc.OtherHeavy && sc.OtherFile != "" {
		activity = i18n.T("automations.run.actTranscode", i18n.A{"file": sc.OtherFile})
	}
	label := sc.OtherLabel
	if strings.TrimSpace(label) == "" {
		label = i18n.T("automations.unnamed")
	}
	return true, i18n.T("automations.run.conflictWait", i18n.A{"label": label, "activity": activity})
}

// arPreviewShown caps how many preview rows the modal lists before folding the rest into "and N more".
const arPreviewShown = 8

// arModalState resolves the dialog. Caller holds s.mu.
func arModalState(s *arSt) arModalSt {
	st := arModalSt{
		Title:        i18n.T("automations.run.title"),
		Auto:         newKV(i18n.T("automations.run.automation"), s.label),
		Watch:        newKV(i18n.T("automations.ed.watchDir"), s.watch),
		Chain:        newKV(i18n.T("automations.run.chain"), autoChainSummary(s.acts)),
		IgnoresMatch: i18n.T("automations.run.ignoresMatch"),
		File: newDlgFieldSt(i18n.T("automations.run.file"), "auto-run-file", s.file, "text",
			i18n.T("automations.run.filePH"), tipTopicSt("auto-run-now")),
		Browse:       uiBtn{Label: i18n.T("common.browse"), Variant: "ghost", Act: "pick-file:auto-run-file"},
		Erases:       autoChainDeletes(s.acts),
		SpecificFile: s.specificFile,
		ModeToggle:   newToggle(i18n.T("automations.run.modeSpecific"), "auto-run-mode", s.specificFile),
		Conflict:     s.conflict,
		ConflictText: s.conflictText,
	}
	if s.errTx != "" {
		st.HasErr, st.Err = true, s.errTx
	}
	if !s.specificFile {
		// Rules-first default: the conditions as badges, then what they match now.
		st.CondsLabel = i18n.T("automations.run.condsLabel")
		st.CondsAny = i18n.T("automations.run.condsAny")
		st.Conds = arCondBadges(s.preview.Match)
		if s.preview.Total == 0 {
			st.Empty = true
			st.EmptyTitle = i18n.T("automations.run.sweepEmpty")
			st.EmptyHints = arWhyHints(s.preview.Match)
		} else {
			st.Files = arPrevRows(s.preview.Files)
			if s.preview.Total > len(st.Files) {
				st.More = i18n.Tn("automations.run.more", s.preview.Total-len(st.Files))
			}
			st.TotalLine = i18n.Tn("automations.run.matched", s.preview.Total,
				i18n.A{"size": arBytes(s.preview.TotalBytes)})
		}
	}
	if st.Erases {
		st.DeleteWarn = i18n.T("automations.run.deleteWarn")
		st.DeleteScope = i18n.T("automations.run.deleteScope")
		st.DeleteTipS = tipTopicSt("auto-delete-action")
		st.Ack = newToggle(i18n.T("automations.run.ack"), "auto-run-ack", s.ack)
	}
	st.Foot = arFooterState(s, st.Erases)
	return st
}

// arPrevRows resolves up to arPreviewShown SweepFiles into display rows (name · human size · age).
func arPrevRows(files []automation.SweepFile) []arPrevRow {
	n := len(files)
	if n > arPreviewShown {
		n = arPreviewShown
	}
	out := make([]arPrevRow, 0, n)
	for _, f := range files[:n] {
		out = append(out, arPrevRow{Name: f.Name, Size: arBytes(f.Size), Meta: fileAge(f.ModTime)})
	}
	return out
}

// arFooterState gates the Run button on each missing precondition in turn, naming it in the
// disabled button's title (btnGated) rather than hiding the control. The primary's label carries
// the count in rules mode ("Run on N files") and becomes "Queue run" when a conflict is pending.
// Caller holds s.mu.
func arFooterState(s *arSt, erases bool) arFootSt {
	f := arFootSt{Cancel: i18n.T("common.cancel")}
	n := s.preview.Total
	// Base label (live case); gates below may override it.
	switch {
	case s.specificFile && erases:
		f.Label = i18n.T("automations.run.goDestructive")
	case s.specificFile:
		f.Label = i18n.T("automations.run.go")
	case erases:
		f.Label = i18n.Tn("automations.run.sweepGoErase", n)
	default:
		f.Label = i18n.Tn("automations.run.sweepGo", n)
	}
	switch {
	case s.busy():
		f.Gated, f.Label, f.Why = true, i18n.T("automations.run.running"), i18n.T("automations.run.runningWhy")
	case s.specificFile && strings.TrimSpace(s.file) == "":
		f.Gated, f.Why = true, i18n.T("automations.run.needFile")
	case !s.specificFile && n == 0:
		f.Gated, f.Why = true, i18n.T("automations.run.sweepEmpty")
	case erases && !s.ack:
		f.Gated, f.Why = true, i18n.T("automations.run.needAck")
	case s.conflict:
		// The run will queue behind another. One primary still: "Queue run", destructive if it erases.
		f.Label = i18n.T("automations.run.queue")
		f.Variant = boolStr2(erases, "destructive", "primary")
	case erases:
		f.Variant = "destructive"
	default:
		f.Variant = "primary"
	}
	return f
}

// boolStr2 picks a or b on cond (a tiny ternary for one-line variant choices).
func boolStr2(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
