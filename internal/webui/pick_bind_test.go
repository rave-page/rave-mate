package webui

// A picked path belongs to the UI context that asked for it. The native dialog is NOT modal to our
// webview (pickers_windows.go passes hwndOwner 0 and pumps it on its own OS thread) and runPick
// allows 10 minutes, so the user can drive the whole app while it stands open: cancel the form that
// asked, open another entity, reorder the very step that asked. Every case below is a sequence that
// window makes reachable, driven through pickApply - the act lane the returning dialog re-enters.

import (
	"strings"
	"testing"

	"rave.page/mate/internal/automation"
)

// pickBack simulates the dialog returning with path: register the request the way runPick does,
// then let the act lane apply it.
func pickBack(u *UI, tok modalTok, target, path string) {
	u.pickApply(itoa64(u.pickPut(pickReq{tok, target})), path)
}

func itoa64(v uint64) string { return aeK(v) }

// TestPickedPathRefusedAfterFormReplaced is the reported HIGH: the picker's redispatch bypassed the
// modal-identity guard entirely - openModalAs claimed the slot unconditionally. Sequence: open the
// editor for A, Browse on its watch folder, go back to the live window (the dialog is not modal),
// Cancel the editor, Run now on automation B, then pick a folder.
func TestPickedPathRefusedAfterFormReplaced(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.ae.load(automation.Automation{Label: "A", WatchDir: `C:\a`})
	editor := aeOpenForm(u) // ...and Browse is clicked: runPick pins this session

	u.closeModal() // the user cancels the editor while the dialog stands open
	u.ar.mu.Lock()
	u.ar.autoID, u.ar.label = "B", "B"
	u.ar.mu.Unlock()
	runB := u.openModalAs(arOwner, u.arModalHTML()) // ...and opens Run now for automation B

	pickBack(u, editor, "auto-ed:watch", `C:\picked`)

	u.ae.mu.Lock()
	watch := u.ae.watch
	u.ae.mu.Unlock()
	if watch == `C:\picked` {
		t.Fatal("the picked folder was written into the cancelled editor's form state")
	}
	if u.modalCur() != runB {
		t.Fatal("the picker's redispatch claimed the modal slot and clobbered the run-now modal")
	}
	if !strings.Contains(u.arModalHTML(), "B") {
		t.Fatal("run-now's modal no longer renders automation B")
	}
}

// TestPickedPathBindsToStepNotIndex is the second HIGH, and it needs no second entity to bite: the
// act carried a BARE INDEX into whatever chain was loaded. Browse on step 1's output folder, then -
// with the dialog still up - delete step 0. Index 1 is now a different step; the bounds check
// passes; the folder lands on the wrong one.
func TestPickedPathBindsToStepNotIndex(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.ae.load(automation.Automation{Label: "A", WatchDir: `C:\a`, Actions: []automation.Action{
		{Type: automation.ActionCopy, OutputDir: `C:\copy`},
		{Type: automation.ActionMove, OutputDir: `C:\move`},
	}})
	tok := aeOpenForm(u)
	copyKey, moveKey := aeKeyAt(u, 0), aeKeyAt(u, 1)

	// Browse on the MOVE step (index 1 as rendered), then remove the copy step above it.
	u.aeRemove(tok, copyKey)

	pickBack(u, tok, "auto-ed-af:"+aeK(moveKey)+":dir", `C:\picked`)

	u.ae.mu.Lock()
	defer u.ae.mu.Unlock()
	if len(u.ae.acts) != 1 {
		t.Fatalf("chain = %d steps, want 1", len(u.ae.acts))
	}
	if got := u.ae.acts[0].act; got.Type != automation.ActionMove || got.OutputDir != `C:\picked` {
		t.Fatalf("the folder did not follow the step it was picked for: %+v", got)
	}
}

// TestPickedPathDroppedWhenItsStepIsGone: same window, but the step that opened the dialog is the
// one deleted. There is no step to apply to - the position it held belongs to somebody else now, so
// the pick is dropped rather than written to whatever moved up.
func TestPickedPathDroppedWhenItsStepIsGone(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.ae.load(automation.Automation{Label: "A", WatchDir: `C:\a`, Actions: []automation.Action{
		{Type: automation.ActionCopy, OutputDir: `C:\copy`},
		{Type: automation.ActionMove, OutputDir: `C:\move`},
	}})
	tok := aeOpenForm(u)
	copyKey := aeKeyAt(u, 0)
	u.aeRemove(tok, copyKey) // the step that asked is deleted while its dialog is up

	pickBack(u, tok, "auto-ed-af:"+aeK(copyKey)+":dir", `C:\picked`)

	u.ae.mu.Lock()
	defer u.ae.mu.Unlock()
	if u.ae.acts[0].act.OutputDir != `C:\move` {
		t.Fatalf("a dead step's picked folder landed on the step that took its index: %+v", u.ae.acts[0].act)
	}
}

// TestPickedPathAppliesToTheFormThatAskedForIt - the guard has to let the ordinary case through, or
// Browse is just broken. Nothing moved: the path lands and the field shows it.
func TestPickedPathAppliesToTheFormThatAskedForIt(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.ae.load(automation.Automation{Label: "A"})
	tok := aeOpenForm(u)

	pickBack(u, tok, "auto-ed:watch", `C:\picked`)

	u.ae.mu.Lock()
	watch := u.ae.watch
	u.ae.mu.Unlock()
	if watch != `C:\picked` {
		t.Fatalf("watch dir = %q, want the picked folder", watch)
	}
	if !strings.Contains(u.aeModalHTML(), `C:\picked`) {
		t.Fatal("the picked folder is in state but the form does not show it")
	}
}

// TestPickApplyIsOneShot: the page is handed a request NUMBER, never the token. Replaying it (or
// inventing one) finds nothing - the session it named was consumed by the real return trip.
func TestPickApplyIsOneShot(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.ae.load(automation.Automation{Label: "A"})
	tok := aeOpenForm(u)

	id := itoa64(u.pickPut(pickReq{tok, "auto-ed:watch"}))
	u.pickApply(id, `C:\first`)
	u.pickApply(id, `C:\replayed`)

	u.ae.mu.Lock()
	defer u.ae.mu.Unlock()
	if u.ae.watch != `C:\first` {
		t.Fatalf("watch = %q - a replayed pick-apply re-ran the request", u.ae.watch)
	}
}

// TestPickedPathRefusedForAnUnguardedModal drives the picker gate on a target that has NO guard of
// its own (re-dest, an openModal dialog nobody re-renders), so only pickApply stands between the
// path and the wrong entity. It is also the sharpest form of the anonymous-identity bug: the user
// never closes anything - a second Re-encode REPLACES the first modal - and the old slot kept its
// generation on a same-owner claim while the anonymous owner was "", so the pin still matched.
func TestPickedPathRefusedForAnUnguardedModal(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.re.mu.Lock()
	u.re.kind, u.re.src, u.re.preset, u.re.dest = "dir", `C:\A`, "remux", `C:\A-remux`
	u.re.mu.Unlock()
	u.openModal(u.reencModalHTML())
	tok := u.modalCur() // Browse clicked on A's destination field

	// Without closing it, the user re-encodes a different folder: the modal is simply replaced.
	u.re.mu.Lock()
	u.re.src, u.re.dest = `C:\B`, `C:\B-remux`
	u.re.mu.Unlock()
	u.openModal(u.reencModalHTML())

	pickBack(u, tok, "re-dest", `C:\picked`)

	u.re.mu.Lock()
	defer u.re.mu.Unlock()
	if u.re.dest != `C:\B-remux` {
		t.Fatalf("a folder picked for C:\\A became the destination for C:\\B: %q", u.re.dest)
	}
}

// TestRunNowFileRefusedAfterSessionReplaced: run-now's file picker is the sharpest case - the chain
// it feeds can end in a delete. Pick a file for automation A, cancel, re-open Run now for B: A's
// file must not become B's target.
func TestRunNowFileRefusedAfterSessionReplaced(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.ar.mu.Lock()
	u.ar.autoID, u.ar.label, u.ar.acts = "A", "A", []automation.Action{{Type: automation.ActionDelete}}
	u.ar.mu.Unlock()
	runA := u.openModalAs(arOwner, u.arModalHTML()) // Browse clicked here

	u.closeModal()
	u.ar.mu.Lock()
	u.ar.autoID, u.ar.label, u.ar.file = "B", "B", ""
	u.ar.mu.Unlock()
	u.openModalAs(arOwner, u.arModalHTML()) // same owner, new session: a different automation

	pickBack(u, runA, "auto-run-file", `C:\set.wav`)

	u.ar.mu.Lock()
	defer u.ar.mu.Unlock()
	if u.ar.file != "" {
		t.Fatalf("a file picked for automation A armed automation B's delete chain: %q", u.ar.file)
	}
}
