package webui

import (
	"strconv"
	"strings"
)

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
			u.tickPatch(&js, "live-rec-state", htmlEscape(recSideText(u.svc.AudioRec.Status())))
		}
		u.tickPatch(&js, "live-np", u.nowPlayingHTML())
		u.tickPatch(&js, "live-status", u.liveStatusHTML())
		u.tickPatch(&js, "live-decks", u.decksHTML())
		if u.svc.Session != nil {
			u.tickPatch(&js, "live-signals", u.signalsHTML())
		}
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
		if u.svc.AbleLink != nil {
			u.pushAbleLink() // feed the client rAF phrase-bar interpolator (after the panel patch)
		}
	})

	onLiveTick("logs", func(u *UI) {
		// Rebuild only when the ring advanced (dedup keyed on bus+last-seq in the frags cache, so
		// patchMain's cache drop forces a repaint after DOM replace) - the unconditional 400-line
		// innerHTML swap each tick wiped text selection and burned CPU with nothing new. Filter
		// changes + logs-clear repaint #log-view themselves, so seq-only keying stays correct.
		u.logMu.Lock()
		sel := u.logBus
		u.logMu.Unlock()
		var seq uint64
		if bus := u.busFor(sel); bus != nil {
			if es := bus.Snapshot(); len(es) > 0 { // no exported Bus.Seq(); tail copy is cheap vs the HTML rebuild
				seq = es[len(es)-1].Seq
			}
		}
		key := sel + ":" + strconv.FormatUint(seq, 10)
		u.fragMu.Lock()
		if u.frags["log-seq"] == key {
			u.fragMu.Unlock()
			return
		}
		if u.frags == nil {
			u.frags = map[string]string{}
		}
		u.frags["log-seq"] = key
		u.fragMu.Unlock()
		// tail-follow: keep scroll unless already at bottom; autoscroll gates the follow entirely
		u.eval("var lv=document.getElementById('log-view');if(lv){var ab=" + u.logAutoscrollJS() +
			"&&(lv.scrollHeight-lv.scrollTop-lv.clientHeight<40);lv.innerHTML=" +
			jsQuote(u.logLinesHTML(logTailN)) + ";if(ab)lv.scrollTop=lv.scrollHeight;}")
	})

	// Automations + App Groups keep their v1 body refresh (not part of the parity fan-out).
	// Version-gate the automations rebuild: skip the whole autoBody build+patch unless the
	// service's autos/scheds/runs actually changed (mirrors the logs tick's seq guard - tickPatch
	// alone still rebuilt the full HTML every tick). patchMain's frags wipe forces one repaint
	// after a DOM replace, so a re-entered tab repaints once then idles.
	onLiveTick("automations", func(u *UI) {
		if u.svc.Automations == nil {
			return // unavailable state has no #auto-body; nothing to refresh (and autoBody would nil-deref)
		}
		ver := strconv.FormatUint(u.svc.Automations.Version(), 10)
		u.fragMu.Lock()
		if u.frags["auto-ver"] == ver {
			u.fragMu.Unlock()
			return
		}
		if u.frags == nil {
			u.frags = map[string]string{}
		}
		u.frags["auto-ver"] = ver
		u.fragMu.Unlock()
		var js strings.Builder
		u.tickPatch(&js, "auto-body", u.autoBody())
		u.flushTick(&js)
	})
	onLiveTick("appgroups", func(u *UI) {
		var js strings.Builder
		u.tickPatch(&js, "appgroups-body", u.appGroupsBody())
		u.flushTick(&js)
	})
}
