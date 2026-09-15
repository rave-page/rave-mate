package webui

// The Live cockpit is the instrument an operator reads WHILE a set runs, and a set runs in exactly
// the state the activity governor closes the ~1 Hz tick for (streaming / unfocused). The whole tab
// then froze - the auto-live landmark and recorder state showed minutes-stale values. These gates
// pin the fix: while the general tick is withheld, the CRITICAL fragments still patch and the
// non-critical ones do not (repainting the graphs over a live encoder is the thing the gate exists
// to prevent). Mirrors peers_routetick_test.go for #peers-media.

import (
	"strings"
	"testing"

	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/ui"
)

// liveTickUI wires a headless UI on the Live tab. No eval flusher: the queue itself is what the tick
// decided to push, so the assertion reads exactly that (draining it would race the drain).
func liveTickUI(t *testing.T) (*UI, func() string) {
	t.Helper()
	vs := newVirtualShell(nil, func(string) {}, func(string) {})
	u := &UI{
		svc:      ui.Services{Log: logbus.New(64)},
		log:      logbus.New(64),
		shell:    vs,
		active:   "live",
		stop:     make(chan struct{}),
		evalKick: make(chan struct{}, 1),
	}
	t.Cleanup(func() { close(u.stop); vs.terminate() })
	return u, func() string {
		u.evalMu.Lock()
		defer u.evalMu.Unlock()
		var q strings.Builder
		for _, e := range u.evalQ {
			q.WriteString(e.js)
		}
		return q.String()
	}
}

// nonCriticalIDs are the Live fragments the governor's reason for closing the gate (don't repaint
// rave-mate's own graphs over a live encoder) still covers - they must stay frozen while streaming.
var nonCriticalIDs = []string{"live-np", "live-decks", "live-status", "live-strip", "live-net", "live-perf2"}

// TestLiveLandmarkPatchesWhileStreaming: with a stream live (the general tick withheld) the Live tab
// must still patch its landmark, and must NOT repaint the non-critical fragments. Pre-fix,
// livePushOnce returns before the "live" tick and nothing on the tab is emitted at all.
func TestLiveLandmarkPatchesWhileStreaming(t *testing.T) {
	restoreGovernor(t)
	u, evals := liveTickUI(t)

	governor.SetStreaming(true) // OBS is up - the exact state the cockpit froze in
	if governor.UIAnimAllowed() {
		t.Fatal("premise broken: the general tick is supposed to be withheld here")
	}

	u.livePushOnce()
	ev := evals()

	if !strings.Contains(ev, "window.__patch('live-stream-state'") {
		t.Fatalf("no landmark patch while streaming - the cockpit is frozen for the whole set: %q", ev)
	}
	for _, id := range nonCriticalIDs {
		if strings.Contains(ev, "window.__patch('"+id+"'") {
			t.Errorf("repainted non-critical fragment %q over a live encoder while streaming", id)
		}
	}
}

// TestLiveCriticalTickScopedToLiveTab: the exemption is narrow - on another tab nothing is emitted.
func TestLiveCriticalTickScopedToLiveTab(t *testing.T) {
	restoreGovernor(t)
	governor.SetStreaming(true)

	u, evals := liveTickUI(t)
	u.active = "settings" // not live, not peers
	u.livePushOnce()
	if ev := evals(); strings.Contains(ev, "live-stream-state") {
		t.Errorf("patched the Live landmark while another tab is on screen: %q", ev)
	}
}

// TestLiveGeneralTickPatchesNonCriticalWhenGateOpen is the positive control: with the gate OPEN the
// registered "live" tick owns the tab and DOES patch the non-critical fragments (plus the landmark),
// proving the streaming path above is a real subset, not a dead tab.
func TestLiveGeneralTickPatchesNonCriticalWhenGateOpen(t *testing.T) {
	restoreGovernor(t) // focused, nothing streaming → UIAnimAllowed
	if !governor.UIAnimAllowed() {
		t.Fatal("premise broken: the general tick should run here")
	}
	u, evals := liveTickUI(t)

	u.livePushOnce()
	ev := evals()
	if !strings.Contains(ev, "window.__patch('live-stream-state'") {
		t.Fatalf("general tick did not patch the landmark: %q", ev)
	}
	if !strings.Contains(ev, "window.__patch('live-np'") {
		t.Fatalf("general tick did not patch a non-critical fragment - the tab is dead: %q", ev)
	}
}
