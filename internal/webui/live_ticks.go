package webui

// Live-tick registry: each tab that needs a ~1 Hz DOM refresh registers a fn in its own file's
// init() (onLiveTick) so parallel tab work never collides on the livePush switch. livePush calls
// the fn for the active tab each tick. Registration is init-time only (no concurrent writers).

var liveTicks = map[string]func(*UI){}

// onLiveTick registers tab's ~1 Hz refresh fn (last registration for a tab id wins).
func onLiveTick(tab string, fn func(*UI)) { liveTicks[tab] = fn }

// ── Live tab tick (owned here; parity renderer in render_live.go) ──

func init() {
	onLiveTick("live", func(u *UI) {
		u.eval("window.__patch('live-tc'," + jsQuote(htmlEscape(u.tcText())) + ")")
		if u.svc.AudioRec != nil {
			u.eval("window.__patch('live-rec-state'," + jsQuote(htmlEscape(recStateText(u.svc.AudioRec.Status()))) + ")")
		}
		u.eval("window.__patch('live-np'," + jsQuote(u.nowPlayingHTML()) + ")")
		u.eval("window.__patch('live-status'," + jsQuote(u.liveStatusHTML()) + ")")
		u.eval("window.__patch('live-decks'," + jsQuote(u.decksHTML()) + ")")
		if u.svc.OBSControl != nil {
			u.eval("window.__patch('live-cockpit'," + jsQuote(u.cockpitHTML()) + ")")
		}
		if u.svc.AbleLink != nil {
			u.eval("window.__patch('live-ablelink'," + jsQuote(u.ableLinkHTML()) + ")")
		}
		if u.svc.NetStats != nil {
			u.eval("window.__patch('live-net'," + jsQuote(u.networkHTML()) + ")")
			u.eval("window.__patch('live-tim'," + jsQuote(u.timingHTML()) + ")")
		}
		if u.svc.Perf != nil {
			u.eval("window.__patch('live-perf2'," + jsQuote(u.sysperfHTML()) + ")")
		}
		u.eval("window.__patch('live-strip'," + jsQuote(u.liveStripHTML()) + ")")
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
