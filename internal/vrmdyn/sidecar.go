package vrmdyn

// Sidecar loader: real physbone parameters from `<avatar>.physbones.json` (format +
// physbone→verlet mapping documented in the package doc). Sidecar chains are
// authoritative - when one loads, the name heuristic is skipped entirely.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rave.page/mate/internal/vrm"
)

// Sidecar mirrors <avatar>.physbones.json (version 1).
type Sidecar struct {
	Version int            `json:"version"`
	Source  string         `json:"source"` // "vrcphysbone" | "dynamicbone"
	Chains  []SidecarChain `json:"chains"`
}

// SidecarChain is one exported physbone/dynamic-bone component.
type SidecarChain struct {
	Root             string     `json:"root"`
	Ignore           []string   `json:"ignore"`
	Pull             float64    `json:"pull"`
	Spring           float64    `json:"spring"`
	Stiffness        float64    `json:"stiffness"`
	Gravity          float64    `json:"gravity"`
	GravityFalloff   float64    `json:"gravityFalloff"`
	Immobile         float64    `json:"immobile"`
	EndpointPosition [3]float64 `json:"endpointPosition"`
	Radius           float64    `json:"radius"`
}

// SidecarPath returns the sidecar path for an avatar file (extension swapped).
func SidecarPath(avatarPath string) string {
	return strings.TrimSuffix(avatarPath, filepath.Ext(avatarPath)) + ".physbones.json"
}

// LoadSidecar reads + validates a physbones sidecar file.
func LoadSidecar(path string) (*Sidecar, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sc Sidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("physbones sidecar: %w", err)
	}
	if sc.Version != 1 {
		return nil, fmt.Errorf("physbones sidecar: unsupported version %d", sc.Version)
	}
	return &sc, nil
}

// NewStateFromFile builds sim state for the avatar at avatarPath: prefers the
// `<avatar>.physbones.json` sidecar, falls back to the name heuristic when the
// sidecar is absent or invalid.
func NewStateFromFile(m *vrm.Model, avatarPath string) *State {
	if sc, err := LoadSidecar(SidecarPath(avatarPath)); err == nil {
		return NewStateWithConfig(m, sc)
	}
	return NewState(m)
}

// params converts physbone semantics to verlet coefficients (see package doc).
func (c *SidecarChain) params() chainParams {
	return chainParams{
		damping:   clamp01(1 - float32(c.Spring)), // Spring = velocity retention
		pull:      clamp01(float32(c.Pull)),
		restStiff: clamp01(float32(c.Stiffness)),
		gravity:   float32(c.Gravity) * 9.81, // physbone 1.0 ≈ earth g, +down
		falloff:   clamp01(float32(c.GravityFalloff)),
		immobile:  clamp01(float32(c.Immobile)),
		endpoint: [3]float32{
			float32(c.EndpointPosition[0]), float32(c.EndpointPosition[1]), float32(c.EndpointPosition[2]),
		},
		radius: float32(c.Radius),
	}
}

// NewStateWithConfig builds sim state from sidecar chains only (no heuristics). Roots
// and ignore entries match node names case-insensitively; a root name matching several
// nodes (mirrored bones sharing a name) yields one chain per node. Chains rooted at a
// humanoid bone are skipped (they would fight the IK), as are humanoid nodes inside a
// chain subtree.
func NewStateWithConfig(m *vrm.Model, sc *Sidecar) *State {
	st := &State{}
	if len(m.Nodes) == 0 || sc == nil {
		return st
	}
	human := humanSet(m)
	restW := m.RestWorld()
	claimed := make([]bool, len(m.Nodes))
	byName := make(map[string][]int, len(m.Nodes))
	for i := range m.Nodes {
		k := lowerASCII(m.Nodes[i].Name)
		byName[k] = append(byName[k], i)
	}
	for i := range sc.Chains {
		c := &sc.Chains[i]
		roots := byName[lowerASCII(c.Root)]
		if len(roots) == 0 {
			continue
		}
		skip := map[int]bool{}
		for _, ig := range c.Ignore {
			for _, n := range byName[lowerASCII(ig)] {
				skip[n] = true
			}
		}
		prm := c.params()
		for _, r := range roots {
			if human[r] || skip[r] {
				continue
			}
			st.addChain(m, restW, r, human, skip, claimed, prm)
		}
	}
	return st
}
