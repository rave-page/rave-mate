package webui

import "strings"

// Live-tick registry: each tab that needs a ~1 Hz DOM refresh registers a fn in its own file's
// init() (onLiveTick) so parallel tab work never collides on the livePush switch. livePush calls
// the fn for the active tab each tick. Registration is init-time only (no concurrent writers).

var liveTicks = map[string]func(*UI){}

// onLiveTick registers tab's ~1 Hz refresh fn (last registration for a tab id wins).
func onLiveTick(tab string, fn func(*UI)) { liveTicks[tab] = fn }

// ── Live tab tick (owned here; parity renderer in render_live.go) ──

func init() {
	// One coalesced eval per tick (tickPatch also skips unchanged fragments) - per-fragment evals
	// are UI-thread ExecuteScript calls and made window dragging stutter.
	onLiveTick("live", func(u *UI) {
		var js strings.Builder
		u.tickPatch(&js, "live-tc", htmlEscape(u.tcText()))
		if u.svc.AudioRec != nil {
			u.tickPatch(&js, "live-rec-state", htmlEscape(recStateText(u.svc.AudioRec.Status())))
		}
		u.tickPatch(&js, "live-np", u.nowPlayingHTML())
		u.tickPatch(&js, "live-status", u.liveStatusHTML())
		u.tickPatch(&js, "live-decks", u.decksHTML())
		if u.svc.OBSControl != nil {
			u.tickPatch(&js, "live-cockpit", u.cockpitHTML())
		}
		if u.svc.AbleLink != nil {
			u.tickPatch(&js, "live-ablelink", u.ableLinkHTML())
		}
		if u.svc.NetStats != nil {
			u.tickPatch(&js, "live-net", u.networkHTML())
			u.tickPatch(&js, "live-tim", u.timingHTML())
		}
		if u.svc.Perf != nil {
			u.tickPatch(&js, "live-perf2", u.sysperfHTML())
		}
		u.tickPatch(&js, "live-strip", u.liveStripHTML())
		u.flushTick(&js)
	})

	onLiveTick("logs", func(u *UI) {
		// tail-follow: keep scroll unless already at bottom; autoscroll gates the follow entirely
		u.eval("var lv=document.getElementById('log-view');if(lv){var ab=" + u.logAutoscrollJS() +
			"&&(lv.scrollHeight-lv.scrollTop-lv.clientHeight<40);lv.innerHTML=" +
			jsQuote(u.logLinesHTML(logTailN)) + ";if(ab)lv.scrollTop=lv.scrollHeight;}")
	})

	// Automations + App Groups keep their v1 body refresh (not part of the parity fan-out).
	onLiveTick("automations", func(u *UI) { u.eval("window.__patch('auto-body'," + jsQuote(u.autoBody()) + ")") })
	onLiveTick("appgroups", func(u *UI) { u.eval("window.__patch('appgroups-body'," + jsQuote(u.appGroupsBody()) + ")") })
}
