package webui

import (
	"html"
	"strings"
)

// Streaming-honest Live tick (P14). The activity governor closes the general ~1 Hz Live tick for
// the WHOLE of a stream (OBS running flips governor.Streaming) and while the window is unfocused/
// dragged - so livePushOnce returns before the registered "live" tick ever runs, and the cockpit
// froze for the entire set: the auto-live landmark and the recorder state showed values that were
// minutes stale. This mirrors mediaRouteTick's narrow exemption (peers_routetick_test.go): local
// snapshot reads, hash-deduped via tickPatch (u.frags), ONLY the answer-at-a-glance fragments, no
// full-tab repaint, no disabled cache. The critical set grows as Phase A/D add route-health + OBS
// drop-ratio fragments; today it is the stream landmark + the recorder state.

// liveStreamStateHTML is #live-stream-state's interior: the auto-live dot + phrase. ONE source for
// the full-render span, the general tick and the streaming-critical tick, so the three never drift.
func liveStreamStateHTML(t liveTransportSt) string {
	return dot(t.DotVar) + " " + html.EscapeString(t.State)
}

// liveCriticalTick keeps the Live landmarks patching while the governor withholds the general tick.
// Scoped to the Live tab and to the fragments an operator reads at a glance mid-set.
func (u *UI) liveCriticalTick() {
	if u.activeTab() != "live" {
		return
	}
	ts := u.liveTransportState()
	var js strings.Builder
	// #live-stream-state lives inside #live-transport, which is not a tick fragment - without a
	// patch here it repaints only on a full render (the P3 landmark defect), and never while streaming.
	u.tickPatch(&js, "live-stream-state", liveStreamStateHTML(ts))
	if ts.HasRec {
		// recorder armed/duration/file - the general tick's #live-rec-state, kept honest while streaming.
		u.tickPatch(&js, "live-rec-state", html.EscapeString(ts.RecState))
	}
	// Route health / frozen-picture verdict: the single most streaming-critical fragment - a route
	// freezes DURING a stream, and every rate/volume counter reads healthy while it does (P3).
	if rs := u.liveRouteState(); len(rs.Rows) > 0 {
		u.tickPatch(&js, "live-route", liveFrag("route", rs, wireLiveRoute, liveRouteFragHTML))
	}
	// Armed tracklist recorder: the live duration must keep counting while streaming (you're
	// capturing the set DURING the stream), so this can't be version-gated shut - patch it here.
	if rc := u.liveRecCardState(); len(rc.Rows) > 0 {
		u.tickPatch(&js, "live-rec-card", liveFrag("reccard", rc, wireLiveRecCard, liveRecCardFragHTML))
	}
	// VRAM pressure: saturation happens DURING a stream (the 2026-09-11 incident: Resolume ate the
	// budget mid-set), which is exactly when the general tick is withheld - keep this meter honest.
	if v, ok := u.liveVramState(); ok {
		u.tickPatch(&js, "live-vram", liveFrag("vram", v, wireLiveVram, liveVramFragHTML))
	}
	u.flushTick(&js)
	u.freezeAbleLink() // P5: stop the client rAF phrase-bar loop the gated-shut general push left running
}

// freezeAbleLink pushes rate 0 to the client rAF 'link' runtime once per streaming episode, so the
// phrase bar stops interpolating while the general tick's push is withheld (P5). Idempotent-guarded
// by liveLinkAnim, which the general tick re-arms whenever it pushes a live rate.
func (u *UI) freezeAbleLink() {
	if u.svc.AbleLink == nil {
		return
	}
	u.fragMu.Lock()
	armed := u.liveLinkAnim
	u.fragMu.Unlock()
	if !armed {
		return
	}
	u.pushAbleLinkR(true) // pins rate 0 + clears liveLinkAnim
}

// setLinkAnim records whether the client rAF 'link' loop is currently running.
func (u *UI) setLinkAnim(v bool) {
	u.fragMu.Lock()
	u.liveLinkAnim = v
	u.fragMu.Unlock()
}
