package vrmdyn

// Real-avatar check (skips when LAMB.fbx is absent): the heuristic must find dynamic
// chains (LAMB has hair), a 30-frame Step sequence must keep all posed positions finite,
// and per-Step cost is logged.

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/vrm"
)

func TestRealLAMBChainsAndStability(t *testing.T) {
	path := filepath.Join(os.Getenv("APPDATA"), "rave-mate", "vr_avatars", "LAMB.fbx")
	if _, err := os.Stat(path); err != nil {
		t.Skip("LAMB.fbx not present")
	}
	m, err := vrm.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st := NewState(m)
	infos := st.Chains()
	if len(infos) == 0 {
		t.Fatal("no dynamic chains detected on LAMB.fbx (has hair)")
	}
	joints := 0
	for _, ci := range infos {
		joints += ci.Joints
		t.Logf("chain %-28s joints=%d", ci.Root, ci.Joints)
	}
	t.Logf("chains=%d dynamic joints=%d particles=%d", len(infos), joints, len(st.parts))

	hips := m.HumanoidNode("hips")
	var stepTime time.Duration
	var local []vrm.Mat4
	for i := range 30 {
		local = m.RestLocal()
		if hips >= 0 { // gentle sway drives the sim
			local[hips][12] += 0.1 * float32(math.Sin(float64(i)*0.3))
		}
		start := time.Now()
		st.Step(m, local, 1.0/60)
		stepTime += time.Since(start)
	}
	t.Logf("Step cost: %v avg over 30 frames", stepTime/30)

	world := m.WorldFrom(local)
	skin := m.SkinMatrices(world)
	for mi := range m.Meshes {
		for _, p := range m.PosedPositions(mi, world, skin) {
			for k := range 3 {
				if math.IsNaN(float64(p[k])) || math.IsInf(float64(p[k]), 0) {
					t.Fatalf("mesh %d: non-finite posed position %v", mi, p)
				}
			}
		}
	}
	for pi, p := range st.parts {
		for k := range 3 {
			if math.IsNaN(float64(p.pos[k])) || math.IsInf(float64(p.pos[k]), 0) {
				t.Fatalf("particle %d non-finite: %v", pi, p.pos)
			}
		}
	}
}
