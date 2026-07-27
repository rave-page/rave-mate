package webui

// The route panel is the instrument an operator reads WHILE a route runs, and the two moments it
// is read - a live stream, or a terminal driving `ctl` - are exactly the two the activity governor
// closes the ~1 Hz tick for (governor.UIAnimAllowed = focused && !minimized && !sizeMove &&
// !streaming; app.watchStreaming flips `streaming` on OBS being open at all). The counters then
// stopped advancing in the DOM for the whole stream: 25+ minutes of identical
// "frames · bytes · kf · drops" on a demonstrably running webcam route.
//
// These gates pin the fix: while the general tick is withheld, a LIVE route still patches
// #peers-media, and the rendered counters advance between ticks.

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/ui"
)

// tickMedia is a MediaControl whose route counters advance on every Stats() read - a route that is
// demonstrably running, so anything frozen downstream is the UI's doing.
type tickMedia struct{ n atomic.Uint64 }

func (m *tickMedia) Start(context.Context) error     { return nil }
func (m *tickMedia) Stop()                           {}
func (m *tickMedia) Advertise()                      {}
func (m *tickMedia) SetCodecCaps(_, _ []string)      {}
func (m *tickMedia) SetSyncPeer(string)              {}
func (m *tickMedia) Encoders() []string              { return nil }
func (m *tickMedia) SyncStats() []medialink.SyncStat { return nil }
func (m *tickMedia) ClockQuality() medialink.ClockQuality {
	return medialink.ClockQuality{Tier: medialink.TierMonotonic, Locked: true}
}
func (m *tickMedia) Stats() []medialink.RouteStat {
	n := m.n.Add(1)
	return []medialink.RouteStat{{
		Session: "s1", Peer: "B", Stream: 2, Direction: "send",
		Frames: 3467 + n, Bytes: 9_800_000 + n*2827, Keyframes: 54,
		Encoder: "mf-native", Tier: 1, RateBps: 950_000, WireFPS: 38,
		Pipe: &medialink.PipelineStats{Encoder: "mf-native", OutFPS: 38, Dropped: 41902 + n, RateCapped: 41902 + n},
	}}
}

// tickUI wires a UI onto a headless shell. NO eval flusher: the queue itself is what the tick
// decided to push, and draining it concurrently would make the assertion race the drain rather
// than the behaviour under test.
func tickUI(t *testing.T) (*UI, *tickMedia, func() string) {
	t.Helper()
	vs := newVirtualShell(nil, func(string) {}, func(string) {})
	m := &tickMedia{}
	u := &UI{
		svc:      ui.Services{Log: logbus.New(64), Media: m},
		log:      logbus.New(64),
		shell:    vs,
		active:   "peers",
		stop:     make(chan struct{}),
		evalKick: make(chan struct{}, 1),
	}
	t.Cleanup(func() { close(u.stop); vs.terminate() })
	return u, m, func() string {
		u.evalMu.Lock()
		defer u.evalMu.Unlock()
		var q strings.Builder
		for _, e := range u.evalQ {
			q.WriteString(e.js)
		}
		return q.String()
	}
}

// TestRouteCountersAdvanceWhileStreaming is the non-vacuous arm: with a stream live (the governor
// gate closed) two ticks must still push #peers-media, and the SECOND push must carry different
// counters from the first. On the pre-fix code livePush `continue`s and nothing is emitted at all.
func TestRouteCountersAdvanceWhileStreaming(t *testing.T) {
	restoreGovernor(t)
	u, _, evals := tickUI(t)

	governor.SetStreaming(true) // OBS is up - the exact state the panel froze in
	if governor.UIAnimAllowed() {
		t.Fatal("premise broken: the general tick is supposed to be withheld here")
	}

	u.livePushOnce()
	first := evals()
	u.livePushOnce()
	second := evals()

	if !strings.Contains(first, "window.__patch('peers-media'") {
		t.Fatalf("no route-counter patch while streaming - the panel is frozen for the whole stream: %q", first)
	}
	if first == second {
		t.Fatalf("the second tick pushed nothing new: rendered counters never advance while a stream is live\n%q", second)
	}
	// The rendered NUMBER, not just the patch call: a patch that re-pushes identical counters is
	// the same frozen panel with extra steps. (Queue entries coalesce newest-wins per fragment id,
	// so each read carries exactly the payload that tick decided on.)
	a, b := renderedFrames(t, first), renderedFrames(t, second)
	if b <= a {
		t.Fatalf("rendered frame count went %d → %d across two ticks", a, b)
	}
}

// renderedFrames pulls the "<n> frames" the queued patch carries.
func renderedFrames(t *testing.T, js string) int {
	t.Helper()
	m := regexp.MustCompile(`(\d+) frames`).FindStringSubmatch(js)
	if m == nil {
		t.Fatalf("no rendered frame count in the patch: %q", js)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The exemption is NARROW: no route, or another tab, and nothing is rendered - the governor's
// reason for closing the gate (don't repaint over a live encoder) still holds everywhere else.
func TestRouteTickIsScopedToALiveRouteOnPeers(t *testing.T) {
	restoreGovernor(t)
	governor.SetStreaming(true)

	u, _, evals := tickUI(t)
	u.active = "live"
	u.livePushOnce()
	if strings.Contains(evals(), "peers-media") {
		t.Error("patched the peers panel while another tab is on screen")
	}

	u2 := &UI{svc: ui.Services{Log: logbus.New(16)}, log: logbus.New(16), active: "peers",
		stop: make(chan struct{}), evalKick: make(chan struct{}, 1)}
	vs := newVirtualShell(nil, func(string) {}, func(string) {})
	u2.shell = vs
	t.Cleanup(func() { close(u2.stop); vs.terminate() })
	u2.livePushOnce() // no media service at all: must not panic, must not render
	u2.evalMu.Lock()
	n := len(u2.evalQ)
	u2.evalMu.Unlock()
	if n != 0 {
		t.Errorf("rendered %d evals with no media plane", n)
	}
}

// With the gate OPEN the normal peers tick owns the body, and it must invalidate the exempt
// fragment's dedup entry - otherwise the entry describes HTML the body patch replaced.
func TestPeersBodyTickDropsTheMediaFragEntry(t *testing.T) {
	restoreGovernor(t)
	u, _, _ := tickUI(t)
	u.fragMu.Lock()
	u.frags = map[string]string{"peers-media": "stale"}
	u.fragMu.Unlock()

	u.livePushOnce() // governor open → the registered "peers" tick runs
	u.fragMu.Lock()
	_, still := u.frags["peers-media"]
	u.fragMu.Unlock()
	if still {
		t.Fatal("the body patch left a dedup entry that no longer describes the DOM")
	}
}
