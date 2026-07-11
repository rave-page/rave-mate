package webui

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"rave.page/mate/internal/i18n"
)

// Generic native-picker contract (see WEBUI brief): any tab renders
// btn("Browse…","ghost","pick-dir:<targetAct>","") (or pick-file:<targetAct> /
// pick-save:<container>:<targetAct>, container = extension hint, may be empty). On OK the target
// action is re-dispatched with the chosen path as its value through the same full path a page
// click takes (built-in set:/toggle: prefixes + the registry). Cancel = no dispatch; error = toast.

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
	// post-pick re-render, queued behind the redispatched target act so inputs show the new value
	onExact("pick-rerender", func(u *UI, _ actMsg) { u.patchMain() })
}

// runPick shows the native dialog off-thread, then re-dispatches target with the chosen path.
// Headless remote sessions refuse: a native dialog would pop on the CONTROLLED machine's
// desktop - the one surface remote mode must never touch.
func (u *UI) runPick(kind, container, target string) {
	if target == "" {
		return
	}
	if u.virtual() {
		u.toast(i18n.T("library.mirror.noPicker"))
		return
	}
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
		if p == "" { // user cancelled
			return
		}
		u.redispatch(target, p)
		u.redispatch("pick-rerender", "") // re-render so path inputs show the chosen value
	})
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
