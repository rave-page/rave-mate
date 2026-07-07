package traktorqml

import (
	"strings"
	"testing"
)

// stock mirrors the real Traktor Pro 4 D2.qml shape: an import block, a blank line, then
// `Mapping` on its own line and `{` on the next.
const stockD2 = `
import CSI 1.0
import QtQuick 2.0

import "../../Defines"
import "../Common"
import "../Common/Settings"

Mapping
{
  LoopModePads
  {
    name: "loop_mode_pads"
  }
} //Mapping
`

func TestPatchD2_insertsBothLines(t *testing.T) {
	got, changed, err := patchD2(stockD2)
	if err != nil || !changed {
		t.Fatalf("patchD2: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(got, apiImport) {
		t.Error("missing import line")
	}
	if !strings.Contains(got, apiModule) {
		t.Error("missing ApiModule")
	}
	// import must sit right after the last import, before Mapping.
	impIdx := strings.Index(got, apiImport)
	mapIdx := strings.Index(got, "Mapping")
	modIdx := strings.Index(got, apiModule)
	if !(impIdx < mapIdx && mapIdx < modIdx) {
		t.Errorf("ordering wrong: import=%d mapping=%d module=%d", impIdx, mapIdx, modIdx)
	}
	// ApiModule must be inside the brace (after the `{` that follows Mapping).
	braceIdx := strings.Index(got, "{")
	if modIdx < braceIdx {
		t.Error("ApiModule placed before the Mapping brace")
	}
}

func TestPatchD2_idempotent(t *testing.T) {
	once, _, _ := patchD2(stockD2)
	twice, changed, err := patchD2(once)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second patch should be a no-op")
	}
	if once != twice {
		t.Error("idempotent patch altered content")
	}
	if strings.Count(twice, apiModule) != 1 {
		t.Errorf("ApiModule appears %d times, want 1", strings.Count(twice, apiModule))
	}
}

func TestUnpatchD2_roundTrips(t *testing.T) {
	patched, _, _ := patchD2(stockD2)
	un, changed := unpatchD2(patched)
	if !changed {
		t.Fatal("unpatch reported no change")
	}
	if strings.Contains(un, apiImport) || strings.Contains(un, apiModule) {
		t.Error("unpatch left a marker behind")
	}
	if strings.TrimRight(un, "\n") != strings.TrimRight(stockD2, "\n") {
		t.Errorf("round-trip mismatch:\n--- got ---\n%s\n--- want ---\n%s", un, stockD2)
	}
}

func TestPatchD2_sameLineMappingBrace(t *testing.T) {
	src := "import CSI 1.0\nMapping {\n  Foo {}\n}\n"
	got, changed, err := patchD2(src)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(got, apiModule) || !strings.Contains(got, apiImport) {
		t.Errorf("same-line Mapping not patched: %q", got)
	}
}

func TestPatchD2_noImportFails(t *testing.T) {
	if _, _, err := patchD2("Mapping {\n}\n"); err == nil {
		t.Error("expected error when no import block present")
	}
}
