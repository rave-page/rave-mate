package ui

import (
	"slices"
	"testing"

	"fyne.io/fyne/v2"
)

func testDefs() []dashCardDef {
	b := func(*UI) fyne.CanvasObject { return nil }
	return []dashCardDef{
		{id: "a", title: "A", defaultOn: true, build: b},
		{id: "b", title: "B", defaultOn: false, build: b},
		{id: "c", title: "C", defaultOn: true, build: b},
	}
}

func TestResolveDashCardsDefaults(t *testing.T) {
	got := resolveDashCards(nil, testDefs())
	if !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("defaults=%v want [a c]", got)
	}
}

func TestResolveDashCardsSaved(t *testing.T) {
	// order preserved; unknown + duplicate dropped; omitted id = disabled
	got := resolveDashCards([]string{"c", "nope", "b", "c"}, testDefs())
	if !slices.Equal(got, []string{"c", "b"}) {
		t.Fatalf("saved=%v want [c b]", got)
	}
}

func TestResolveDashCardsAllUnknown(t *testing.T) {
	if got := resolveDashCards([]string{"x", "y"}, testDefs()); len(got) != 0 {
		t.Fatalf("all-unknown=%v want empty", got)
	}
}

// Registry sanity: unique ids, title + help + build present on every module.
func TestDashCardDefsRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range dashCardDefs() {
		if d.id == "" || seen[d.id] {
			t.Errorf("bad/dup id %q", d.id)
		}
		seen[d.id] = true
		if d.title == "" || d.help == "" || d.build == nil {
			t.Errorf("module %q missing title/help/build", d.id)
		}
	}
}
