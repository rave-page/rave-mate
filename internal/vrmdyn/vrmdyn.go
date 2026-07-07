// Package vrmdyn simulates secondary motion for avatar rendering - the CPU equivalent of
// Unity Dynamic Bones / VRChat PhysBones. Non-humanoid bone chains (hair, tails, ears,
// skirts, accessories) hang off the vrmik-posed humanoid skeleton via verlet particles:
// inertia + gravity + stiffness pull toward the animated pose + rest-length constraint,
// then joint LOCAL rotations are rewritten so the skinned mesh follows. Pure stdlib.
//
// Chain sources, in priority order:
//
//  1. Sidecar file `<avatar-path-without-ext>.physbones.json` (NewStateFromFile) - REAL
//     physbone parameters exported by a future Unity editor tool walking VRCPhysBone /
//     DynamicBone components. When a sidecar loads, it is authoritative: heuristics are
//     skipped entirely. Format (version 1):
//
//     {
//     "version": 1,
//     "source": "vrcphysbone" | "dynamicbone",
//     "chains": [{
//     "root": "<bone name>",           // case-insensitive vs skeleton joint names
//     "ignore": ["<bone name>", ...],  // subtrees under root kept rigid
//     "pull": 0.2,                     // physbone Pull
//     "spring": 0.2,                   // physbone Spring/Momentum
//     "stiffness": 0.0,                // physbone Stiffness
//     "gravity": 0.0,                  // −1..1, y-down positive
//     "gravityFalloff": 0.0,
//     "immobile": 0.0,                 // 0..1 root-motion transfer
//     "endpointPosition": [0,0,0],     // extra virtual tip bone (leaf-local, optional)
//     "radius": 0.0                    // collider radius (stored; collision TODO)
//     }]
//     }
//
//     Physbone → verlet mapping (documented here, applied in sidecar.go):
//     pull → per-frame pull factor toward the animated target; spring → velocity
//     retention (damping = 1−spring); stiffness → additional rest-pose pull toward the
//     animated target; gravity → per-chain acceleration gravity×9.81 m/s² (falloff
//     scales it down as the bone aligns with its target); immobile → fraction of anchor
//     world motion transferred rigidly to the particles; endpointPosition → virtual tip
//     offset appended to every leaf; radius → stored only (no colliders yet).
//
//  2. Name heuristic (NewState) - FBX/VRM files do NOT embed physbone components, so
//     chains are detected once from the skeleton: joints NOT in Model.Humanoid whose
//     subtree contains no humanoid bone, gated by dynamic keywords (hair/tail/ear/…).
//     Fingers, toes, twist bones, props etc. never match and stay rigid.
//
// A State is per-avatar-instance and NOT safe for concurrent use - one per render
// consumer. Bounded by construction: state size = #dynamic joints (≤ maxParticles).
package vrmdyn

import (
	"math"
	"strings"

	"rave.page/mate/internal/vrm"
)

// Simulation constants (heuristic defaults; sidecar chains carry their own params).
const (
	defDamping   = 0.15 // velocity loss per 60fps frame
	defPull      = 0.08 // per-60fps-frame pull toward the animated pose
	defGravity   = 2.0  // m/s², subtle idle sway (not full 9.81 - targets follow the body)
	maxSubstep   = 0.05 // s; larger dt splits into substeps
	maxSubsteps  = 4    // dt beyond maxSubstep*maxSubsteps is clamped
	teleportDist = 0.5  // m; chain-anchor jump beyond this reseeds the chain
	maxParticles = 4096 // hard cap: pathological skeletons can't grow state unbounded
)

// chainParams are the per-chain sim coefficients (verlet-space, see package doc for
// the physbone mapping).
type chainParams struct {
	damping   float32    // 0..1 velocity loss per 60fps frame
	pull      float32    // 0..1 per-frame pull toward animated target
	restStiff float32    // 0..1 extra pull (physbone Stiffness)
	gravity   float32    // m/s², +down (may be negative)
	falloff   float32    // 0..1 gravity reduction when aligned with target
	immobile  float32    // 0..1 anchor-motion transfer
	endpoint  [3]float32 // leaf virtual-tip local offset; zero = auto-extend
	radius    float32    // collider radius (stored for future collision; unused)
}

var defaultParams = chainParams{damping: defDamping, pull: defPull, gravity: defGravity}

// fleshParams drive body-flesh helper bones (thigh/butt/belly/breast jiggle): stiff, strongly
// target-pulled, gravity-free - subtle wobble. Accessory defaults on these rotate skinned
// flesh regions in big swings that read as broken weights.
var fleshParams = chainParams{damping: 0.3, pull: 0.5, restStiff: 0.3}

// fleshKeywords classify an already-detected chain root as body flesh (plain substring -
// these run only on names that already matched the dynamic heuristic).
var fleshKeywords = []string{"jiggle", "breast", "bust", "boob", "belly", "butt", "ass"}

// isFleshName reports whether a chain-root name denotes a flesh-jiggle helper bone.
func isFleshName(name string) bool {
	ln := lowerASCII(name)
	for _, kw := range fleshKeywords {
		if strings.Contains(ln, kw) {
			return true
		}
	}
	return false
}

// particle is one verlet point: a dynamic joint's world position, or a virtual tip
// extending a leaf joint (tip=true, node = the leaf).
type particle struct {
	node      int        // node index (for tips: the leaf joint it extends)
	parent    int        // particle index of parent; -1 = chain root
	aimChild  int        // particle this joint's rotation aims at (-1 for tips)
	restLen   float32    // rest bone length to parent particle (world)
	tip       bool       // virtual tip (no joint written)
	tipLocal  [3]float32 // tip offset in the leaf joint's local space
	kinematic bool       // chain root: follows animation exactly
	pos, prev [3]float32
}

// chain is one dynamic subtree: particles [start,end), seated on anchor's motion.
type chain struct {
	name       string // root joint name (diagnostics)
	anchor     int    // node whose world motion seats the chain (chain-root's parent)
	start, end int
	prm        chainParams
	seeded     bool
	lastAnchor [3]float32
}

// State is per-avatar-instance sim state. NOT safe for concurrent use; keyed to the
// Model it was built from (node indices).
type State struct {
	parts  []particle
	chains []chain
	simW   []vrm.Mat4 // write-back scratch, len == len(parts)
}

// ChainInfo describes one detected chain (Joints excludes virtual tips).
type ChainInfo struct {
	Root   string
	Joints int
}

// Chains reports the detected dynamic chains.
func (st *State) Chains() []ChainInfo {
	out := make([]ChainInfo, len(st.chains))
	for i, c := range st.chains {
		n := 0
		for pi := c.start; pi < c.end; pi++ {
			if !st.parts[pi].tip {
				n++
			}
		}
		out[i] = ChainInfo{Root: c.name, Joints: n}
	}
	return out
}

// ── chain detection ──────────────────────────────────────────────────────────

// dynKeywords gate the name heuristic (case-insensitive substring with a word/camelCase
// boundary before the match, so "forearm" never matches "ear").
var dynKeywords = []string{
	"hair", "tail", "ear", "skirt", "breast", "bust", "boob", "cloth", "ribbon",
	"wing", "scarf", "cape", "string", "rope", "chain", "phys", "dynamic", "jiggle",
	"acc", "feather", "fluff", "bell", "hood",
}

// weakKeywords are too generic to justify a single-bone chain with no children.
var weakKeywords = map[string]bool{
	"cloth": true, "string": true, "rope": true, "chain": true, "dynamic": true, "acc": true,
}

// matchDynamic reports the first dynamic keyword found in name at a word/camelCase boundary.
func matchDynamic(name string) (string, bool) {
	ln := lowerASCII(name)
	for _, kw := range dynKeywords {
		for idx := 0; ; {
			j := strings.Index(ln[idx:], kw)
			if j < 0 {
				break
			}
			j += idx
			if j == 0 || !isAlpha(ln[j-1]) || (name[j] >= 'A' && name[j] <= 'Z') {
				return kw, true
			}
			idx = j + 1
		}
	}
	return "", false
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// lowerASCII lowercases ASCII letters, preserving byte length.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// NewState detects dynamic chains by the name heuristic (see package doc) and returns
// fresh sim state (lazily seeded on the first Step).
func NewState(m *vrm.Model) *State {
	st := &State{}
	if len(m.Nodes) == 0 {
		return st
	}
	human := humanSet(m)
	hasHum := humanDescent(m, human)
	restW := m.RestWorld()
	claimed := make([]bool, len(m.Nodes))
	meshOwner := make(map[int]bool, len(m.Meshes)) // mesh containers aren't bones
	for i := range m.Meshes {
		meshOwner[m.Meshes[i].NodeIdx] = true
	}
	var scan func(i int)
	scan = func(i int) {
		if i < 0 || i >= len(m.Nodes) || claimed[i] {
			return
		}
		// A dynamic root must hang off something (parentless = scene/mesh container) and
		// must be a bone, not a mesh-owner node - keyword-named mesh objects ("Hoodie",
		// "Hair", …) otherwise become self-anchored no-op chains.
		if !human[i] && !hasHum[i] && m.Nodes[i].Parent >= 0 && !meshOwner[i] {
			if kw, ok := matchDynamic(m.Nodes[i].Name); ok {
				// single bone, no children: only strong keywords (hair strands etc.)
				if len(m.Nodes[i].Children) > 0 || !weakKeywords[kw] {
					prm := defaultParams
					if isFleshName(m.Nodes[i].Name) {
						prm = fleshParams
					}
					st.addChain(m, restW, i, human, nil, claimed, prm)
					return
				}
			}
		}
		for _, c := range m.Nodes[i].Children {
			scan(c)
		}
	}
	for _, r := range m.Roots {
		scan(r)
	}
	return st
}

func humanSet(m *vrm.Model) map[int]bool {
	h := make(map[int]bool, len(m.Humanoid))
	for _, n := range m.Humanoid {
		if n >= 0 && n < len(m.Nodes) {
			h[n] = true
		}
	}
	return h
}

// humanDescent marks nodes whose subtree (excluding self) contains a humanoid bone.
func humanDescent(m *vrm.Model, human map[int]bool) []bool {
	out := make([]bool, len(m.Nodes))
	var visit func(i int) bool
	visit = func(i int) bool {
		below := false
		for _, c := range m.Nodes[i].Children {
			if c < 0 || c >= len(m.Nodes) {
				continue
			}
			if visit(c) || human[c] {
				below = true
			}
		}
		out[i] = below
		return below
	}
	for _, r := range m.Roots {
		visit(r)
	}
	return out
}

// addChain builds particles for the dynamic subtree rooted at root (pre-order, so a
// parent always precedes its children in st.parts). skip/human subtrees stay rigid.
func (st *State) addChain(m *vrm.Model, restW []vrm.Mat4, root int, human map[int]bool, skip map[int]bool, claimed []bool, prm chainParams) {
	if len(st.parts) >= maxParticles || claimed[root] {
		return
	}
	start := len(st.parts)
	var walk func(node, parentPart int)
	walk = func(node, parentPart int) {
		if len(st.parts) >= maxParticles {
			return
		}
		pi := len(st.parts)
		claimed[node] = true
		pt := particle{node: node, parent: parentPart, aimChild: -1, kinematic: parentPart < 0}
		if parentPart >= 0 {
			pt.restLen = dist(mpos(restW[node]), mpos(restW[st.parts[parentPart].node]))
		}
		st.parts = append(st.parts, pt)
		if parentPart >= 0 && st.parts[parentPart].aimChild < 0 {
			st.parts[parentPart].aimChild = pi
		}
		for _, c := range m.Nodes[node].Children {
			if c < 0 || c >= len(m.Nodes) || claimed[c] || human[c] || skip[c] {
				continue
			}
			walk(c, pi)
		}
		if st.parts[pi].aimChild < 0 && len(st.parts) < maxParticles { // leaf → virtual tip
			tip := particle{node: node, parent: pi, aimChild: -1, tip: true}
			tip.tipLocal, tip.restLen = tipOffset(m, restW, node, prm.endpoint)
			st.parts[pi].aimChild = len(st.parts)
			st.parts = append(st.parts, tip)
		}
	}
	walk(root, -1)
	if len(st.parts) == start {
		return
	}
	anchor := m.Nodes[root].Parent
	if anchor < 0 {
		anchor = root
	}
	st.chains = append(st.chains, chain{name: m.Nodes[root].Name, anchor: anchor, start: start, end: len(st.parts), prm: prm})
}

// tipOffset returns a leaf's virtual-tip offset in the leaf's local space + its rest
// length: the given endpoint if non-zero, else the parent→leaf rest direction extended
// by the leaf's own rest bone length (0.07m down for zero-length bones).
func tipOffset(m *vrm.Model, restW []vrm.Mat4, node int, endpoint [3]float32) ([3]float32, float32) {
	lp := mpos(restW[node])
	if endpoint != ([3]float32{}) {
		return endpoint, dist(restW[node].TransformPoint(endpoint), lp)
	}
	var dir [3]float32
	var l float32
	if par := m.Nodes[node].Parent; par >= 0 {
		dir = sub(lp, mpos(restW[par]))
		l = length(dir)
	}
	if l < eps {
		dir, l = [3]float32{0, -1, 0}, 0.07
	} else {
		dir = scale(dir, 1/l)
	}
	// world offset → leaf local (conjugate rotation, per-axis scale; shear-free)
	lo := qRotate(qConj(rotQuat(restW[node])), scale(dir, l))
	_, s := matTS(restW[node])
	for k := range 3 {
		if s[k] > eps {
			lo[k] /= s[k]
		}
	}
	return lo, l
}

// ── simulation ───────────────────────────────────────────────────────────────

// Reset clears sim state; the next Step re-seats every chain at the animated pose.
func (st *State) Reset() {
	for i := range st.chains {
		st.chains[i].seeded = false
	}
}

// Step advances the simulation by dt seconds and rewrites the LOCAL matrices of dynamic
// joints in place. dt==0 applies the current sim state without integrating (paused
// re-render). dt is clamped to maxSubstep per substep (max maxSubsteps). A chain whose
// anchor jumps >teleportDist in one Step reseeds (scrub-jump safety).
func (st *State) Step(m *vrm.Model, local []vrm.Mat4, dt float64) {
	if len(st.parts) == 0 || len(local) != len(m.Nodes) {
		return
	}
	worldAnim := m.WorldFrom(local)
	for ci := range st.chains {
		ch := &st.chains[ci]
		ap := mpos(worldAnim[ch.anchor])
		if !ch.seeded || dist(ap, ch.lastAnchor) > teleportDist {
			st.seedChain(ci, worldAnim)
			ch.seeded = true
		} else if ch.prm.immobile > 0 { // physbone Immobile: move rigidly with the anchor
			d := scale(sub(ap, ch.lastAnchor), ch.prm.immobile)
			for pi := ch.start; pi < ch.end; pi++ {
				if p := &st.parts[pi]; !p.kinematic {
					p.pos, p.prev = add(p.pos, d), add(p.prev, d)
				}
			}
		}
		ch.lastAnchor = ap
	}
	if dt > 0 {
		if dt > maxSubstep*maxSubsteps {
			dt = maxSubstep * maxSubsteps
		}
		n := int(math.Ceil(dt / maxSubstep))
		if n < 1 {
			n = 1
		}
		h := float32(dt / float64(n))
		for range n {
			st.integrate(worldAnim, h)
		}
	}
	st.writeBack(m, local, worldAnim)
}

// targetPos is a particle's fully-animated (no-sim) world position.
func (st *State) targetPos(pi int, worldAnim []vrm.Mat4) [3]float32 {
	p := &st.parts[pi]
	if p.tip {
		return worldAnim[p.node].TransformPoint(p.tipLocal)
	}
	return mpos(worldAnim[p.node])
}

func (st *State) seedChain(ci int, worldAnim []vrm.Mat4) {
	ch := &st.chains[ci]
	for pi := ch.start; pi < ch.end; pi++ {
		t := st.targetPos(pi, worldAnim)
		st.parts[pi].pos, st.parts[pi].prev = t, t
	}
}

// integrate runs one verlet substep of h seconds: inertia (damped), gravity, pull toward
// the animated target, then rest-length constraint to the parent particle. Coefficients
// are 60fps-normalized: keep = (1−damping)^(60h), k = 1−(1−pull)^(60h).
func (st *State) integrate(worldAnim []vrm.Mat4, h float32) {
	h60 := h * 60
	for ci := range st.chains {
		ch := &st.chains[ci]
		keep := powf(1-clamp01(ch.prm.damping), h60)
		kPull := 1 - powf(1-clamp01(ch.prm.pull), h60)
		kStiff := 1 - powf(1-clamp01(ch.prm.restStiff), h60)
		grav := ch.prm.gravity * h * h
		for pi := ch.start; pi < ch.end; pi++ {
			p := &st.parts[pi]
			target := st.targetPos(pi, worldAnim)
			if p.kinematic {
				p.prev, p.pos = p.pos, target
				continue
			}
			vel := sub(p.pos, p.prev)
			p.prev = p.pos
			p.pos = add(p.pos, scale(vel, keep))
			pp := st.parts[p.parent].pos
			if grav != 0 {
				g := grav
				if ch.prm.falloff > 0 { // gravity fades as the bone aligns with its target
					a := dot(normalize(sub(p.pos, pp)), normalize(sub(target, pp)))
					if a > 0 {
						g *= 1 - ch.prm.falloff*a
					}
				}
				p.pos[1] -= g
			}
			if kPull > 0 {
				p.pos = add(p.pos, scale(sub(target, p.pos), kPull))
			}
			if kStiff > 0 {
				p.pos = add(p.pos, scale(sub(target, p.pos), kStiff))
			}
			if p.restLen > eps { // no stretch: pin to rest bone length
				d := sub(p.pos, pp)
				l := length(d)
				if l < eps {
					d, l = sub(target, pp), length(sub(target, pp))
				}
				if l > eps {
					p.pos = add(pp, scale(d, p.restLen/l))
				}
			}
		}
	}
}

// writeBack rewrites dynamic joints' LOCAL rotations root→leaf so each bone aims at its
// (first) child particle: dR = qBetween(animated dir, sim dir) composed onto the joint's
// current world rotation under the already-simulated parent, then converted to local.
func (st *State) writeBack(m *vrm.Model, local, worldAnim []vrm.Mat4) {
	if cap(st.simW) < len(st.parts) {
		st.simW = make([]vrm.Mat4, len(st.parts))
	}
	simW := st.simW[:len(st.parts)]
	for ci := range st.chains {
		ch := &st.chains[ci]
		for pi := ch.start; pi < ch.end; pi++ {
			p := &st.parts[pi]
			if p.tip {
				continue
			}
			var pw vrm.Mat4
			if p.parent < 0 {
				if a := m.Nodes[p.node].Parent; a >= 0 {
					pw = worldAnim[a]
				} else {
					pw = vrm.Identity()
				}
			} else {
				pw = simW[p.parent]
			}
			cur := pw.Mul(local[p.node])
			jp := mpos(cur)
			c := &st.parts[p.aimChild]
			var childCur [3]float32
			if c.tip {
				childCur = cur.TransformPoint(c.tipLocal)
			} else {
				childCur = cur.TransformPoint(mpos(local[c.node]))
			}
			curDir, simDir := sub(childCur, jp), sub(c.pos, jp)
			if length(curDir) > eps && length(simDir) > eps {
				dR := qBetween(normalize(curDir), normalize(simDir))
				qw := qMul(dR, rotQuat(cur))
				lq := qMul(qConj(rotQuat(pw)), qw)
				t, s := matTS(local[p.node])
				local[p.node] = vrm.TRS(t, [4]float32(lq), s)
			}
			simW[pi] = pw.Mul(local[p.node])
		}
	}
}
