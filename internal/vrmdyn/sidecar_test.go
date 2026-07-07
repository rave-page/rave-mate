package vrmdyn

import (
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/vrm"
)

func writeSidecar(t *testing.T, avatar, body string) {
	t.Helper()
	if err := os.WriteFile(SidecarPath(avatar), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSidecarChainsAuthoritative(t *testing.T) {
	m := testModel(t)
	avatar := filepath.Join(t.TempDir(), "ava.fbx")
	writeSidecar(t, avatar, `{
		"version": 1, "source": "vrcphysbone",
		"chains": [{"root": "hairroot", "ignore": ["Hair2"], "pull": 0.2, "spring": 0.2}]
	}`)
	st := NewStateFromFile(m, avatar)
	infos := st.Chains()
	if len(infos) != 1 || infos[0].Root != "HairRoot" {
		t.Fatalf("chains = %v, want only HairRoot (sidecar authoritative, tail rigid)", infos)
	}
	if infos[0].Joints != 2 { // Hair2 ignored
		t.Errorf("joints = %d, want 2 (ignore respected)", infos[0].Joints)
	}
	for _, p := range st.parts {
		if p.node == nHair2 && !p.tip {
			t.Fatal("ignored Hair2 simulated")
		}
	}
}

func TestSidecarMissingOrInvalidFallsBack(t *testing.T) {
	m := testModel(t)
	// absent sidecar → heuristic (hair + tail)
	if n := len(NewStateFromFile(m, filepath.Join(t.TempDir(), "ava.fbx")).Chains()); n != 2 {
		t.Errorf("missing sidecar: chains = %d, want 2 heuristic", n)
	}
	// bad version → heuristic
	avatar := filepath.Join(t.TempDir(), "ava.vrm")
	writeSidecar(t, avatar, `{"version": 2, "chains": [{"root": "HairRoot"}]}`)
	if n := len(NewStateFromFile(m, avatar).Chains()); n != 2 {
		t.Errorf("bad version: chains = %d, want 2 heuristic", n)
	}
	// malformed JSON → heuristic
	avatar2 := filepath.Join(t.TempDir(), "ava.glb")
	writeSidecar(t, avatar2, `{nope`)
	if n := len(NewStateFromFile(m, avatar2).Chains()); n != 2 {
		t.Errorf("malformed: chains = %d, want 2 heuristic", n)
	}
}

func TestSidecarGravityParamApplied(t *testing.T) {
	m := testModel(t)
	tipY := func(gravity float64) float32 {
		st := NewStateWithConfig(m, &Sidecar{Version: 1, Chains: []SidecarChain{
			{Root: "HairRoot", Pull: 0.05, Spring: 0.5, Gravity: gravity},
		}})
		var local []vrm.Mat4
		for range 120 {
			local = m.RestLocal()
			st.Step(m, local, 1.0/60)
		}
		return worldPos(m, local, nHair2)[1]
	}
	y0, y1 := tipY(0), tipY(1)
	if d := 1.6 - y0; d > 1e-3 || d < -1e-3 {
		t.Errorf("gravity 0: tip y = %v, want no sag from 1.6", y0)
	}
	if y1 > 1.6-0.05 {
		t.Errorf("gravity 1: tip y = %v, want sag below 1.55", y1)
	}
}
