// Package cuepattern is the cue-pattern engine: beat math over a track's beatgrid,
// drop markers as anchors, and reusable cue patterns (beat-offset cue sets) that are
// extracted from gridded tracks and re-applied around the drops of other tracks.
package cuepattern

import (
	"fmt"
	"math"
	"sort"

	"rave.page/mate/internal/musiclib"
)

// seg is one constant-tempo span: [startMs, endMs) at beatMs per beat. The first
// segment extends backward to 0 and the last forward to the track end, so beat
// positions exist across the whole track (audio before the first marker still has
// beats - they continue the first segment's tempo backward).
type seg struct {
	startMs, endMs float64
	anchorMs       float64 // a known on-beat position inside/defining the segment
	beatMs         float64
}

// Grid provides beat-precise positions over a track's grid markers.
type Grid struct {
	segs  []seg
	durMs float64
}

// NewGrid builds beat math from grid markers (variable grids = one segment per marker).
func NewGrid(markers []musiclib.GridMarker, durMs float64) (*Grid, error) {
	var ms []musiclib.GridMarker
	for _, m := range markers {
		if m.BPM > 0 {
			ms = append(ms, m)
		}
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("cuepattern: track has no beatgrid")
	}
	if durMs <= 0 {
		durMs = math.MaxFloat64 / 4 // unknown duration: don't clamp
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].PositionMs < ms[j].PositionMs })
	g := &Grid{durMs: durMs}
	for i, m := range ms {
		s := seg{anchorMs: m.PositionMs, beatMs: 60000 / m.BPM}
		if i == 0 {
			s.startMs = 0
		} else {
			s.startMs = m.PositionMs
		}
		if i == len(ms)-1 {
			s.endMs = durMs
		} else {
			s.endMs = ms[i+1].PositionMs
		}
		if s.endMs > s.startMs {
			g.segs = append(g.segs, s)
		}
	}
	if len(g.segs) == 0 { // degenerate marker set - keep the first as a whole-track segment
		g.segs = []seg{{startMs: 0, endMs: durMs, anchorMs: ms[0].PositionMs, beatMs: 60000 / ms[0].BPM}}
	}
	return g, nil
}

// segAt returns the segment containing ms (clamped to the first/last).
func (g *Grid) segAt(ms float64) *seg {
	for i := range g.segs {
		if ms < g.segs[i].endMs || i == len(g.segs)-1 {
			if ms >= g.segs[i].startMs || i == 0 {
				return &g.segs[i]
			}
		}
	}
	return &g.segs[len(g.segs)-1]
}

// BeatLenMs is the beat length at position ms.
func (g *Grid) BeatLenMs(ms float64) float64 { return g.segAt(ms).beatMs }

// AnchorMs is the first grid marker's position - the grid's pinned anchor ("beat 1").
// Beats extend backward from here, so it's the true reference for rendering the anchor.
func (g *Grid) AnchorMs() float64 {
	if len(g.segs) == 0 {
		return 0
	}
	return g.segs[0].anchorMs
}

// SnapMs returns the nearest beat line to ms.
func (g *Grid) SnapMs(ms float64) float64 {
	s := g.segAt(ms)
	k := math.Round((ms - s.anchorMs) / s.beatMs)
	return g.clamp(s.anchorMs + k*s.beatMs)
}

// StepMs moves from ms by beats (negative = backward), landing on a beat line.
func (g *Grid) StepMs(ms float64, beats float64) float64 {
	return g.clamp(g.OffsetMs(g.SnapMs(ms), beats))
}

// OffsetMs walks beats (fractional, signed) from a start position across segments.
// NOT clamped - callers checking span fit need to see under/overshoot (StepMs clamps).
func (g *Grid) OffsetMs(fromMs, beats float64) float64 {
	pos, left := fromMs, beats
	for left != 0 {
		s := g.segAt(pos)
		if left > 0 {
			room := (s.endMs - pos) / s.beatMs
			if left <= room || s.endMs >= g.durMs || s == &g.segs[len(g.segs)-1] {
				return pos + left*s.beatMs
			}
			pos, left = s.endMs, left-room
		} else {
			room := (pos - s.startMs) / s.beatMs
			if -left <= room || s.startMs <= 0 || s == &g.segs[0] {
				return pos + left*s.beatMs
			}
			pos, left = s.startMs, left+room
		}
	}
	return pos
}

// BeatsBetween is the signed beat distance from fromMs to toMs across segments.
func (g *Grid) BeatsBetween(fromMs, toMs float64) float64 {
	if toMs < fromMs {
		return -g.BeatsBetween(toMs, fromMs)
	}
	pos, beats := fromMs, 0.0
	for pos < toMs {
		s := g.segAt(pos)
		end := math.Min(s.endMs, toMs)
		if end <= pos { // at the final segment boundary
			end = toMs
		}
		beats += (end - pos) / s.beatMs
		if end >= toMs {
			break
		}
		pos = end
	}
	return beats
}

func (g *Grid) clamp(ms float64) float64 {
	if ms < 0 {
		return 0
	}
	if ms > g.durMs {
		return g.durMs
	}
	return ms
}
