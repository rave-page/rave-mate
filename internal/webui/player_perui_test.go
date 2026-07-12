package webui

import (
	"testing"

	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/ui"
)

// Two UI instances (window + headless remote session) must never share an mpSt for the same
// host string - remote ceEnter/mpEnsureFile would clobber the window's inspector player.
func TestMpInstancesPerUI(t *testing.T) {
	a, b := &UI{}, &UI{}
	t.Cleanup(func() { releaseUIState(a); releaseUIState(b) })
	if a.mp("library") == b.mp("library") {
		t.Fatal("two UIs share one mpSt for host \"library\"")
	}
	if a.mp("library") != a.mp("library") {
		t.Fatal("same UI+host must return a stable instance")
	}
	if a.mp("library") == a.mp("publish") {
		t.Fatal("hosts must stay distinct within one UI")
	}
}

// u.player() must gate the shared audio engine off for headless sessions: remote control
// never plays/stops audio on the host.
func TestHeadlessPlayerGate(t *testing.T) {
	pl := &featurehost.PlayerProxy{}
	svc := ui.Services{Player: pl}
	hu := newHeadlessUI(svc, func(string) {}, func(string) {})
	t.Cleanup(func() { hu.Stop(); releaseUIState(hu) })
	if !hu.virtual() {
		t.Fatal("headless UI must report virtual")
	}
	if hu.player() != nil {
		t.Fatal("headless UI must not expose the audio engine")
	}
	if hu.playerGateKey() != "player.toast.remoteAudioOff" {
		t.Fatal("headless gate toast key wrong")
	}
	w := &UI{svc: svc} // window-style UI (no shell in tests) keeps direct access
	if w.player() != pl {
		t.Fatal("window UI must expose the audio engine")
	}
}

// releaseUIState must drop a retired session's instances (fresh pointer on re-create).
func TestMpInstancesReleased(t *testing.T) {
	u := &UI{}
	prev := u.mp("library")
	prev.name = "bound"
	releaseUIState(u)
	if got := u.mp("library"); got == prev || got.name != "" {
		t.Fatal("releaseUIState did not drop the UI's mp instances")
	}
	releaseUIState(u)
}
