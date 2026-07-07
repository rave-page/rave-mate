package giokit

import (
	"image"
	"strings"
	"testing"
)

// snapshotFrame publishes one frame with the given labeled buttons.
func snapshotFrame(r *Registry, labels ...string) {
	r.BeginFrame()
	for i, l := range labels {
		r.PushOffset(image.Pt(10*i, 5))
		r.Add("button", l, image.Pt(30, 24), func() {})
		r.PopOffset()
	}
	r.EndFrame()
}

func TestWindowRegistryLifecycle(t *testing.T) {
	r1, r2 := NewRegistry(), NewRegistry()
	id1 := RegisterWindow("Player", "Track - rave-mate (Gio)", r1)
	id2 := RegisterWindow("player", "Other - rave-mate (Gio)", r2)
	defer UnregisterWindow(id1)
	defer UnregisterWindow(id2)

	if id1 != "player" {
		t.Errorf("id1 = %q, want player", id1)
	}
	if id2 != "player#2" {
		t.Errorf("id2 = %q, want player#2 (collision suffix)", id2)
	}
	ids := WindowIDs()
	if len(ids) != 2 || !strings.HasPrefix(ids[0], "player\t") || !strings.HasPrefix(ids[1], "player#2\t") {
		t.Errorf("WindowIDs = %q", ids)
	}

	UnregisterWindow(id1)
	if got := WindowIDs(); len(got) != 1 || !strings.HasPrefix(got[0], "player#2\t") {
		t.Errorf("after unregister: %q", got)
	}
	UnregisterWindow(id1) // idempotent
	UnregisterWindow(id2)
	if len(WindowIDs()) != 0 {
		t.Error("registry not empty after unregisters")
	}
}

func TestSnapshotText(t *testing.T) {
	r := NewRegistry()
	id := RegisterWindow("player", "T", r)
	defer UnregisterWindow(id)

	s, ok := SnapshotText(id)
	if !ok || !strings.Contains(s, "no labeled controls") {
		t.Errorf("empty-frame snapshot = %q ok=%v", s, ok)
	}

	snapshotFrame(r, "play", "trim.in")
	s, ok = SnapshotText(id)
	if !ok {
		t.Fatal("known window must snapshot")
	}
	if !strings.Contains(s, `button "play" (0,5)-(30,29)`) || !strings.Contains(s, `button "trim.in" (10,5)-(40,29)`) {
		t.Errorf("snapshot missing nodes:\n%s", s)
	}
	if _, ok := SnapshotText("nope"); ok {
		t.Error("unknown window must not snapshot")
	}
}

func TestTapWindow(t *testing.T) {
	r := NewRegistry()
	id := RegisterWindow("player", "T", r)
	defer UnregisterWindow(id)

	fired := 0
	r.BeginFrame()
	r.Add("button", "play", image.Pt(10, 10), func() { fired++ })
	r.EndFrame()

	if err := TapWindow("nope", "play"); err == nil {
		t.Error("unknown window must error")
	}
	if err := TapWindow(id, "nope"); err == nil {
		t.Error("unknown control must error")
	}
	if err := TapWindow(id, "play"); err != nil {
		t.Fatalf("tap: %v", err)
	}
	r.BeginFrame() // queued activation runs here
	if fired != 1 {
		t.Errorf("fired = %d, want 1", fired)
	}
	r.EndFrame()
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Player":                    "player",
		"Track A - rave-mate (Gio)": "track-a-rave-mate-gio",
		"trim.in window":            "trim.in-window",
		"  ":                        "",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
