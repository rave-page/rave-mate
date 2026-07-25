package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/ui"
)

// The driver-managed block clones the controller's device name to the DJ fan-out by default
// (Serato matches by name), exposes the clone toggle (checked), and switches to "<Name> THRU"
// when the toggle is off. Pure render - no driver sync, no live instance.
func TestMidiDriverThruCloneToggle(t *testing.T) {
	u := &UI{svc: ui.Services{Cfg: &config.Config{}}}
	ctx := midiCtlRenderCtx{}
	c := config.MIDIControllerMap{
		Name: "Controller 2", Port: "DJ2GO2 Touch MIDI",
		ThruPort: midi.DriverSentinel, Enabled: true,
	}

	// clone (default): fan-out shows the real device name, toggle checked.
	clone := midiDrvThruHTML(u.midiDrvThruState(0, c, ctx))
	if !strings.Contains(clone, "<code>DJ2GO2 Touch MIDI</code>") {
		t.Errorf("clone mode should show the device name; got:\n%s", clone)
	}
	if strings.Contains(clone, "Controller 2 THRU") {
		t.Errorf("clone mode must not append THRU")
	}
	if !strings.Contains(clone, "midi-ctl-clone:0") {
		t.Errorf("clone toggle missing")
	}
	// only the clone toggle emits a checkbox in this block (fchip uses class "active").
	if !strings.Contains(clone, " checked") {
		t.Errorf("clone toggle should be ON by default")
	}

	// distinct opt-in: fan-out is "<Name> THRU", toggle off.
	c.ThruDistinctName = true
	dist := midiDrvThruHTML(u.midiDrvThruState(0, c, ctx))
	if !strings.Contains(dist, "<code>Controller 2 THRU</code>") {
		t.Errorf("distinct mode should show <Name> THRU; got:\n%s", dist)
	}
	if strings.Contains(dist, " checked") {
		t.Errorf("distinct mode clone toggle should be off (no checkbox checked)")
	}
}
