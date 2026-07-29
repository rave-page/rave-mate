package mediaroute

import (
	"fmt"

	"rave.page/mate/internal/testcard"
)

// Testcard drives the deterministic diagnostic source (internal/testcard) inside the media child,
// where the Spout cgo lives. op: "start" (w/h/fps, 0 = defaults; restarts if running), "stop",
// "stats" (gen + every verifier stage), "reset" (clear verifier tallies for a fresh experiment).
//
// The card is a normal Spout sender, so it is advertised as a medialink source like any other:
// routing it DIRECTLY to a peer exercises capture→encode→wire→decode→republish with zero third
// parties, and adding OBS in the middle isolates the OBS leg. That differential is the experiment
// this harness exists for.
func (m *Manager) Testcard(op string, w, h, fps int) (testcard.Report, error) {
	switch op {
	case "start":
		if w <= 0 {
			w, h = testcard.DefaultW, testcard.DefaultH
		}
		if fps <= 0 {
			fps = testcard.DefaultFPS
		}
		m.mu.Lock()
		old := m.tc
		m.tc = nil
		m.mu.Unlock()
		if old != nil {
			old.Stop() // outside m.mu: Stop joins the render loop
		}
		fs, err := m.newFrameSnd(testcard.SenderName)
		if err != nil {
			return testcard.Report{}, fmt.Errorf("testcard: video share unavailable: %w", err)
		}
		g, err := testcard.NewGen(fs, w, h, fps)
		if err != nil {
			return testcard.Report{}, err
		}
		m.mu.Lock()
		m.tc = g
		m.mu.Unlock()
		st := g.Stats()
		m.log.Info(source, "testcard started", map[string]any{
			"sender": st.Name, "w": st.W, "h": st.H, "fps": st.FPS, "session": st.Session})
		return m.testcardReport(), nil
	case "stop":
		m.mu.Lock()
		old := m.tc
		m.tc = nil
		m.mu.Unlock()
		if old != nil {
			st := old.Stats()
			old.Stop()
			m.log.Info(source, "testcard stopped", map[string]any{
				"frames": st.Frames, "skips": st.Skips})
		}
		return m.testcardReport(), nil
	case "reset":
		testcard.VerifyReset()
		return m.testcardReport(), nil
	case "", "stats":
		return m.testcardReport(), nil
	}
	return testcard.Report{}, fmt.Errorf("testcard: unknown op %q (start|stop|stats|reset)", op)
}

func (m *Manager) testcardReport() testcard.Report {
	r := testcard.Report{Stages: testcard.VerifySnapshot()}
	m.mu.Lock()
	g := m.tc
	m.mu.Unlock()
	if g != nil {
		st := g.Stats()
		r.Gen = &st
	}
	return r
}
