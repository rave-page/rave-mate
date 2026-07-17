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
	ToMemory  bool   // write pattern cues as memory cues (no pad slot) where supported
	SnapDrop  bool   // snap each drop to the nearest gridline before anchoring (default true in UI)
	Software  string // scope: new cues carry this tag; collisions + pad slots count only this scope ("" = all)
	Overwrite bool   // clear the in-scope musical cues first - the patterns REPLACE the track's cue set
	MaxPads   int    // hotcue pad budget for this scope (0 = the 8-pad baseline)
}

// ApplyReport says what happened per track.
type ApplyReport struct {
	Added    int // cues written
	Cut      int // pattern cues outside the drop's span (clipped per spec)
	Skipped  int // collision with an existing cue
	Demoted  int // wanted a pad slot but none free - written as memory cue
	Replaced int // pre-existing cues cleared by Overwrite
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

	maxPads := opt.MaxPads
	if maxPads <= 0 {
		maxPads = hotcueSlots
	}
	out := append([]musiclib.CuePoint(nil), t.Cues...)
	if opt.Overwrite {
		out, rep.Replaced = ClearMusical(out, opt.Software)
	}
	used := map[int]bool{}
	for _, c := range out {
		if c.Kind == musiclib.CueHot && c.Hotcue >= 0 && InScope(c, opt.Software) {
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
			if collides(out, pos, opt.Software) {
				rep.Skipped++
				continue
			}
			nc := musiclib.CuePoint{Name: pc.Name, StartMs: pos, Hotcue: -1, Sw: opt.Software}
			switch {
			case pc.Kind == musiclib.CueLoop:
				nc.Kind = musiclib.CueLoop
				nc.LenMs = pc.LenBeats * g.BeatLenMs(pos)
			case opt.ToMemory || pc.Kind == musiclib.CuePlain:
				nc.Kind = musiclib.CuePlain
			default:
				nc.Kind = musiclib.CueHot
				slot := pc.Hotcue
				if slot < 0 || slot >= maxPads || used[slot] {
					slot = freeSlotN(used, maxPads)
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

// PromoteMemoryToHotcues assigns free pad slots to plain (memory) cues in time
// order - the reverse of ConvertHotcuesToMemory, for tracks prepared with
// memory cues that should fire from controller hotcue pads (Traktor shows a
// HOTCUE=-1 cue as a flag but pads can't trigger it). Scope-aware: only cues in
// scope sw promote, and only that scope's slots count. max caps the pad budget
// (0 = the 8-pad baseline). Existing hotcue slots are respected; loops/grid/
// load/fade cues are untouched; memory cues beyond the free pads stay memory.
// Returns the new list + how many were promoted.
func PromoteMemoryToHotcues(cues []musiclib.CuePoint, sw string, max int) ([]musiclib.CuePoint, int) {
	if max <= 0 {
		max = hotcueSlots
	}
	out := append([]musiclib.CuePoint(nil), cues...)
	used := map[int]bool{}
	for _, c := range out {
		if c.Kind == musiclib.CueHot && c.Hotcue >= 0 && InScope(c, sw) {
			used[c.Hotcue] = true
		}
	}
	var cand []int
	for i := range out {
		if out[i].Kind == musiclib.CuePlain && InScope(out[i], sw) {
			cand = append(cand, i)
		}
	}
	sort.SliceStable(cand, func(a, b int) bool { return out[cand[a]].StartMs < out[cand[b]].StartMs })
	n := 0
	for _, i := range cand {
		slot := freeSlotN(used, max)
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

// ConvertHotcuesToMemory returns the cue list with every in-scope hotcue demoted to a
// plain (memory) cue - names and positions preserved, pad slots released.
func ConvertHotcuesToMemory(cues []musiclib.CuePoint, sw string) []musiclib.CuePoint {
	out := append([]musiclib.CuePoint(nil), cues...)
	n := 0
	for i := range out {
		if out[i].Kind == musiclib.CueHot && InScope(out[i], sw) {
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

// collides: only cues software sw can see count - a Traktor cue and a Rekordbox cue may
// share a beat.
func collides(cues []musiclib.CuePoint, ms float64, sw string) bool {
	for _, c := range cues {
		if c.Kind == musiclib.CueGrid {
			continue // grid anchors share positions with musical cues by design
		}
		if !InScope(c, sw) {
			continue
		}
		if math.Abs(c.StartMs-ms) <= collisionEps {
			return true
		}
	}
	return false
}

// freeSlotN returns the lowest unused pad slot < n (-1 = none).
func freeSlotN(used map[int]bool, n int) int {
	for s := range n {
		if !used[s] {
			return s
		}
	}
	return -1
}
