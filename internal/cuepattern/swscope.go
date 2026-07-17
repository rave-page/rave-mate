package cuepattern

// Software-scoped cues: CuePoint.Sw narrows a cue to one DJ software ("" = every app).
// The editor's mode decides the scope new cues get; write-back exports only the target's
// scope. All slot/collision accounting here is per-scope - a Traktor cue and a Rekordbox
// cue may share a position or a pad slot without conflict.

import (
	"math"
	"sort"

	"rave.page/mate/internal/musiclib"
)

// InScope reports whether cue c belongs to software scope sw ("" = the all view).
func InScope(c musiclib.CuePoint, sw string) bool {
	return sw == "" || c.Sw == "" || c.Sw == sw
}

// FilterForSoftware returns the cues software sw sees (Sw "" or == sw); grid/load/fade
// markers always pass. This is the write-back export set.
func FilterForSoftware(cues []musiclib.CuePoint, sw string) []musiclib.CuePoint {
	out := make([]musiclib.CuePoint, 0, len(cues))
	for _, c := range cues {
		if isMusical(c.Kind) && !InScope(c, sw) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ClearMusical removes the musical cues (hot/memory/loop) in scope sw; grid/load/fade
// and other scopes' cues survive. Returns the new list + how many were removed.
func ClearMusical(cues []musiclib.CuePoint, sw string) ([]musiclib.CuePoint, int) {
	out := make([]musiclib.CuePoint, 0, len(cues))
	n := 0
	for _, c := range cues {
		if isMusical(c.Kind) && InScope(c, sw) {
			n++
			continue
		}
		out = append(out, c)
	}
	if n == 0 {
		return cues, 0
	}
	return out, n
}

func isMusical(k musiclib.CueKind) bool {
	return k == musiclib.CueHot || k == musiclib.CuePlain || k == musiclib.CueLoop
}

// CapPads enforces a hotcue pad budget within scope sw: at most max in-scope hotcues
// survive, the rest demote to memory cues. Keep priority = distance to the nearest drop
// (closest first); with several drops and splitEven, the budget splits evenly across the
// drops (remainder to the earlier drops) so every drop keeps its closest cues - then any
// leftover capacity refills globally by distance. No drops = keep the earliest by time.
// Kept cues keep their slot when it fits under max, else take the lowest free one.
// Out-of-scope hotcues are untouched (their pads live in another software's world).
// Returns the new list + how many pads were demoted.
func CapPads(cues []musiclib.CuePoint, dropsMs []float64, sw string, max int, splitEven bool) ([]musiclib.CuePoint, int) {
	if max <= 0 {
		max = hotcueSlots
	}
	var hot []int // indices of in-scope hotcues
	for i, c := range cues {
		if c.Kind == musiclib.CueHot && InScope(c, sw) {
			hot = append(hot, i)
		}
	}
	if len(hot) == 0 {
		return cues, 0
	}
	drops := append([]float64(nil), dropsMs...)
	sort.Float64s(drops)

	// rank key: distance to the nearest drop; no drops = time order
	dist := func(i int) float64 {
		if len(drops) == 0 {
			return cues[i].StartMs
		}
		d := math.Inf(1)
		for _, dm := range drops {
			if v := math.Abs(cues[i].StartMs - dm); v < d {
				d = v
			}
		}
		return d
	}
	nearestDrop := func(i int) int {
		bi, bd := 0, math.Inf(1)
		for di, dm := range drops {
			if v := math.Abs(cues[i].StartMs - dm); v < bd {
				bi, bd = di, v
			}
		}
		return bi
	}

	keep := map[int]bool{}
	switch {
	case len(hot) <= max:
		for _, i := range hot {
			keep[i] = true
		}
	case splitEven && len(drops) > 1:
		// per-drop buckets, budget split evenly (remainder to earlier drops)
		buckets := make([][]int, len(drops))
		for _, i := range hot {
			di := nearestDrop(i)
			buckets[di] = append(buckets[di], i)
		}
		base, rem := max/len(drops), max%len(drops)
		spare := 0
		var rest []int
		for di, b := range buckets {
			budget := base
			if di < rem {
				budget++
			}
			sort.SliceStable(b, func(x, y int) bool { return dist(b[x]) < dist(b[y]) })
			if len(b) < budget {
				spare += budget - len(b)
				budget = len(b)
			}
			for _, i := range b[:budget] {
				keep[i] = true
			}
			rest = append(rest, b[budget:]...)
		}
		// refill unused capacity globally, closest first
		sort.SliceStable(rest, func(x, y int) bool { return dist(rest[x]) < dist(rest[y]) })
		for _, i := range rest {
			if spare == 0 {
				break
			}
			keep[i] = true
			spare--
		}
	default:
		ranked := append([]int(nil), hot...)
		sort.SliceStable(ranked, func(x, y int) bool { return dist(ranked[x]) < dist(ranked[y]) })
		for _, i := range ranked[:max] {
			keep[i] = true
		}
	}

	out := append([]musiclib.CuePoint(nil), cues...)
	demoted := 0
	for _, i := range hot {
		if !keep[i] {
			out[i].Kind = musiclib.CuePlain
			out[i].Hotcue = -1
			out[i].Type = 0
			demoted++
		}
	}
	// re-slot the keepers: keep an existing unique slot < max, else lowest free (time order)
	kept := make([]int, 0, len(keep))
	for i := range keep {
		kept = append(kept, i)
	}
	sort.SliceStable(kept, func(x, y int) bool { return out[kept[x]].StartMs < out[kept[y]].StartMs })
	used := map[int]bool{}
	var reslot []int
	for _, i := range kept {
		if s := out[i].Hotcue; s >= 0 && s < max && !used[s] {
			used[s] = true
			continue
		}
		reslot = append(reslot, i)
	}
	for _, i := range reslot {
		s := freeSlotN(used, max)
		if s < 0 { // cannot happen (len(keep) <= max) - stay safe anyway
			out[i].Kind = musiclib.CuePlain
			out[i].Hotcue = -1
			out[i].Type = 0
			demoted++
			continue
		}
		out[i].Hotcue = s
		used[s] = true
	}
	if demoted == 0 && len(reslot) == 0 {
		return cues, 0
	}
	return out, demoted
}
