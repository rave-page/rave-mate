package avataratlas

// bonemap.go - humanoid §5-slot mapping WITHOUT VRM metadata (contract §11 v1.3.1 amendment).
// Deterministic name heuristics shared by FBX and plain glTF/GLB skins: namespace/prefix
// stripping (mixamorig:, VRoid J_Bip_C_/L_/R_; J_Sec_* secondary bones stay unmapped for the
// ancestor walk), case/separator/camelCase-insensitive token table covering Mixamo / VRoid /
// Mecanim / Cats-style names, left/right by token or L_/_L affix. Escape hatch: an override
// table ({nodeName: §5 slotName}, the CLI -bonemap file) extends/replaces heuristics. Missing
// CORE slots (hips/spine/head) after mapping reject with an error LISTING matched and
// unmatched joint names. All iteration is sorted - same input always maps identically.
//
// Bare "leg" is convention-ambiguous (Mixamo LeftLeg = shin, Cats "Left leg" = thigh) and
// resolves per rig: if any OTHER joint claims a lowerLeg slot (knee/calf/shin/lowerleg), bare
// "leg" means upperLeg; otherwise lowerLeg (Mixamo).

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// §5 slots by heuristic key. Sided values hold the LEFT slot; right = +4 (§5 layout:
// left block 6..9 / 14..17, right block 10..13 / 18..21).
var centerSlotByKey = map[string]int{
	"hips": 0, "hip": 0, "pelvis": 0,
	"spine": 1,
	"chest": 2, "spine1": 2,
	"upperchest": 3, "spine2": 3,
	"neck": 4,
	"head": 5,
}

var leftSlotByKey = map[string]int{
	"shoulder": 6, "clavicle": 6, "collar": 6,
	"upperarm": 7, "uparm": 7, "arm": 7,
	"lowerarm": 8, "forearm": 8, "elbow": 8,
	"hand": 9, "wrist": 9,
	"upperleg": 14, "upleg": 14, "thigh": 14,
	"lowerleg": 15, "knee": 15, "calf": 15, "shin": 15,
	"foot": 16, "ankle": 16,
	"toe": 17, "toes": 17, "toebase": 17,
}

const rightSlotOffset = 4

type boneSide int

const (
	sideNone boneSide = iota
	sideLeft
	sideRight
)

// boneTokens splits a name into lowercase tokens at separators, camelCase boundaries and
// letter<->digit transitions ("LeftUpLeg" -> left up leg; "Spine1" -> spine 1).
func boneTokens(s string) []string {
	var toks []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			toks = append(toks, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	prev := rune(0)
	for _, r := range s {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
			prev = 0
			continue
		case len(cur) > 0 && unicode.IsUpper(r) && unicode.IsLower(prev),
			len(cur) > 0 && unicode.IsDigit(r) != unicode.IsDigit(prev):
			flush()
		}
		cur = append(cur, r)
		prev = r
	}
	flush()
	return toks
}

// boneKey normalizes a node name into (heuristic key, side, secondary). Namespace prefixes
// strip at the last ':'; VRoid J_Bip_ prefixes carry the side, J_Sec_* flags secondary
// (never mapped - ancestor walk). Otherwise the first left/right/l/r token is the side.
func boneKey(name string) (key string, side boneSide, secondary bool) {
	base := name
	if i := strings.LastIndexByte(base, ':'); i >= 0 {
		base = base[i+1:]
	}
	lower := strings.ToLower(base)
	switch {
	case strings.HasPrefix(lower, "j_sec_"):
		return "", sideNone, true
	case strings.HasPrefix(lower, "j_bip_c_"):
		base, side = base[len("j_bip_c_"):], sideNone
	case strings.HasPrefix(lower, "j_bip_l_"):
		base, side = base[len("j_bip_l_"):], sideLeft
	case strings.HasPrefix(lower, "j_bip_r_"):
		base, side = base[len("j_bip_r_"):], sideRight
	}
	toks := boneTokens(base)
	if side == sideNone {
		out := toks[:0]
		for _, t := range toks {
			if side == sideNone {
				switch t {
				case "left", "l":
					side = sideLeft
					continue
				case "right", "r":
					side = sideRight
					continue
				}
			}
			out = append(out, t)
		}
		toks = out
	}
	return strings.Join(toks, ""), side, false
}

// GuessSlot maps a single node name to a §5 slot via the frozen heuristics. Bare "leg"
// resolves Mixamo-style (lowerLeg) - MapHumanoid applies the rig-context rule instead.
func GuessSlot(name string) (int, bool) {
	slot, ambiguousLeg := guessSlot(name)
	if ambiguousLeg {
		return slot, true
	}
	return slot, slot >= 0
}

// guessSlot returns (slot, legAmbiguity). legAmbiguity means the name is a bare sided "leg";
// slot then holds the LOWER-leg reading (Mixamo default).
func guessSlot(name string) (int, bool) {
	key, side, secondary := boneKey(name)
	if secondary || key == "" {
		return -1, false
	}
	if side == sideNone {
		if slot, ok := centerSlotByKey[key]; ok {
			return slot, false
		}
		return -1, false
	}
	off := 0
	if side == sideRight {
		off = rightSlotOffset
	}
	if key == "leg" {
		return leftSlotByKey["lowerleg"] + off, true
	}
	if slot, ok := leftSlotByKey[key]; ok {
		return slot + off, false
	}
	return -1, false
}

// BoneMapPair is one resolved node -> §5 slot assignment.
type BoneMapPair struct {
	Node     int    `json:"node"`
	Name     string `json:"name"`
	Slot     int    `json:"slot"`
	SlotName string `json:"slotName"`
}

// BoneMapping reports how a document's humanoid mapping was produced.
type BoneMapping struct {
	Source    string        `json:"source"`         // "vrm0"/"vrm1" (metadata) or "heuristic"; "+override" suffix when a -bonemap table applied
	Pairs     []BoneMapPair `json:"mapping"`        // sorted by slot, then node
	Unmapped  []string      `json:"unmappedJoints"` // skin-joint names left unmapped (sorted, deduped)
	Conflicts int           `json:"conflicts"`      // heuristic slot contentions (lowest node index won)
}

// MapHumanoid ensures doc.NodeSlot carries a §5 humanoid mapping: VRM metadata is kept as-is,
// otherwise name heuristics run over all skin joints (ascending node order; a contested slot
// goes to the lowest node index). overrides ({nodeName: slotName}; slotName "" force-unmaps)
// applies on top either way - an override claims its slot exclusively. Missing core slots
// (hips/spine/head) reject with an error listing matched and unmatched joint names.
func MapHumanoid(doc *Document, overrides map[string]string) (*BoneMapping, error) {
	// Candidate joints: union of all skins' joint nodes, ascending.
	seen := map[int]bool{}
	var joints []int
	for si := range doc.Skins {
		for _, n := range doc.Skins[si].Joints {
			if n >= 0 && n < len(doc.Nodes) && !seen[n] {
				seen[n] = true
				joints = append(joints, n)
			}
		}
	}
	sort.Ints(joints)

	rep := &BoneMapping{}
	if len(doc.NodeSlot) > 0 {
		rep.Source = "vrm" + doc.VRMVersion
	} else {
		rep.Source = "heuristic"
		type legClaim struct {
			node, lowerSlot int
		}
		var legs []legClaim
		claimed := map[int]int{} // slot -> node
		claim := func(node, slot int) {
			if holder, ok := claimed[slot]; ok && holder <= node {
				rep.Conflicts++
				return
			} else if ok {
				rep.Conflicts++
				delete(doc.NodeSlot, holder)
			}
			claimed[slot] = node
			doc.NodeSlot[node] = slot
		}
		for _, n := range joints {
			slot, ambiguousLeg := guessSlot(doc.Nodes[n].Name)
			if ambiguousLeg {
				legs = append(legs, legClaim{node: n, lowerSlot: slot})
				continue
			}
			if slot >= 0 {
				claim(n, slot)
			}
		}
		// Bare-"leg" convention: another joint already reading as lowerLeg (knee/calf/
		// shin/lowerleg) means this rig's "leg" is the thigh (Cats); else Mixamo shin.
		_, lowerL := claimed[15]
		_, lowerR := claimed[19]
		legIsUpper := lowerL || lowerR
		for _, lc := range legs {
			slot := lc.lowerSlot
			if legIsUpper {
				slot += 14 - 15 // lowerLeg slot -> upperLeg slot (both sides: 15->14, 19->18)
			}
			claim(lc.node, slot)
		}
	}

	if len(overrides) > 0 {
		rep.Source += "+override"
		names := make([]string, 0, len(overrides))
		for k := range overrides {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			node := -1 // lowest node index with this exact name
			for i := range doc.Nodes {
				if doc.Nodes[i].Name == name {
					node = i
					break
				}
			}
			if node < 0 {
				return nil, fmt.Errorf("bonemap: override %q matches no node", name)
			}
			slotName := overrides[name]
			if slotName == "" {
				delete(doc.NodeSlot, node)
				continue
			}
			slot, ok := SlotByVRMName(slotName)
			if !ok {
				return nil, fmt.Errorf("bonemap: override %q -> unknown slot %q (want a §5 name like hips/spine/head/leftUpperArm...)", name, slotName)
			}
			for other, s := range doc.NodeSlot { // override owns its slot exclusively
				if s == slot && other != node {
					delete(doc.NodeSlot, other)
				}
			}
			doc.NodeSlot[node] = slot
		}
	}

	// Report rows (sorted by slot, then node - deterministic).
	for node, slot := range doc.NodeSlot {
		name := ""
		if node >= 0 && node < len(doc.Nodes) {
			name = doc.Nodes[node].Name
		}
		rep.Pairs = append(rep.Pairs, BoneMapPair{Node: node, Name: name, Slot: slot, SlotName: SlotName(slot)})
	}
	sort.Slice(rep.Pairs, func(i, j int) bool {
		if rep.Pairs[i].Slot != rep.Pairs[j].Slot {
			return rep.Pairs[i].Slot < rep.Pairs[j].Slot
		}
		return rep.Pairs[i].Node < rep.Pairs[j].Node
	})
	unmappedSeen := map[string]bool{}
	for _, n := range joints {
		if _, ok := doc.NodeSlot[n]; ok {
			continue
		}
		name := doc.Nodes[n].Name
		if !unmappedSeen[name] {
			unmappedSeen[name] = true
			rep.Unmapped = append(rep.Unmapped, name)
		}
	}
	sort.Strings(rep.Unmapped)

	// Core check (§5: hips 0, spine 1, head 5 mandatory).
	slotTaken := map[int]bool{}
	for _, s := range doc.NodeSlot {
		slotTaken[s] = true
	}
	var missing []string
	for _, core := range []int{0, 1, 5} {
		if !slotTaken[core] {
			missing = append(missing, SlotName(core))
		}
	}
	if len(missing) > 0 {
		var matched []string
		for _, p := range rep.Pairs {
			matched = append(matched, fmt.Sprintf("%s->%s", p.Name, p.SlotName))
		}
		matchedStr := strings.Join(matched, ", ")
		if matchedStr == "" {
			matchedStr = "(none)"
		}
		unmappedStr := strings.Join(rep.Unmapped, ", ")
		if unmappedStr == "" {
			unmappedStr = "(none)"
		}
		return nil, fmt.Errorf("bonemap: core slot(s) %s unmapped; matched: %s; unmapped joints: %s (assign them with -bonemap map.json)",
			strings.Join(missing, ", "), matchedStr, unmappedStr)
	}
	return rep, nil
}
