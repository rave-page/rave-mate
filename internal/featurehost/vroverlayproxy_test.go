package featurehost

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/perfmon"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/vroverlay"
)

// sentFrame captures one proxy→child push.
type sentFrame struct {
	event string
	data  json.RawMessage
}

// newTestVrProxy builds a proxy whose sends are captured instead of hitting a pipe.
func newTestVrProxy(t *testing.T, deps VROverlayDeps) (*VrOverlayProxy, func() []sentFrame) {
	t.Helper()
	if deps.Cfg == nil {
		deps.Cfg = func() config.VROverlayFeature { return config.VROverlayFeature{} }
	}
	p, err := NewVrOverlayProxy(logbus.New(64), deps)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sent []sentFrame
	p.send = func(event string, data any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		mu.Lock()
		sent = append(sent, sentFrame{event: event, data: raw})
		mu.Unlock()
		return nil
	}
	return p, func() []sentFrame {
		mu.Lock()
		defer mu.Unlock()
		return append([]sentFrame(nil), sent...)
	}
}

// onReady re-pushes FULL desired state: world, campaths, stats (when a stats overlay is enabled),
// then the mirrored bus content (chat ring, last viewers, fresh obs status) - so a restarted child
// rebuilds every overlay without waiting for new traffic.
func TestVRProxyReadyRepush(t *testing.T) {
	cfg := config.VROverlayFeature{Enabled: true, Overlays: []config.VROverlay{
		{ID: "c", Type: "chat", Enabled: true},
		{ID: "p", Type: "perf", Enabled: true},
	}}
	deps := VROverlayDeps{
		Cfg:   func() config.VROverlayFeature { return cfg },
		World: func() (string, string, bool) { return "wrld_9", "Warehouse", true },
		CamPaths: func() []vroverlay.CamPathItem {
			return []vroverlay.CamPathItem{{Label: "sweep", File: "a.dolly"}}
		},
		CamPathGeom: func(string) vroverlay.CamPathGeom {
			return vroverlay.CamPathGeom{Pts: [][3]float32{{1, 2, 3}}, Spd: []float32{1}, Dur: []float32{2}}
		},
		StatsPerf: func() []perfmon.Sample { return []perfmon.Sample{{CPUPct: 12}} },
	}
	p, sent := newTestVrProxy(t, deps)

	// Mirror some content before the child is up (host not running → nothing forwarded live).
	p.onBusEvent(twitch.TopicChat, eventbus.Event{Topic: twitch.TopicChat, Origin: "n1", Local: true, Data: json.RawMessage(`{"text":"hi"}`)})
	p.onBusEvent(twitch.TopicViewers, eventbus.Event{Topic: twitch.TopicViewers, Origin: "n1", Local: true, Data: json.RawMessage(`{"viewerCount":3}`)})
	if got := sent(); len(got) != 0 {
		t.Fatalf("forwarded while child down: %+v", got)
	}

	p.onReady()
	got := sent()
	byEvent := map[string]int{}
	for _, f := range got {
		byEvent[f.event]++
	}
	if byEvent[vrEvWorld] != 1 || byEvent[vrEvCamPaths] != 1 || byEvent[vrEvStats] != 1 || byEvent[vrEvBus] != 2 {
		t.Fatalf("re-push counts: %v (frames %+v)", byEvent, got)
	}
	// Bus replays keep Origin/Local.
	for _, f := range got {
		if f.event != vrEvBus {
			continue
		}
		var ev vrBusEvent
		if json.Unmarshal(f.data, &ev) != nil || ev.Origin != "n1" || !ev.Local {
			t.Fatalf("replayed bus event lost origin/local: %s", f.data)
		}
	}
	// Campath geometry travels with the list.
	for _, f := range got {
		if f.event == vrEvCamPaths {
			var ev vrCamPathsEvent
			if json.Unmarshal(f.data, &ev) != nil || len(ev.Items) != 1 || len(ev.Items[0].Pts) != 1 {
				t.Fatalf("campath push: %s", f.data)
			}
		}
	}
}

// pushConfig sends only on change, and a child-originated edit (onConfigEdit) is echo-suppressed -
// the child already has that state; persisting must not bounce it straight back.
func TestVRProxyConfigChangeAndEchoSuppression(t *testing.T) {
	cur := config.VROverlayFeature{Enabled: true}
	var saved []config.VROverlayFeature
	deps := VROverlayDeps{
		Cfg:    func() config.VROverlayFeature { return cur },
		Mutate: func(fn func(*config.VROverlayFeature)) { fn(&cur); saved = append(saved, cur) },
	}
	p, sent := newTestVrProxy(t, deps)

	p.onReady() // seeds cfgSig from current cfg (init params already carried it)
	base := len(sent())
	p.pushConfig()
	if len(sent()) != base {
		t.Fatal("unchanged config was pushed")
	}

	cur.StickMoveOnly = true // daemon-side edit → push
	p.pushConfig()
	got := sent()
	if len(got) != base+1 || got[base].event != vrEvConfig {
		t.Fatalf("config change not pushed: %+v", got[base:])
	}

	// Child edit arrives: persist + suppress the echo.
	edit := cur
	edit.WristLarge = true
	raw, _ := json.Marshal(edit)
	p.onConfigEdit(raw)
	if len(saved) != 1 || !saved[0].WristLarge {
		t.Fatalf("edit not persisted: %+v", saved)
	}
	cur = edit
	p.pushConfig()
	if len(sent()) != base+1 {
		t.Fatal("child edit echoed back")
	}
}

// Child state events drive the cached Surface (Available/BindingStatus); OnDown clears them so a
// crashed child never reads as connected.
func TestVRProxyStateMirrorAndDown(t *testing.T) {
	p, _ := newTestVrProxy(t, VROverlayDeps{})
	if p.Available() {
		t.Fatal("available before any state")
	}
	p.onState(json.RawMessage(`{"available":true,"binding":2}`))
	if !p.Available() || p.BindingStatus() != vroverlay.BindingStatus(2) {
		t.Fatal("state event not mirrored")
	}
	p.onDown()
	if p.Available() || p.BindingStatus() != vroverlay.BindingNotReady {
		t.Fatal("down did not clear state")
	}
}

// Surface calls degrade cleanly while the child is down (no panic, explanatory diag).
func TestVRProxySurfaceDownFallbacks(t *testing.T) {
	p, _ := newTestVrProxy(t, VROverlayDeps{})
	if d := p.InputDiag(); !strings.Contains(d, "not running") {
		t.Fatalf("InputDiag while down: %q", d)
	}
	if b := p.ActionBinding("/actions/main/in/summon"); b != "" {
		t.Fatalf("ActionBinding while down: %q", b)
	}
	if err := p.OpenBindingUI(); err == nil {
		t.Fatal("OpenBindingUI while down should error")
	}
	p.ToggleAllOverlays() // fire-and-forget: must not panic
}

// Full restart loop against a REAL child process (re-exec harness): the proxy pushes state, the
// child dies, the host restarts it, and onReady re-pushes - the child's snapshot shows the world
// again without any new daemon-side change.
func TestVRProxyRestartRepushE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e")
	}
	oldBackoff := backoffSchedule
	backoffSchedule = []time.Duration{50 * time.Millisecond}
	defer func() { backoffSchedule = oldBackoff }()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.VROverlayFeature{Enabled: true, Overlays: []config.VROverlay{{ID: "c", Type: "chat", Enabled: true}}}
	deps := VROverlayDeps{
		Cfg:   func() config.VROverlayFeature { return cfg },
		World: func() (string, string, bool) { return "wrld_e2e", "E2E", true },
	}
	p, err := NewVrOverlayProxy(logbus.New(256), deps)
	if err != nil {
		t.Fatal(err)
	}
	p.host.command = func() *exec.Cmd {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "RAVE_MATE_TEST_FEATURE=vr")
		return cmd
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	snapshotWorld := func() (string, error) {
		cctx, ccancel := context.WithTimeout(ctx, 3*time.Second)
		defer ccancel()
		raw, err := p.host.Call(cctx, vrMethSnapshot, nil)
		if err != nil {
			return "", err
		}
		var s vrSnapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s.WorldID, nil
	}
	waitWorld := func(what string) {
		t.Helper()
		waitFor(t, what, 15*time.Second, func() bool {
			if !p.host.Running() {
				return false
			}
			w, err := snapshotWorld()
			return err == nil && w == "wrld_e2e"
		})
	}
	waitWorld("initial world push")

	// Kill the child; the host restarts it and onReady must re-push the world.
	p.host.mu.Lock()
	proc := p.host.cur.cmd.Process
	p.host.mu.Unlock()
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "child down", 10*time.Second, func() bool { return !p.host.Running() })
	waitWorld("world re-push after restart")
	if restarts, _ := p.host.Stats(); restarts < 1 {
		t.Fatal("no restart recorded")
	}
}
