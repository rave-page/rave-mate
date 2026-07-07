package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/ui"
)

func newSettingsTestUI() *UI {
	return &UI{svc: ui.Services{Cfg: &config.Config{}}, active: "settings"}
}

// TestSettingsRegistryComplete: every sub-tab card renders real content, appears in exactly one
// sub-tab, and every feature toggle is reachable through some sub-tab (search + tabs both drive
// off settingsSections, so a missing card would be silently unreachable).
func TestSettingsRegistryComplete(t *testing.T) {
	u := newSettingsTestUI()
	seen := map[string]int{}
	for _, s := range settingsSections() {
		for _, id := range s.cards {
			seen[id]++
			if title, _, _ := u.cardContent(id); title == "?" || title == "" {
				t.Errorf("card %q (section %q) has no content", id, s.id)
			}
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("card %q appears in %d sections", id, n)
		}
	}
	for _, tg := range u.toggleRegistry() {
		if seen[tg.id] == 0 {
			t.Errorf("toggle %q not reachable via any settings sub-tab", tg.id)
		}
	}
}

// TestSettingsSubTabRendersActiveOnly: the content pane holds only the active section's cards.
func TestSettingsSubTabRendersActiveOnly(t *testing.T) {
	u := newSettingsTestUI()
	u.setSec = "djsources"
	out := u.renderSettingsContent()
	if !strings.Contains(out, "stset-midi") {
		t.Fatal("active sub-tab card missing")
	}
	if strings.Contains(out, "stset-updates") {
		t.Fatal("inactive sub-tab card rendered")
	}
	vis, searching := u.settingsVisible()
	if searching || !vis["midi"] || vis["updates"] {
		t.Fatalf("view state wrong: searching=%v vis=%v", searching, vis)
	}
}

// TestSettingsSearch: a query matches across ALL sections; no match → empty state.
func TestSettingsSearch(t *testing.T) {
	u := newSettingsTestUI()
	u.setSec = "account" // search must surface cards from other sections regardless
	u.setQuery = "midi"
	out := u.renderSettingsContent()
	if !strings.Contains(out, "stset-midi") {
		t.Fatal("search did not surface the MIDI card")
	}
	if vis, searching := u.settingsVisible(); !searching || !vis["midi"] {
		t.Fatal("view state not in search mode with midi visible")
	}
	u.setQuery = "zzz-no-such-setting"
	if out := u.renderSettingsContent(); !strings.Contains(out, "rp-empty") {
		t.Fatal("no-results empty state missing")
	}
}

func TestFoldSearch(t *testing.T) {
	for in, want := range map[string]string{"Résolume": "resolume", "MIDI": "midi", "übersicht": "ubersicht"} {
		if got := foldSearch(in); got != want {
			t.Errorf("foldSearch(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTickPatchDedup: identical fragment pushes are skipped; changed ones re-emit.
func TestTickPatchDedup(t *testing.T) {
	u := newSettingsTestUI()
	var js strings.Builder
	u.tickPatch(&js, "x", "<b>1</b>")
	u.tickPatch(&js, "x", "<b>1</b>")
	if n := strings.Count(js.String(), "__patch"); n != 1 {
		t.Fatalf("dedup failed: %d patches", n)
	}
	u.tickPatch(&js, "x", "<b>2</b>")
	if n := strings.Count(js.String(), "__patch"); n != 2 {
		t.Fatalf("change not re-emitted: %d patches", n)
	}
}
