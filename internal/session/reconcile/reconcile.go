// Package reconcile matches a finished recording (a time window) against Traktor's
// post-session history files to produce an authoritative, timestamped tracklist. The live
// recorder captures a best-effort tracklist during the set; once Traktor closes it writes the
// definitive History/*.nml (with per-track wall-clock times). This package picks the history
// session that best overlaps the recording window and positions each played track at its
// offset into the recording - the post-hoc ground truth the live capture is reconciled to.
//
// Pure (no I/O): the caller loads sessions (musiclib.LoadSessions) and enriches paths to
// title/artist (libdb) around it. See docs/DJ_SOURCES.md "Live access … history".
package reconcile

import (
	"sort"
	"time"

	"rave.page/mate/internal/musiclib"
)

// minOverlap is the least session↔recording time overlap to accept a match (filters an
// unrelated earlier set that happens to be in the history dir).
const minOverlap = 60 * time.Second

// pad widens the recording window when deciding which played tracks belong to it - a track
// that started just before the recorder armed, or just after it stopped, still counts.
const pad = 90 * time.Second

// Track is one history-played track positioned into a recording. Title/Artist/… carry the history
// file's own embedded metadata (musiclib.PlayedTrack), so the tracklist is complete even when the file
// isn't in the live library (path-resolve miss).
type Track struct {
	Index       int           // 1-based position in the matched tracklist
	Offset      time.Duration // start offset into the recording (>= 0)
	StartedAt   time.Time     // absolute play time (from the history file)
	Path        string        // file path (resolve to title/artist via the collection)
	Deck        int
	DurationSec float64
	Title       string
	Artist      string
	Album       string
	Key         string
	BPM         float64
}

// Match is a recording matched to a history session.
type Match struct {
	SessionName string
	Tracks      []Track
	Overlap     time.Duration // session-span ∩ recording-window
}

// MatchSession picks the history session that best overlaps the recording window
// [recStart, recEnd] and maps its played tracks to recording offsets. ok=false if no session
// overlaps by at least minOverlap. A zero recEnd means open-ended (use the session's own end).
func MatchSession(recStart, recEnd time.Time, sessions []musiclib.Session) (Match, bool) {
	best := Match{}
	bestOv := time.Duration(-1)
	for _, s := range sessions {
		span0, span1, ok := sessionSpan(s)
		if !ok {
			continue
		}
		end := recEnd
		if end.IsZero() {
			end = span1
		}
		ov := overlap(recStart, end, span0, span1)
		if ov > bestOv {
			bestOv = ov
			m := buildMatch(s, recStart, end)
			m.Overlap = ov
			best = m
		}
	}
	if bestOv < minOverlap || len(best.Tracks) == 0 {
		return Match{}, false
	}
	return best, true
}

// sessionSpan returns the wall-clock span of a session's timestamped plays (ok=false if none
// carry a usable StartedAt).
func sessionSpan(s musiclib.Session) (time.Time, time.Time, bool) {
	var first, last time.Time
	for _, p := range s.Played {
		if p.StartedAt.IsZero() {
			continue
		}
		if first.IsZero() || p.StartedAt.Before(first) {
			first = p.StartedAt
		}
		endAt := p.StartedAt.Add(time.Duration(p.DurationSec) * time.Second)
		if endAt.After(last) {
			last = endAt
		}
	}
	if first.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	return first, last, true
}

// buildMatch maps a session's played tracks within [recStart-pad, recEnd+pad] to offsets.
func buildMatch(s musiclib.Session, recStart, recEnd time.Time) Match {
	lo, hi := recStart.Add(-pad), recEnd.Add(pad)
	var tracks []Track
	for _, p := range s.Played {
		if p.StartedAt.IsZero() || p.StartedAt.Before(lo) || p.StartedAt.After(hi) {
			continue
		}
		off := p.StartedAt.Sub(recStart)
		if off < 0 {
			off = 0
		}
		tracks = append(tracks, Track{
			Offset: off, StartedAt: p.StartedAt, Path: p.Path, Deck: p.Deck, DurationSec: p.DurationSec,
			Title: p.Title, Artist: p.Artist, Album: p.Album, Key: p.Key, BPM: p.BPM,
		})
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].StartedAt.Before(tracks[j].StartedAt) })
	for i := range tracks {
		tracks[i].Index = i + 1
	}
	return Match{SessionName: s.Name, Tracks: tracks}
}

// overlap returns the intersection duration of [a0,a1] and [b0,b1] (0 if disjoint).
func overlap(a0, a1, b0, b1 time.Time) time.Duration {
	start := a0
	if b0.After(start) {
		start = b0
	}
	end := a1
	if b1.Before(end) {
		end = b1
	}
	if d := end.Sub(start); d > 0 {
		return d
	}
	return 0
}
