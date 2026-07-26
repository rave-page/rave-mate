//go:build windows && cgo

package mfenc

import (
	"strings"
	"testing"
)

// TestProbeChildHello: the advertisement gate's child probe - positive against the real
// exe (no HW needed: hello precedes any MF work), negative against a bogus path.
func TestProbeChildHello(t *testing.T) {
	exe, err := encExePath()
	if err != nil {
		t.Skip("rave-mate-enc.exe not built: ", err)
	}
	if err := probeChildHello(exe); err != nil {
		t.Fatalf("hello probe against real child failed: %v", err)
	}
	if err := probeChildHello(`C:\definitely\not\here\rave-mate-enc.exe`); err == nil {
		t.Fatal("hello probe against bogus path must fail")
	}
}

// TestChildAvailCheckMissingExe (field #166): with the child exe unresolvable the gate
// must refuse - h264_mf_native is then never advertised, so a childless install
// negotiates as if the native engine does not exist.
func TestChildAvailCheckMissingExe(t *testing.T) {
	t.Setenv("RAVE_MATE_ENC_EXE", `C:\definitely\not\here\rave-mate-enc.exe`)
	if childAvailCheck() {
		t.Fatal("gate passed with an unspawnable child exe")
	}
}

// TestStagedChildExe verifies embed extraction: version-stamped filename, atomic stage,
// idempotent re-use, and that the staged file answers hello. Skips in builds without the
// encembed tag (run the mfenc suite with -tags encembed to cover it).
func TestStagedChildExe(t *testing.T) {
	if len(embeddedEnc) == 0 {
		t.Skip("no embedded child in this build (encembed tag off)")
	}
	p1, err := stagedChildExe()
	if err != nil || p1 == "" {
		t.Fatalf("stagedChildExe: %q %v", p1, err)
	}
	if !strings.Contains(p1, "rave-mate-enc-") || !strings.HasSuffix(p1, ".exe") {
		t.Fatalf("staged path not version-stamped: %q", p1)
	}
	p2, err := stagedChildExe()
	if err != nil || p2 != p1 {
		t.Fatalf("re-stage not idempotent: %q vs %q (%v)", p2, p1, err)
	}
	if err := probeChildHello(p1); err != nil {
		t.Fatalf("staged child does not answer hello: %v", err)
	}
}
