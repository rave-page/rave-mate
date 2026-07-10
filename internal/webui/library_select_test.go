package webui

import "testing"

func TestLibRangeApply(t *testing.T) {
	order := []string{"a", "b", "c", "d", "e"}
	sel := map[string]bool{}

	libRangeApply(order, sel, "b", "d", true)
	for _, p := range []string{"b", "c", "d"} {
		if !sel[p] {
			t.Fatalf("range select missing %q", p)
		}
	}
	if sel["a"] || sel["e"] {
		t.Fatal("range select leaked outside anchor..path")
	}

	// reversed direction deselects the same run
	libRangeApply(order, sel, "d", "b", false)
	if len(sel) != 0 {
		t.Fatalf("range deselect left %v", sel)
	}

	// no anchor degrades to the single clicked row
	libRangeApply(order, sel, "", "c", true)
	if !sel["c"] || len(sel) != 1 {
		t.Fatalf("anchorless apply = %v, want just c", sel)
	}

	// anchor filtered away (not in order) also degrades to single row
	libRangeApply(order, sel, "zz", "e", true)
	if !sel["e"] || len(sel) != 2 {
		t.Fatalf("missing-anchor apply = %v, want c+e", sel)
	}

	// clicked row not visible: no-op
	libRangeApply(order, sel, "a", "zz", true)
	if len(sel) != 2 {
		t.Fatalf("invisible target mutated selection: %v", sel)
	}
}
