package ui

import (
	"testing"

	"fyne.io/fyne/v2/test"

	"rave.page/mate/internal/automation"
)

// Editing an automation with an existing action chain must not panic. Regression:
// build() looped addRow() (which inserts before the trailing "Add action" button)
// before that button existed → e.box.Objects[:len-1] underflowed on the first row.
func TestActionChainEditorBuildWithInitialActions(t *testing.T) {
	test.NewApp()

	e := &actionChainEditor{u: &UI{}}
	initial := []automation.Action{
		{Type: automation.ActionTranscode},
		{Type: automation.ActionMove, OutputDir: `D:\out`},
	}
	e.build(initial) // panicked pre-fix

	if len(e.rows) != len(initial) {
		t.Fatalf("rows = %d, want %d", len(e.rows), len(initial))
	}
	// box = N rows + trailing "Add action" button, in order.
	if got := len(e.box.Objects); got != len(initial)+1 {
		t.Fatalf("box objects = %d, want %d", got, len(initial)+1)
	}
	if e.box.Objects[len(e.box.Objects)-1] == e.rows[0].root {
		t.Fatal("trailing object should be the Add button, not a row")
	}

	// collect() round-trips the chain.
	out := e.collect()
	if len(out) != len(initial) {
		t.Fatalf("collect len = %d, want %d", len(out), len(initial))
	}
	if out[0].Type != automation.ActionTranscode || out[1].Type != automation.ActionMove {
		t.Fatalf("collect order wrong: %+v", out)
	}
}
