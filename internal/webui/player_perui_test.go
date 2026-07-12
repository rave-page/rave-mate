package webui

import "testing"

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
