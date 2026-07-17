package cuepattern

import (
	"fmt"
	"math"
	"sort"

	"rave.page/mate/internal/musiclib"
)

// collisionEps: an applied cue landing within this of an existing cue is skipped
// (the track already has a cue there - never stack duplicates).
const collisionEps = 25.0 // ms

const hotcueSlots = 8 // pads per track (Traktor/Serato/Rekordbox baseline)

// Extract builds a pattern from a track's selected cues, offsets in beats relative to
// anchorMs (the drop). selected indexes into t.Cues; grid/load/fade cues are ignored.
func Extract(t musiclib.Track, selected []int, anchorMs float64, name string) (Pattern, error) {
	g, err := NewGrid(t.Beatgrid, t.DurationSec*1000)
	if err != nil {
		return Pattern{}, err
	}
	p := Pattern{Name: name, FromTrack: t.Title}
	for _, i := range selected {
		if i < 0 || i >= len(t.Cues) {
			continue
		}
		c := t.Cues[i]
		switch c.Kind {
		case musiclib.CueHot, musiclib.CuePlain, musiclib.CueLoop:
		default:
			continue // grid/load/fade markers aren't pattern material
		}
		pc := PatternCue{
			Beats:  g.BeatsBetween(anchorMs, c.StartMs),
			Name:   c.Name,
			Kind:   c.Kind,
			Hotcue: c.Hotcue,
		}
		if c.LenMs > 0 {
			pc.LenBeats = c.LenMs / g.BeatLenMs(c.StartMs)
		}
		p.Cues = append(p.Cues, pc)
	}
	if len(p.Cues) == 0 {
		return Pattern{}, fmt.Errorf("cuepattern: no hotcues/loops in selection")
	}
	sort.Slice(p.Cues, func(i, j int) bool { return p.Cues[i].Beats < p.Cues[j].Beats })
	return p, nil
}

// ApplyOptions controls pattern application.
type ApplyOptions struct {
	ToMemory bool // write pattern cues as memory cues (no pad slot) where supported
	SnapDrop bool // snap each drop to the nearest gridline before anchoring (default true in UI)
}

// ApplyReport says what happened per track.
type ApplyReport struct {
	Added   int // cues written
	Cut     int // pattern cues outside the drop's span (clipped per spec)
	Skipped int // collision with an existing cue
	Demoted int // wanted a pad slot but none free - written as memory cue
}

// Apply generates the track's new cue list: existing cues preserved, each drop's
// pattern laid out around it. The drop is ALWAYS the anchor; pattern cues that fall
// outside the drop's span (previous drop / track start ... next drop / track end)
// are cut. patterns maps drop index -> pattern (missing index = no pattern there).
func Apply(t musiclib.Track, dropsMs []float64, patterns map[int]Pattern, opt ApplyOptions) ([]musiclib.CuePoint, ApplyReport, error) {
	var rep ApplyReport
	g, err := NewGrid(t.Beatgrid, t.DurationSec*1000)
	if err != nil {
		return nil, rep, err
	}
	drops := append([]float64(nil), dropsMs...)
	sort.Float64s(drops)

	out := append([]musiclib.CuePoint(nil), t.Cues...)
	used := map[int]bool{}
	for _, c := range t.Cues {
		if c.Kind == musiclib.CueHot && c.Hotcue >= 0 {
			used[c.Hotcue] = true
		}
	}
	durMs := t.DurationSec * 1000
	if durMs <= 0 {
		durMs = math.MaxFloat64 / 4
	}

	for di, dropMs := range drops {
		p, ok := patterns[di]
		if !ok || len(p.Cues) == 0 {
			continue
		}
		anchor := dropMs
		if opt.SnapDrop {
			anchor = g.SnapMs(dropMs)
		}
		lo := 0.0 // span floor: previous drop (exclusive-ish) or track start
		if di > 0 {
			lo = drops[di-1]
		}
		hi := durMs // span ceiling: next drop or track end
		if di < len(drops)-1 {
			hi = drops[di+1]
		}
		for _, pc := range p.Cues {
			pos := g.OffsetMs(anchor, pc.Beats)
			// cut what doesn't fit the span (the drop itself always fits)
			if pc.Beats != 0 && (pos < lo || pos >= hi || pos < 0 || pos >= durMs) {
				rep.Cut++
				continue
			}
			if collides(out, pos) {
				rep.Skipped++
				continue
			}
			nc := musiclib.CuePoint{Name: pc.Name, StartMs: pos, Hotcue: -1}
			switch {
			case pc.Kind == musiclib.CueLoop:
				nc.Kind = musiclib.CueLoop
				nc.LenMs = pc.LenBeats * g.BeatLenMs(pos)
			case opt.ToMemory || pc.Kind == musiclib.CuePlain:
				nc.Kind = musiclib.CuePlain
			default:
				nc.Kind = musiclib.CueHot
				slot := pc.Hotcue
				if slot < 0 || slot >= hotcueSlots || used[slot] {
					slot = freeSlot(used)
				}
				if slot < 0 { // pads exhausted - keep the cue, lose the pad
					nc.Kind = musiclib.CuePlain
					rep.Demoted++
				} else {
					nc.Hotcue = slot
					used[slot] = true
				}
			}
			out = append(out, nc)
			rep.Added++
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	return out, rep, nil
}

// PromoteMemoryToHotcues assigns free pad slots (0..7) to plain (memory) cues in
// time order - the reverse of ConvertHotcuesToMemory, for tracks prepared with
// memory cues that should fire from controller hotcue pads (Traktor shows a
// HOTCUE=-1 cue as a flag but pads can't trigger it). Existing hotcue slots are
// respected; loops/grid/load/fade cues are untouched; memory cues beyond the
// free pads stay memory. Returns the new list + how many were promoted.
func PromoteMemoryToHotcues(cues []musiclib.CuePoint) ([]musiclib.CuePoint, int) {
	out := append([]musiclib.CuePoint(nil), cues...)
	used := map[int]bool{}
	for _, c := range out {
		if c.Kind == musiclib.CueHot && c.Hotcue >= 0 {
			used[c.Hotcue] = true
		}
	}
	var cand []int
	for i := range out {
		if out[i].Kind == musiclib.CuePlain {
			cand = append(cand, i)
		}
	}
	sort.SliceStable(cand, func(a, b int) bool { return out[cand[a]].StartMs < out[cand[b]].StartMs })
	n := 0
	for _, i := range cand {
		slot := freeSlot(used)
		if slot < 0 {
			break // pads exhausted - the rest stay memory cues
		}
		out[i].Kind = musiclib.CueHot
		out[i].Hotcue = slot
		used[slot] = true
		n++
	}
	if n == 0 {
		return cues, 0
	}
	return out, n
}

// ConvertHotcuesToMemory returns the cue list with every hotcue demoted to a plain
// (memory) cue - names and positions preserved, pad slots released.
func ConvertHotcuesToMemory(cues []musiclib.CuePoint) []musiclib.CuePoint {
	out := append([]musiclib.CuePoint(nil), cues...)
	n := 0
	for i := range out {
		if out[i].Kind == musiclib.CueHot {
			out[i].Kind = musiclib.CuePlain
			out[i].Hotcue = -1
			out[i].Type = 0
			n++
		}
	}
	if n == 0 {
		return cues
	}
	return out
}

func collides(cues []musiclib.CuePoint, ms float64) bool {
	for _, c := range cues {
		if c.Kind == musiclib.CueGrid {
			continue // grid anchors share positions with musical cues by design
		}
		if math.Abs(c.StartMs-ms) <= collisionEps {
			return true
		}
	}
	return false
}

func freeSlot(used map[int]bool) int {
	for s := range hotcueSlots {
		if !used[s] {
			return s
		}
	}
	return -1
}
