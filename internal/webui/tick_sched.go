package webui

import (
	"strings"

	"rave.page/mate/internal/zigui"
)

// Fragment-tick scheduler (phase B3). The ~1 Hz tick used to cross the ABI once per FRAGMENT:
// marshal that fragment's state to JSON, call its render export, dedup the returned string
// against u.frags. Here the surface's whole state crosses ONCE as an RZW1 document carrying the
// hash of what we last pushed per fragment id; the Zig side renders every fragment, suppresses
// the unchanged ones and returns only what changed (native/zigui/src/tick.zig). Unchanged
// fragment HTML never crosses the ABI, is never jsQuote'd and never enters the eval queue.
//
// Dedup semantics are tickPatch's, moved across the wire: same bytes → suppressed, and a
// patchMain (DOM replaced) drops the cache so everything is resent. u.fragH holds the hashes,
// u.fragGen counts the drops - a batch built before a drop is discarded and the legacy path runs
// that tick, so no fragment can be silently withheld from a fresh DOM.
//
// Piloted surfaces: the Live tab tick + the #log-view tick. Every other tick keeps the legacy
// path untouched. Design notes: .devnotes/ZIG_UI_GUIDE.md "Phase B — B3 fragment scheduler".

// tickPrev is one fragment's last-pushed hash (id + the lib's Wyhash-64 of the HTML).
type tickPrev struct {
	ID   string `json:"id"`
	Hash uint64 `json:"hash"`
}

// liveTickSt is the Live tick surface: every ticked fragment's state (as the liveState the tab
// renderer already uses) plus the raw timecode text, which is a TEXT fragment no renderer owns
// (#live-tc is patched with htmlEscape(tcText()) whether or not the transport shows a timecode).
type liveTickSt struct {
	Live liveState  `json:"live"`
	TC   string     `json:"tc"`
	Prev []tickPrev `json:"prev"`
}

// logsTickSt is the #log-view tick surface (one fragment, 400-line tail).
type logsTickSt struct {
	Lines logsLines  `json:"lines"`
	Prev  []tickPrev `json:"prev"`
}

// liveTickIDs are the Live tick's fragment ids in patch order - the order live_ticks.go pushed
// them and the order the Zig scheduler emits them. Conditional fragments (rec-state, signals,
// cockpit, link, net/tim, perf) simply never come back when their service is absent; sending a
// prev slot for them regardless costs one entry and keeps this list a plain constant.
var liveTickIDs = []string{
	"live-tc", "live-rec-state", "live-np", "live-status", "live-decks", "live-signals",
	"live-cockpit", "live-ablelink", "live-net", "live-tim", "live-perf2", "live-strip",
}

// logsTickIDs is the #log-view surface's single fragment.
var logsTickIDs = []string{"log-view"}

// liveTickState resolves every fragment the Live tick patches. The has* flags carry the SAME
// `u.svc.X != nil` conditions the old tick branched on, so they double as the scheduler's
// fragment-presence flags; the static chrome (titles, section tooltips) is deliberately left
// empty - the tick patches fragment interiors, never the section headers, and building five
// tooltip cards per second to throw them away is exactly the waste this pilot removes.
func (u *UI) liveTickState() liveTickSt {
	st := liveTickSt{TC: u.tcText()}
	st.Live.Transport = u.liveTransportState()
	st.Live.NP = u.liveNPState()
	st.Live.Status = u.liveStatusState()
	st.Live.Decks = u.liveDecksState()
	st.Live.Strip = u.liveStripState()
	st.Live.Signals = liveSignalsSt{Rows: []liveKV{}}
	st.Live.Cockpit = liveCockpitSt{Rows: []liveCockpitRow{}}
	st.Live.Link = liveLinkSt{Sources: []liveSRow{}}
	if u.svc.Session != nil {
		st.Live.HasSignals, st.Live.Signals = true, u.liveSignalsState()
	}
	if u.svc.OBSControl != nil {
		st.Live.HasCockpit, st.Live.Cockpit = true, u.liveCockpitState()
	}
	if u.svc.AbleLink != nil {
		st.Live.HasLink, st.Live.Link = true, u.liveLinkState()
	}
	if u.svc.NetStats != nil {
		st.Live.HasNet, st.Live.Net, st.Live.Tim = true, u.liveNetState(), u.liveTimState()
	}
	if u.svc.Perf != nil {
		st.Live.HasPerf, st.Live.Perf = true, u.livePerfState()
	}
	return st
}

// tickPrevs snapshots the last-pushed hash for ids together with the cache generation the
// snapshot belongs to. Ids we have never pushed are omitted (absent = "always emit").
func (u *UI) tickPrevs(ids []string) ([]tickPrev, uint64) {
	u.fragMu.Lock()
	defer u.fragMu.Unlock()
	out := make([]tickPrev, 0, len(ids))
	for _, id := range ids {
		if h, ok := u.fragH[id]; ok {
			out = append(out, tickPrev{ID: id, Hash: h})
		}
	}
	return out, u.fragGen
}

// commitFrags stores the batch's hashes and reports whether the cache is still the one the batch
// was built against. false = a patchMain / eval-queue overflow dropped the cache mid-tick: the
// suppressed fragments are no longer in the DOM, so the batch is discarded and the caller runs
// the legacy path (which re-renders and re-pushes everything) for this tick.
func (u *UI) commitFrags(gen uint64, frs []zigui.Frag) bool {
	u.fragMu.Lock()
	defer u.fragMu.Unlock()
	if gen != u.fragGen {
		return false
	}
	if u.fragH == nil {
		u.fragH = make(map[string]uint64, len(frs))
	}
	for _, f := range frs {
		u.fragH[f.ID] = f.Hash
	}
	return true
}

// tickBatch runs one surface through the scheduler: build the document, one ABI call, commit the
// hashes. ok=false → the caller runs the legacy per-fragment path for this tick.
func (u *UI) tickBatch(doc []byte, name string, run func([]byte) ([]zigui.Frag, bool), gen uint64) ([]zigui.Frag, bool) {
	if len(doc) == 0 {
		zigui.NoteWireFallback(name) // encoder refused (over-size) - never reached the ABI
		return nil, false
	}
	frs, ok := run(doc)
	if !ok {
		return nil, false
	}
	if !u.commitFrags(gen, frs) {
		return nil, false
	}
	return frs, true
}

// tickLiveSched renders + patches the Live tick's fragments in one call. false = declined (stub
// build, malformed document, or a cache drop mid-tick) and the caller must run the legacy path.
func (u *UI) tickLiveSched(js *strings.Builder, st liveTickSt) bool {
	if !zigui.Available() {
		return false
	}
	prev, gen := u.tickPrevs(liveTickIDs)
	st.Prev = prev
	// --- phaseb-retain ---
	// B7 (ii): the tick's state crosses as a DELTA when the slot already holds last tick's state.
	// Most of the Live cockpit is static from second to second (decks, cockpit, link, strip), so a
	// tick usually changes three fragments' worth of fields: the delta is 564 B against a 3 173 B
	// document (18.0%). When NOTHING changed the delta is empty and neither the ABI nor a render is
	// touched. Bench: -5% dispatch (inside the noise band), -67.0% allocated bytes on a changed
	// tick, -84.8% on an unchanged one (PHASEB_BASELINE "Phase B7 (ii)").
	frs, rok := u.retained().tickLive.send(st)
	ok := rok && u.commitFrags(gen, frs)
	if !ok {
		frs, ok = u.tickBatch(wireTkLive(st), "TickLive", zigui.TickLive, gen)
	}
	// --- end phaseb-retain ---
	if !ok {
		return false
	}
	for _, f := range frs {
		u.recordPatch(js, f.ID, f.HTML)
	}
	return true
}

// recordPatch queues one __patch call exactly as tickPatch does (same JS, same coalescing key,
// same per-batch pend list flushTick drains) - the dedup already happened in Zig.
func (u *UI) recordPatch(js *strings.Builder, id, html string) {
	call := "window.__patch('" + id + "'," + jsQuote(html) + ");"
	u.fragMu.Lock()
	if u.tickPend == nil {
		u.tickPend = map[*strings.Builder][]evalEntry{}
	}
	u.tickPend[js] = append(u.tickPend[js], evalEntry{key: id, js: call})
	u.fragMu.Unlock()
	js.WriteString(call)
}

// tickLogsSched patches #log-view through the scheduler. The tail is re-rendered on the Zig side
// either way (the seq gate in the tick decided it might have changed), but an unchanged tail is
// suppressed instead of swapped: the legacy path pushed ~50 kB of identical innerHTML whenever
// the ring advanced, even when the FILTERED view was byte-identical. Text selection survives that
// suppression - the same reason the seq gate exists.
func (u *UI) tickLogsSched(n int) bool {
	if !zigui.Available() {
		return false // stub build: don't resolve the tail twice (the legacy path does it)
	}
	return u.tickLogsSchedFrom(logsTickSt{Lines: u.logsLinesState(n)})
}

// tickLogsSchedFrom is tickLogsSched over an already-resolved tail (the parity gate drives it).
func (u *UI) tickLogsSchedFrom(st logsTickSt) bool {
	if !zigui.Available() {
		return false
	}
	prev, gen := u.tickPrevs(logsTickIDs)
	st.Prev = prev
	frs, ok := u.tickBatch(wireTkLogs(st), "TickLogs", zigui.TickLogs, gen)
	if !ok {
		return false
	}
	for _, f := range frs {
		u.evalLogView(f.HTML)
	}
	return true
}

// evalLogView swaps #log-view's inner HTML, preserving the scroll position unless the view was
// already tailing (autoscroll). Shared by the tick paths so the emitted JS cannot drift.
func (u *UI) evalLogView(html string) {
	u.eval("var lv=document.getElementById('log-view');if(lv){var ab=" + u.logAutoscrollJS() +
		"&&(lv.scrollHeight-lv.scrollTop-lv.clientHeight<40);lv.innerHTML=" +
		jsQuote(html) + ";if(ab)lv.scrollTop=lv.scrollHeight;}")
}
