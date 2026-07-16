package webui

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/i18n"
)

// Generic native-picker contract (see WEBUI brief): any tab renders
// btn("Browse…","ghost","pick-dir:<targetAct>","") (or pick-file:<targetAct> /
// pick-save:<container>:<targetAct>, container = extension hint, may be empty). On OK the target
// action is re-dispatched with the chosen path as its value through the same full path a page
// click takes (built-in set:/toggle: prefixes + the registry). Cancel = no dispatch; error = toast.
//
// The dialog is NOT modal to our webview (pickers_windows.go passes hwndOwner 0 and pumps it on its
// own locked OS thread) and runPick allows 10 minutes, so this is the WIDEST window in the app: the
// user can drive the whole UI while it stands open - cancel the form that asked, open another
// entity, reorder the very step that asked - and the act name alone is then a lie. So a pick is
// bound to the UI context that opened it: runPick pins the modal session on the act lane BEFORE
// the dialog, and the path is applied only if that exact session is still on screen, under the
// token, on the act lane. Anything else is dropped with a toast rather than written somewhere new.

// pickReq is one dialog's return trip. Registered only once a path is in hand and popped by the act
// it schedules, so an entry lives for one redispatch and can never be replayed. The page is handed
// the request NUMBER only - the token stays in Go, so nothing the DOM says can forge a session.
type pickReq struct {
	tok    modalTok // the modal session on screen when the dialog opened (zero = none)
	target string   // the act to apply the path to
}

func init() {
	onPrefix("pick-dir:", func(u *UI, m actMsg) { u.runPick("dir", "", m.arg("pick-dir:")) })
	onPrefix("pick-file:", func(u *UI, m actMsg) { u.runPick("file", "", m.arg("pick-file:")) })
	onPrefix("pick-save:", func(u *UI, m actMsg) {
		container, target, ok := strings.Cut(m.arg("pick-save:"), ":")
		if !ok {
			return
		}
		u.runPick("save", container, target)
	})
	onPrefix("pick-apply:", func(u *UI, m actMsg) { u.pickApply(m.arg("pick-apply:"), m.Val) })
}

// runPick shows the native dialog off-thread, then schedules the chosen path back onto the act
// lane. Headless remote sessions refuse: a native dialog would pop on the CONTROLLED machine's
// desktop - the one surface remote mode must never touch.
func (u *UI) runPick(kind, container, target string) {
	if target == "" {
		return
	}
	if u.virtual() {
		u.toast(i18n.T("library.mirror.noPicker"))
		return
	}
	tok := u.modalCur() // on the act lane: the modal that owns the Browse button being clicked
	u.bg(func() {
		// Not u.actx(): browsing can take well over 30 s. A modal dialog can't be force-closed on
		// timeout anyway - the deadline only unblocks an abandoned call.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var (
			p   string
			err error
		)
		switch kind {
		case "dir":
			p, err = u.PickDirectory(ctx)
		case "file":
			p, err = u.PickFile(ctx)
		case "save":
			p, err = u.ChooseSavePath(ctx, "", container)
		}
		if err != nil {
			u.logErr("pick "+kind, err)
			u.toast("File dialog: " + err.Error())
			return
		}
		if p == "" || u.stopped() { // user cancelled, or the window is gone
			return
		}
		u.redispatch("pick-apply:"+strconv.FormatUint(u.pickPut(pickReq{tok, target}), 10), p)
	})
}

// pickPut registers a returning dialog's request and yields its id.
func (u *UI) pickPut(r pickReq) uint64 {
	u.pickMu.Lock()
	defer u.pickMu.Unlock()
	if u.pickReqs == nil {
		u.pickReqs = map[uint64]pickReq{}
	}
	u.pickSeq++
	u.pickReqs[u.pickSeq] = r
	return u.pickSeq
}

// pickTake pops a request. One shot: a replayed (or invented) id finds nothing.
func (u *UI) pickTake(id uint64) (pickReq, bool) {
	u.pickMu.Lock()
	defer u.pickMu.Unlock()
	r, ok := u.pickReqs[id]
	delete(u.pickReqs, id)
	return r, ok
}

// pickApply applies a picked path on the act lane - the same lane a click takes, so the session
// check and the write are one serialized step. The pick is refused unless the modal slot is
// EXACTLY as the dialog left it: the form that asked is what the path was chosen for, and applying
// it to whatever replaced it is data corruption on another entity. tok rides along into the target
// act so the handler re-checks it under the slot lock (a bg reload could still land in between).
func (u *UI) pickApply(idTx, path string) {
	id, err := strconv.ParseUint(idTx, 10, 64)
	if err != nil {
		return
	}
	r, ok := u.pickTake(id)
	if !ok {
		return
	}
	// Only a MODAL-owned picker guards on identity: applying its path to whatever replaced the form
	// would corrupt another entity. A page-level Browse (settings/library toolbar) captures the empty
	// slot (r.tok not live) and its target is a page field, not a modal - a modal opening during the
	// dialog must not spuriously refuse it.
	if r.tok.live() && u.modalCur() != r.tok {
		u.toast(i18n.T("picker.stale"))
		return
	}
	u.onActMsg(actMsg{Act: r.target, Val: path, tok: r.tok})
	u.patchMain() // path inputs outside the modal (settings/library toolbars) show the chosen value
}

// redispatch re-enters the action pipeline exactly as a page event would (covers the set:/toggle:
// built-ins and every registry handler). It routes through the page's bound rave() so the act is
// queued on the shell's acts chan and executes serialized on actWorker - never calls onAction from
// this background goroutine (concurrent handlers would race unsynchronized state, e.g. *config.Config).
func (u *UI) redispatch(act, val string) {
	b, err := json.Marshal(actMsg{Act: act, Val: val})
	if err != nil {
		return
	}
	u.eval("window.rave&&window.rave(" + jsQuote(string(b)) + ")")
}
