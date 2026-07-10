package cuepattern

import (
	"math"
	"sort"
)

// dropEps: two drops within this are the same drop (toggle-off distance).
const dropEps = 50.0 // ms

// AddDrop inserts a drop at ms (sorted, deduped). Returns the new list.
func AddDrop(drops []float64, ms float64) []float64 {
	for _, d := range drops {
		if math.Abs(d-ms) <= dropEps {
			return drops
		}
	}
	out := append(append([]float64(nil), drops...), ms)
	sort.Float64s(out)
	return out
}

// RemoveDrop removes the drop nearest ms within eps (no-op when none).
func RemoveDrop(drops []float64, ms float64) []float64 {
	best, bi := math.MaxFloat64, -1
	for i, d := range drops {
		if dd := math.Abs(d - ms); dd <= dropEps && dd < best {
			best, bi = dd, i
		}
	}
	if bi < 0 {
		return drops
	}
	return append(append([]float64(nil), drops[:bi]...), drops[bi+1:]...)
}

// NearestDrop returns the index of the drop nearest ms (-1 when empty).
func NearestDrop(drops []float64, ms float64) int {
	best, bi := math.MaxFloat64, -1
	for i, d := range drops {
		if dd := math.Abs(d - ms); dd < best {
			best, bi = dd, i
		}
	}
	return bi
}
