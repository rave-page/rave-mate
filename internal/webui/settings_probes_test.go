package webui

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/ui"
	"rave.page/mate/internal/vrdll"
)

// B4c gates: the settings probes lost their single `busy` flag + 10 s TTL. What has to hold now is
// (a) they run CONCURRENTLY, (b) a slow probe never delays a fast one and never gates the next
// refresh, (c) a batch that lands together triggers exactly ONE re-render, and (d) the DOM is
// unchanged - a probe in flight renders the last known values, never a pending/loading state, and a
// result renders identically whether it arrived before the render (old) or after it (new).

// probeUI builds a settings UI with no shell (patchMain's eval is a no-op) - the gates read the
// cache's own bookkeeping, which is the only witness that survives the eval queue's coalescing.
func probeUI(active string) *UI {
	return &UI{svc: ui.Services{Cfg: &config.Config{}}, active: active, stop: make(chan struct{})}
}

// swapProbeTable installs a synthetic probe table for one test (serially - the table is package
// state, like version.FeedURL in the golden gates).
//
// HAZARD, hit while writing these gates: the table is package state, so ANY other live *UI in the
// process - another test's session whose 1 Hz settings tick calls kickProbes - runs YOUR probes while
// yours is installed. Counting runs without checking whose UI asked made the pacing gate fail only in
// the full-package run ("slow probe ran 5 times, want 2"). Every counting probe is built with
// countProbe, which ignores foreign UIs.
func swapProbeTable(t *testing.T, tbl []probeSpec) {
	t.Helper()
	old := settingsProbeTable
	settingsProbeTable = tbl
	t.Cleanup(func() { settingsProbeTable = old })
}

// countProbe builds a probe that counts (and optionally delays) runs for MINE only.
func countProbe(mine *UI, key string, n *int32, body func()) probeSpec {
	return probeSpec{key: key, run: func(u *UI) bool {
		if u != mine {
			return false
		}
		atomic.AddInt32(n, 1)
		if body != nil {
			body()
		}
		return false
	}}
}

// waitProbesIdle waits until no probe is in flight.
func waitProbesIdle(t *testing.T, u *UI, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		u.probes.mu.Lock()
		idle := u.probes.pend == 0
		for _, s := range u.probes.slots {
			idle = idle && !s.live
		}
		u.probes.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("probes still in flight after %s", within)
}

func (u *UI) repatchCount() int {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.repatches
}

// TestProbesRunConcurrently: N probes of duration D finish in ~D, not N*D, and the batch asks for
// exactly one re-render. Pre-B4c one `busy` flag serialized the whole set behind the slowest member.
func TestProbesRunConcurrently(t *testing.T) {
	const n, d = 6, 60 * time.Millisecond
	var live, peak, runs int32
	u := probeUI("live") // not the settings tab: the re-render decision is what we count, not the DOM
	tbl := make([]probeSpec, 0, n)
	for i := 0; i < n; i++ {
		tbl = append(tbl, countProbe(u, "p"+string(rune('a'+i)), &runs, func() {
			cur := atomic.AddInt32(&live, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
					break
				}
			}
			time.Sleep(d)
			atomic.AddInt32(&live, -1)
		}))
	}
	swapProbeTable(t, tbl)

	t0 := time.Now()
	u.kickProbes()
	waitProbesIdle(t, u, 10*time.Second)
	elapsed := time.Since(t0)

	if elapsed > n*d/2 {
		t.Errorf("probes serialized: %v for %d x %v (concurrent should be ~%v)", elapsed, n, d, d)
	}
	if got := atomic.LoadInt32(&peak); got < n {
		t.Errorf("peak concurrency %d, want %d (every probe has its own goroutine)", got, n)
	}
	if got := u.repatchCount(); got != 1 {
		t.Errorf("re-renders = %d, want 1 (the last probe of the batch owns the patch)", got)
	}
	t.Logf("cold fill: %d probes x %v serial=%v concurrent=%v, peak=%d, re-renders=%d",
		n, d, n*d, elapsed.Round(time.Millisecond), peak, u.repatchCount())
}

// TestProbeSingleFlightIsPerProbe: a blocked probe is not restarted by the next kick, and it does NOT
// hold the others back - they re-run immediately. That pair is what replaced the TTL.
func TestProbeSingleFlightIsPerProbe(t *testing.T) {
	release := make(chan struct{})
	var slow, fast int32
	u := probeUI("live")
	swapProbeTable(t, []probeSpec{
		countProbe(u, "slow", &slow, func() { <-release }),
		countProbe(u, "fast", &fast, nil),
	})

	u.kickProbes()
	for atomic.LoadInt32(&slow) == 0 { // wait for the slow probe to be in flight
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 5; i++ { // five more demand kicks while it blocks
		u.kickProbes()
		time.Sleep(2 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&slow); got != 1 {
		t.Errorf("slow probe ran %d times, want 1 (its guard IS the rate limit)", got)
	}
	if got := atomic.LoadInt32(&fast); got < 5 {
		t.Errorf("fast probe ran %d times, want >=5 (no TTL, no shared busy flag)", got)
	}
	close(release)
	waitProbesIdle(t, u, 10*time.Second)
}

// TestProbeNoTTL: two back-to-back kicks re-probe. Pre-B4c the second was a no-op for 10 s.
func TestProbeNoTTL(t *testing.T) {
	var runs int32
	u := probeUI("live")
	swapProbeTable(t, []probeSpec{countProbe(u, "p", &runs, nil)})
	for i := 0; i < 3; i++ {
		u.kickProbes()
		waitProbesIdle(t, u, 5*time.Second)
	}
	if got := atomic.LoadInt32(&runs); got != 3 {
		t.Fatalf("probe ran %d times over 3 kicks, want 3", got)
	}
	if got := u.repatchCount(); got != 1 {
		t.Errorf("re-renders = %d, want 1: only the FIRST landing changes anything", got)
	}
}

// TestProbePacingIsCostProportional: the only gate left is a probe's OWN measured cost. A 20 ms probe
// may not restart for probeBudget x 20 ms; a free probe beside it keeps running at the demand rate.
// (Pre-B4c both were gated by one 10 s TTL, and the cheap one paid for the expensive one.)
func TestProbePacingIsCostProportional(t *testing.T) {
	const d = 20 * time.Millisecond
	var slow, free int32
	u := probeUI("live")
	swapProbeTable(t, []probeSpec{
		countProbe(u, "slow", &slow, func() { time.Sleep(d) }),
		countProbe(u, "free", &free, nil),
	})
	u.kickProbes()
	waitProbesIdle(t, u, 5*time.Second)

	deadline := time.Now().Add(probeBudget * d / 4) // well inside the slow probe's budget gap
	for time.Now().Before(deadline) {
		u.kickProbes()
		time.Sleep(2 * time.Millisecond)
	}
	waitProbesIdle(t, u, 5*time.Second)
	if got := atomic.LoadInt32(&slow); got != 1 {
		t.Errorf("slow probe ran %d times inside its own budget gap, want 1", got)
	}
	if got := atomic.LoadInt32(&free); got < 10 {
		t.Errorf("free probe ran %d times, want >=10 (demand rate, not gated by its sibling)", got)
	}

	time.Sleep(probeBudget * d) // past the gap: it must be eligible again, so this is a gap not a latch
	u.kickProbes()
	waitProbesIdle(t, u, 5*time.Second)
	if got := atomic.LoadInt32(&slow); got != 2 {
		t.Errorf("slow probe ran %d times after its gap elapsed, want 2", got)
	}
}

// TestProbeFrozenNeverRuns pins the fixture seam the golden gates rest on.
func TestProbeFrozenNeverRuns(t *testing.T) {
	var runs int32
	u := probeUI("settings")
	swapProbeTable(t, []probeSpec{countProbe(u, "p", &runs, nil)})
	freezeProbes(u)
	u.kickProbes()
	u.probeNow("p")
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 0 {
		t.Fatalf("frozen cache ran %d probes", got)
	}
	if !u.probeDone("p") {
		t.Fatal("freezeProbes must mark every probe landed (gates render off probeDone)")
	}
}

// TestProbeAsyncArrivalRendersIdentically is the DOM-identity gate for the async path: while a probe
// is in flight the pane renders the LAST KNOWN values (no pending/loading state exists), and once the
// result lands the pane is byte-identical to one rendered from a cache that had the same values all
// along - i.e. sync (old) and async (new) arrival are indistinguishable in the document.
func TestProbeAsyncArrivalRendersIdentically(t *testing.T) {
	pane := func(u *UI) string {
		u.setMu.Lock()
		u.setSec, u.setQuery = "libmedia", "" // ffmpeg/mpv install cards + the transcode gate live here
		u.setMu.Unlock()
		return setContentHTML(u.settingsContentState())
	}
	// A: ffmpeg present. Frozen, so nothing races the fixture.
	warm := setFixtureUI(true)
	before := pane(warm)

	// a probe goes in flight: the slot is untouched, so the pane must not move by one byte
	warm.probes.mu.Lock()
	warm.probes.slotOf(pkTools).live = true
	warm.probes.mu.Unlock()
	if pending := pane(warm); pending != before {
		t.Fatalf("a probe in flight changed the DOM (%d vs %d bytes) - the async path must render last-known values",
			len(pending), len(before))
	}

	// B: the probe lands with ffmpeg GONE (async arrival)
	warm.probes.mu.Lock()
	warm.probes.slotOf(pkTools).live = false
	warm.probes.tools = map[string]mediatools.Status{"fpcalc": {Installed: true, Path: `C:\tools\fpcalc.exe`}}
	warm.probes.mu.Unlock()
	async := pane(warm)
	if async == before {
		t.Fatal("the landed probe changed nothing - the fixture is inert")
	}

	// the same values, present BEFORE the first render (sync arrival, the pre-B4c shape)
	sync := setFixtureUI(true)
	sync.probes.mu.Lock()
	sync.probes.tools = map[string]mediatools.Status{"fpcalc": {Installed: true, Path: `C:\tools\fpcalc.exe`}}
	sync.probes.mu.Unlock()
	if want := pane(sync); async != want {
		t.Fatalf("async arrival != sync arrival:\n async: %d B\n sync:  %d B\n first diff at %d",
			len(async), len(want), probeFirstDiff(async, want))
	}
}

// TestProbeGatesWaitForTheirOwnProbe: a card gate renders only once ITS probe landed (pre-B4c: once
// ANY probe pass landed) - never off the placeholder zero state.
func TestProbeGatesWaitForTheirOwnProbe(t *testing.T) {
	u := probeUI("settings")
	if g := u.cardGate("fingerprint"); g != "" {
		t.Errorf("cold cache gated fingerprint with %q - the gate must not flash before its probe lands", g)
	}
	u.probes.mu.Lock()
	u.probes.slotOf(pkTools).done = true // tools landed: fpcalc missing
	u.probes.mu.Unlock()
	if g := u.cardGate("fingerprint"); g == "" {
		t.Error("fpcalc gate missing after the tools probe landed")
	}
	// the VR gate must not depend on the tools probe (that coupling was the old global `ready`)
	u2 := probeUI("settings")
	u2.probes.mu.Lock()
	u2.probes.slotOf(pkVR).done, u2.probes.vr = true, vrdll.Status{Installed: true}
	u2.probes.mu.Unlock()
	if g := u2.cardGate("vroverlay"); g != "" && !strings.Contains(g, "VR") {
		t.Errorf("unexpected vroverlay gate %q", g)
	}
}

// TestProbeChangeDetection: only a probe whose result moved may ask for a re-render.
func TestProbeChangeDetection(t *testing.T) {
	u := probeUI("live")
	if !u.commitDevs("midi", []string{"LoopBe"}) {
		t.Error("first device list must count as changed")
	}
	if u.commitDevs("midi", []string{"LoopBe"}) {
		t.Error("identical device list must not ask for a re-render")
	}
	if !u.commitDevs("midi", []string{"LoopBe", "Denon"}) {
		t.Error("appended device not detected")
	}
	if !u.commitDevs("midi", []string{"Denon", "LoopBe"}) {
		t.Error("reordered device list not detected (the picker renders the order)")
	}
	a := map[string]mediatools.Status{"ffmpeg": {Installed: true, Path: "a"}}
	if toolInstallChanged(a, map[string]mediatools.Status{"ffmpeg": {Installed: true, Path: "a"}}) {
		t.Error("identical tool snapshot flagged as changed")
	}
	if !toolInstallChanged(a, map[string]mediatools.Status{"ffmpeg": {Installed: true, Path: "b"}}) {
		t.Error("moved tool path not detected")
	}
}

// probeFirstDiff reports the first differing byte offset (-1 when equal).
func probeFirstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return min(len(a), len(b))
	}
	return -1
}

// ── numbers ──

// TestProbeRealDurations reports what the real probes cost on this box: the pre-B4c cold fill was
// their SUM (one serial goroutine), the B4c cold fill is their MAX (all concurrent).
func TestProbeRealDurations(t *testing.T) {
	u := probeUI("live")
	var sum, max time.Duration
	for _, p := range settingsProbeTable {
		t0 := time.Now()
		p.run(u)
		d := time.Since(t0)
		sum += d
		if d > max {
			max = d
		}
		t.Logf("probe %-13s %v", p.key, d.Round(10*time.Microsecond))
	}
	t.Logf("cold fill: serial(old)=%v concurrent(new)~=%v", sum.Round(10*time.Microsecond), max.Round(10*time.Microsecond))
}

// BenchmarkProbeKickNew: the handler-lane cost of the demand kick (8 single-flight checks + 8
// goroutine spawns) with no-op probes.
func BenchmarkProbeKickNew(b *testing.B) {
	tbl := make([]probeSpec, 0, len(settingsProbeTable))
	for _, p := range settingsProbeTable {
		tbl = append(tbl, probeSpec{key: p.key, run: func(*UI) bool { return false }})
	}
	old := settingsProbeTable
	settingsProbeTable = tbl
	defer func() { settingsProbeTable = old }()
	u := probeUI("live")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u.kickProbes()
	}
	b.StopTimer()
	for i := 0; i < 200 && u.repatchCount() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	b.ReportMetric(float64(len(tbl)), "probes")
}

// BenchmarkProbeKickLegacy: the pre-B4c kick shape for comparison - one mutex round trip, one TTL
// check, one goroutine for the whole serial pass.
func BenchmarkProbeKickLegacy(b *testing.B) {
	u := probeUI("live")
	var mu sync.Mutex
	var busy bool
	var at time.Time
	ready := false
	const ttl = 10 * time.Second
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mu.Lock()
		stale := !ready || time.Since(at) > ttl
		if !stale || busy {
			mu.Unlock()
			continue
		}
		busy = true
		mu.Unlock()
		u.bg(func() {
			mu.Lock()
			at, ready, busy = time.Now(), true, false
			mu.Unlock()
		})
	}
}

// BenchmarkSettingsStateColdProbes: the handler-lane cost of a full settings state build with a COLD
// cache, kick included - the number B4c must not regress.
func BenchmarkSettingsStateColdProbes(b *testing.B) {
	tbl := make([]probeSpec, 0, len(settingsProbeTable))
	for _, p := range settingsProbeTable {
		tbl = append(tbl, probeSpec{key: p.key, run: func(*UI) bool { return false }})
	}
	old := settingsProbeTable
	settingsProbeTable = tbl
	defer func() { settingsProbeTable = old }()
	u := setFixtureUI(true)
	u.probes.mu.Lock()
	u.probes.frozen = false // cold: every render kicks
	u.probes.mu.Unlock()
	u.setMu.Lock()
	u.setSec = "libmedia"
	u.setMu.Unlock()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := u.settingsState()
		if !st.Available {
			b.Fatal("state unavailable")
		}
	}
}
