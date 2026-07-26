package vroverlay

import (
	"errors"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

func newTickManager(t *testing.T, rt Runtime) *Manager {
	t.Helper()
	cfg := func() config.VROverlayFeature {
		return config.VROverlayFeature{Overlays: []config.VROverlay{{ID: "c1", Type: "chat", Enabled: true}}}
	}
	m := New(logbus.New(16), nil, rt, cfg, nil)
	rend, err := NewRenderer(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rend.Close)
	m.rend = rend
	return m
}

// A persistent SetTexture (SetOverlayRaw) failure streak must trip texDead through the tick path -
// post-TDR the compositor rejects uploads while Show/SetTransform still "succeed", so the all-fail
// detector never fires; this is the in-place-reinit trigger (GPU_RESILIENCE_PLAN P0).
func TestTickTexFailureStreakTripsTexDead(t *testing.T) {
	rt := &fakeRT{texErr: errors.New("SetOverlayRaw err 23")}
	m := newTickManager(t, rt)
	for i := 0; i < vrTexFailBudget; i++ {
		if m.health.texDead() {
			t.Fatalf("tripped early at tick %d (budget %d)", i, vrTexFailBudget)
		}
		m.tick()
	}
	if !m.health.texDead() {
		t.Fatalf("texDead not tripped after %d failing ticks (texFails=%d)", vrTexFailBudget, m.health.texFails)
	}
	rt.texErr = nil // compositor recovered - one good upload clears the streak
	m.tick()
	if m.health.texDead() {
		t.Fatalf("success did not reset the streak (texFails=%d)", m.health.texFails)
	}
}

// RequestReinit (TDR fan-out) is consumed exactly once, from any goroutine; a fresh session
// discards a request that predates it (runConnected calls takeReinit on entry).
func TestRequestReinitConsumeOnce(t *testing.T) {
	m := newTickManager(t, &fakeRT{})
	if _, ok := m.takeReinit(); ok {
		t.Fatal("pending reinit without a request")
	}
	m.RequestReinit("Display (event 4101)")
	d, ok := m.takeReinit()
	if !ok || d != "Display (event 4101)" {
		t.Fatalf("takeReinit = %q,%v", d, ok)
	}
	if _, ok := m.takeReinit(); ok {
		t.Fatal("reinit not cleared after take")
	}
}
