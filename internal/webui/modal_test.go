package webui

// Modal-slot identity (ui.go modalTok). The rule under test: work that finishes off the actWorker
// may only patch the modal it actually owns. Every case below is a real sequence a user can drive.

import "testing"

// TestModalTokenSurvivesOwnRerender pins the reason identity is (owner, gen) and not a bare
// counter: a form re-renders its own modal on every field change, and its outstanding token must
// stay valid across all of them.
func TestModalTokenSurvivesOwnRerender(t *testing.T) {
	u := &UI{} // nil shell: the eval is a no-op, so this exercises the slot bookkeeping alone
	tok := u.openModalAs("auto-run", "<a>")
	for i := 0; i < 5; i++ {
		if got := u.openModalAs("auto-run", "<b>"); got != tok {
			t.Fatalf("re-render %d minted a new session: %+v, want %+v", i, got, tok)
		}
	}
	if !u.openModalIf(tok, "<c>") {
		t.Fatal("openModalIf refused the owner's own live session")
	}
	if !u.closeModalIf(tok) {
		t.Fatal("closeModalIf refused the owner's own live session")
	}
}

// TestCompletionCannotDestroyAnotherFeaturesModal is the reported bug, at the primitive: a run-now
// modal is cancelled, the user opens the automations editor, and the run's completion lands. The
// completion must not be able to close what is now someone else's modal.
func TestCompletionCannotDestroyAnotherFeaturesModal(t *testing.T) {
	u := &UI{}
	run := u.openModalAs("auto-run", "<run>")   // user starts a 40-minute run
	u.closeModal()                              // user hits Cancel (modal-close)
	editor := u.openModalAs("auto-ed", "<new>") // user clicks New and starts typing

	if u.closeModalIf(run) {
		t.Fatal("the run's completion closed the editor modal - the unsaved chain would be gone")
	}
	if u.openModalIf(run, "<run-done>") {
		t.Fatal("the run's completion patched the editor modal")
	}
	if u.openModalIfOwner("auto-run", "<run-done>") {
		t.Fatal("openModalIfOwner patched a modal owned by auto-ed")
	}
	// The editor is untouched and still owns the slot.
	if !u.openModalIf(editor, "<still-mine>") {
		t.Fatal("the editor lost its own session")
	}
}

// TestUserCloseInvalidatesToken: a plain modal-close (scrim / ✕ / Cancel) is always authoritative
// and must strand every outstanding token, even with nothing opened afterwards.
func TestUserCloseInvalidatesToken(t *testing.T) {
	u := &UI{}
	tok := u.openModalAs("auto-run", "<run>")
	u.closeModal()
	if u.closeModalIf(tok) {
		t.Fatal("closeModalIf fired against an already-closed slot")
	}
	if u.openModalIf(tok, "<x>") {
		t.Fatal("openModalIf re-opened a modal the user had closed")
	}
}

// TestLaterSessionOfSameOwnerIsDistinct: run A is cancelled and run-now is re-opened for
// automation B. A's completion must not close B's modal - the owner matches, the session does not.
func TestLaterSessionOfSameOwnerIsDistinct(t *testing.T) {
	u := &UI{}
	runA := u.openModalAs("auto-run", "<A>")
	u.closeModal()
	runB := u.openModalAs("auto-run", "<B>")
	if runA == runB {
		t.Fatal("a re-opened modal reused the cancelled session's token")
	}
	if u.closeModalIf(runA) {
		t.Fatal("run A's completion closed run B's modal")
	}
	// ...but A's completion MAY refresh B: B's footer gates on state A just cleared.
	if !u.openModalIfOwner("auto-run", "<B-refreshed>") {
		t.Fatal("openModalIfOwner refused to refresh the live run-now modal")
	}
	if !u.closeModalIf(runB) {
		t.Fatal("run B lost its own session")
	}
}

// TestAnonymousOpenTakesTheSlot: an unguarded openModal still replaces what is on screen, so an
// owned modal's tokens must go stale - the owner's modal is no longer the thing the user sees.
func TestAnonymousOpenTakesTheSlot(t *testing.T) {
	u := &UI{}
	tok := u.openModalAs("auto-run", "<run>")
	u.openModal("<some-other-dialog>")
	if u.closeModalIf(tok) {
		t.Fatal("the run's completion closed an unrelated dialog")
	}
}

// TestClaimModalRefusesWhenSlotMoved guards the open-after-a-bbolt-read path (arOpen/aeEdit/asNew):
// the click pins the slot, and if anything claimed it while the store was read, the open is dropped
// rather than clobbering whatever the user moved on to. The LOAD must be dropped with it: build is
// where the form's working copy is written, and a render dropped after the write does not un-write
// it (tfEditOpen seeded a new track with the old track's tags exactly that way).
func TestClaimModalRefusesWhenSlotMoved(t *testing.T) {
	u := &UI{}
	prev := u.modalCur() // captured on the actWorker, at click time

	// The user opens something else while Get is still in flight.
	other := u.openModalAs("auto-ed", "<editor>")

	built := false
	if _, ok := u.claimModalWith(prev, "auto-run", func() string { built = true; return "<run>" }); ok {
		t.Fatal("a stale load claimed the slot and destroyed the editor modal")
	}
	if built {
		t.Fatal("the refused open still ran its load - the editor's form state is now the run's")
	}
	if !u.openModalIf(other, "<editor-alive>") {
		t.Fatal("the editor lost its session to a load that should have been dropped")
	}

	// The uncontended case still works: nothing moved between the pin and the claim.
	u.closeModal()
	prev = u.modalCur()
	tok, ok := u.claimModalWith(prev, "auto-run", func() string { return "<run>" })
	if !ok {
		t.Fatal("claimModalWith refused an uncontended slot")
	}
	if !u.closeModalIf(tok) {
		t.Fatal("the token claimModalWith returned does not own the modal it opened")
	}
}

// TestEmptySlotIsNotAnIdentity: the empty slot must never compare equal to an occupied one. It did
// - the anonymous owner was "" and so was the empty slot's, and a same-owner claim kept the
// generation - so a pin taken with nothing open still matched after an unrelated dialog opened, and
// a load pinned to "no modal" would claim the slot straight out from under it.
func TestEmptySlotIsNotAnIdentity(t *testing.T) {
	u := &UI{}
	empty := u.modalCur()
	if empty.live() {
		t.Fatal("an empty slot names a session")
	}
	u.openModal("<some-dialog>") // anonymous, unguarded - still what the user is looking at
	if u.modalCur() == empty {
		t.Fatal("an open dialog is indistinguishable from an empty slot")
	}
	if _, ok := u.claimModalWith(empty, "auto-ed", func() string { return "<editor>" }); ok {
		t.Fatal("a load pinned to an empty slot claimed it back from a live dialog")
	}
	// ...and the zero token is never a licence to patch or close whatever happens to be up.
	if u.openModalIf(empty, "<x>") || u.closeModalIf(empty) {
		t.Fatal("the zero token owns a live modal")
	}
}

// TestTwoAnonymousDialogsAreDistinct: openModal takes the slot without handing back a token, so
// nothing re-renders it - which is exactly why each open must still be its own identity. Two of
// them sharing one would let a pin taken across the first match the second.
func TestTwoAnonymousDialogsAreDistinct(t *testing.T) {
	u := &UI{}
	u.openModal("<confirm-delete>")
	first := u.modalCur()
	u.openModal("<save-preset>")
	if u.modalCur() == first {
		t.Fatal("two unrelated anonymous dialogs share one session")
	}
	if u.closeModalIf(first) {
		t.Fatal("the first dialog's pin closed the second")
	}
}

// TestUpdateModalIfGatesTheWrite pins the half the render guard missed: state. A refused update
// must not run mutate at all - guarding only the DOM leaves the working copy corrupted and the
// next re-render paints the damage.
func TestUpdateModalIfGatesTheWrite(t *testing.T) {
	u := &UI{}
	stale := u.openModalAs("auto-ed", "<A>")
	u.closeModal()
	live := u.openModalAs("auto-ed", "<B>") // same owner, new session: B is a different automation

	wrote := false
	if u.updateModalIf(stale, func() string { wrote = true; return "<A'>" }) {
		t.Fatal("a write pinned to the cancelled form was applied to its successor")
	}
	if wrote {
		t.Fatal("updateModalIf ran the mutation before checking the session")
	}
	if !u.updateModalIf(live, func() string { return "" }) {
		t.Fatal("the live form cannot write to itself")
	}
}
