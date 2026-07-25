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
		// --- phaseb-sched ---
		// B3: resolve the whole surface once, then ONE call renders + dedups every fragment on
		// the Zig side (tick_sched.go). Declined (stub build, malformed document, cache dropped
		// mid-tick) → the legacy per-fragment path below, from the SAME state.
		st := u.liveTickState()
		if !u.tickLiveSched(&js, st) {
			u.liveTickLegacy(&js, st)
		}
		// --- end phaseb-sched ---
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
		// --- phaseb-sched ---
		// B3: the ring advanced, but the FILTERED tail often did not - one call renders it on the
		// Zig side and suppresses the swap when its bytes are unchanged (tick_sched.go). Declined
		// → the legacy render+swap below.
		if u.tickLogsSched(logTailN) {
			return
		}
		// --- end phaseb-sched ---
		// tail-follow: keep scroll unless already at bottom; autoscroll gates the follow entirely
		u.evalLogView(u.logLinesHTML(logTailN))
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

// --- phaseb-sched ---

// liveTickLegacy is the pre-B3 Live tick: one render + one tickPatch per fragment, deduped in Go
// against u.frags. It stays as the scheduler's fallback (stub builds, a declined batch) AND as
// the parity reference - tick_sched_test.go drives this and the scheduler from the SAME state and
// requires an identical ordered set of __patch calls. Fragment order + conditions are the wire
// contract with native/zigui/src/tick.zig runLive; change them in both or the gate fails.
func (u *UI) liveTickLegacy(js *strings.Builder, st liveTickSt) {
	u.tickPatch(js, "live-tc", htmlEscape(st.TC))
	if st.Live.Transport.HasRec {
		u.tickPatch(js, "live-rec-state", htmlEscape(st.Live.Transport.RecState))
	}
	u.tickPatch(js, "live-np", liveFrag("np", st.Live.NP, liveNPHTML))
	u.tickPatch(js, "live-status", liveFrag("status", st.Live.Status, liveStatusFragHTML))
	u.tickPatch(js, "live-decks", liveFrag("decks", st.Live.Decks, liveDecksFragHTML))
	if st.Live.HasSignals {
		u.tickPatch(js, "live-signals", liveFrag("signals", st.Live.Signals, liveSignalsFragHTML))
	}
	if st.Live.HasCockpit {
		u.tickPatch(js, "live-cockpit", liveFrag("cockpit", st.Live.Cockpit, liveCockpitFragHTML))
	}
	if st.Live.HasLink {
		u.tickPatch(js, "live-ablelink", liveFrag("link", st.Live.Link, liveLinkFragHTML))
	}
	if st.Live.HasNet {
		u.tickPatch(js, "live-net", liveFrag("graph", st.Live.Net, liveGraphFragHTML))
		u.tickPatch(js, "live-tim", liveFrag("graph", st.Live.Tim, liveGraphFragHTML))
	}
	if st.Live.HasPerf {
		u.tickPatch(js, "live-perf2", liveFrag("perf", st.Live.Perf, livePerfFragHTML))
	}
	u.tickPatch(js, "live-strip", liveFrag("strip", st.Live.Strip, liveStripFragHTML))
}

// --- end phaseb-sched ---
