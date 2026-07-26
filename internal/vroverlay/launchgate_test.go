package vroverlay

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

// Regression tests for the hard no-launch gate. Runtime.Init is the only launch-capable OpenVR entry
// (VR_InitInternal(VRApplication_Overlay) starts SteamVR), so "the supervise loop never calls Init"
// IS the property that keeps a headless box from being poked into launching SteamVR. A dev PC with
// SteamVR installed and no headset got it launched + crash-looping when the process gate failed open.

// gateRT counts Init calls and reports configurable HMD presence / availability.
type gateRT struct {
	fakeRT
	inits atomic.Int32
	hmd   bool
}

func (g *gateRT) HMDPresent() bool { return g.hmd }
func (g *gateRT) Available() bool  { return false } // never let the loop enter runConnected
func (g *gateRT) Init() error      { g.inits.Add(1); return nil }

// newGateManager builds a Manager whose Start loop can be run for real, with both launch-gate probes
// injected. cfg has no overlays so a connected session would do nothing anyway.
func newGateManager(t *testing.T, rt Runtime, hmd, server bool) (*Manager, *logbus.Bus) {
	t.Helper()
	log := logbus.New(64)
	m := New(log, nil, rt, func() config.VROverlayFeature { return config.VROverlayFeature{} }, nil)
	m.hmdProbe = func() bool { return hmd }
	m.serverProbe = func() bool { return server }
	return m, log
}

// runStart runs Start for d, then cancels and waits for it to return.
func runStart(t *testing.T, m *Manager, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Start(ctx) }()
	time.Sleep(d)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

// No HMD → Init (the launch-capable call) is NEVER reached, however long the loop runs, and the
// single INFO line names the reason.
func TestNoHMDNeverInits(t *testing.T) {
	rt := &gateRT{hmd: false}
	m, log := newGateManager(t, rt, false, true) // SteamVR "running": only the HMD gate can stop us
	runStart(t, m, 300*time.Millisecond)
	if n := rt.inits.Load(); n != 0 {
		t.Fatalf("Init called %d× with no HMD - that call launches SteamVR", n)
	}
	if !logHas(log, "no HMD present") && !logHas(log, "non-vr build") {
		t.Fatalf("no idle reason logged; log:\n%s", logDump(log))
	}
}

// The no-HMD idle logs ONCE, not once per re-check (the user sees one line, not a spam loop).
func TestNoHMDLogsOnce(t *testing.T) {
	m, log := newGateManager(t, &gateRT{hmd: false}, false, true)
	// hmdRecheckWait paces production; drive logIdle directly to prove the once-per-reason contract.
	last := ""
	for i := 0; i < 5; i++ {
		last = m.logIdle(last, idleNoHMD)
	}
	if n := logCount(log, "VR overlays idle"); n != 1 {
		t.Fatalf("logged %d idle lines for one unchanged reason, want 1:\n%s", n, logDump(log))
	}
	// A genuine reason change logs again.
	m.logIdle(last, idleNoServer)
	if n := logCount(log, "VR overlays idle"); n != 2 {
		t.Fatalf("reason change did not log; got %d lines:\n%s", n, logDump(log))
	}
}

// HMD present but SteamVR down → still no Init: we attach to a running SteamVR, we never start one.
func TestSteamVRDownNeverInits(t *testing.T) {
	rt := &gateRT{hmd: true}
	m, log := newGateManager(t, rt, true, false)
	runStart(t, m, 300*time.Millisecond)
	if n := rt.inits.Load(); n != 0 {
		t.Fatalf("Init called %d× with SteamVR down - that call launches it", n)
	}
	if !logHas(log, "SteamVR not running") {
		t.Fatalf("missing the won't-launch idle line:\n%s", logDump(log))
	}
}

// Both gates pass → Init IS attempted (the gate must not be a permanent VR kill switch).
func TestHMDAndServerUpInits(t *testing.T) {
	rt := &gateRT{hmd: true}
	m, _ := newGateManager(t, rt, true, true)
	runStart(t, m, 300*time.Millisecond)
	if rt.inits.Load() == 0 {
		t.Fatal("Init never attempted with an HMD present and SteamVR up")
	}
}

// A GPU-reset reinit request must not turn into a launch on a headless box: RequestReinit only sets a
// flag consumed by a CONNECTED session, and the reconnect it triggers re-enters the gated loop head.
func TestReinitRequestNeverInitsWithoutHMD(t *testing.T) {
	rt := &gateRT{hmd: false}
	m, _ := newGateManager(t, rt, false, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Start(ctx) }()
	for i := 0; i < 20; i++ { // hammer the TDR fan-out while the loop idles
		m.RequestReinit("test-tdr")
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if n := rt.inits.Load(); n != 0 {
		t.Fatalf("recovery path reached Init %d× on a headless box", n)
	}
}

// An idle (no-HMD) supervise loop MUST keep beating: the featurehost host kills a child that stops
// pinging for vrHeartbeat (45s). The first cut of this gate slept the whole 60s re-check between
// beats, so the host force-restarted a perfectly healthy idle vr child every 45s.
func TestIdleLoopKeepsBeating(t *testing.T) {
	prev := idleBeatSlice
	idleBeatSlice = 5 * time.Millisecond // shrink the slice; hmdRecheckWait stays production-sized
	t.Cleanup(func() { idleBeatSlice = prev })

	var beats atomic.Int32
	m, _ := newGateManager(t, &gateRT{hmd: false}, false, true)
	m.SetBeat(func() { beats.Add(1) })
	runStart(t, m, 300*time.Millisecond)
	if n := beats.Load(); n < 5 {
		t.Fatalf("idle loop beat %d× in 300ms - the host would kill it as hung", n)
	}
}

// ── log helpers ──

func logDump(l *logbus.Bus) string {
	var b strings.Builder
	for _, e := range l.Snapshot() {
		b.WriteString(e.Msg)
		b.WriteByte('\n')
	}
	return b.String()
}

func logHas(l *logbus.Bus, sub string) bool { return logCount(l, sub) > 0 }

func logCount(l *logbus.Bus, sub string) int {
	n := 0
	for _, e := range l.Snapshot() {
		if strings.Contains(e.Msg, sub) {
			n++
		}
	}
	return n
}
