// Package libsync is the cross-DJ-software library sync engine. rave-mate's merged music.db is
// the source of truth: tracks from every imported source (Traktor, Rekordbox, VirtualDJ, …) are
// grouped by portable hash and merged - per field-priority rules - into one canonical track, then
// written to each target (importable file / live write-back / file tags). All local.
package libsync

import (
	"os"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// defaultAppOrder ranks source apps when no per-field rule applies (richest analysis first).
var defaultAppOrder = []string{"rekordbox", "traktor", "serato", "enginedj", "virtualdj"}

// MergeFields are the canonical field names a rule may target.
var MergeFields = []string{"beatgrid", "cues", "rating", "key", "genre", "bpm", "comment", "playCount", "label", "album"}

// hasField reports whether a track carries a value for the named field.
func hasField(t musiclib.Track, field string) bool {
	switch field {
	case "beatgrid":
		return len(t.Beatgrid) > 0
	case "cues":
		return len(t.Cues) > 0
	case "rating":
		return t.Rating > 0
	case "key":
		return t.Key != ""
	case "genre":
		return t.Genre != ""
	case "bpm":
		return t.BPM > 0
	case "comment":
		return t.Comment != ""
	case "playCount":
		return t.PlayCount > 0
	case "label":
		return t.Label != ""
	case "album":
		return t.Album != ""
	}
	return false
}

// appOrder returns the candidate-app order for a field: the rule's preferred app first (if any),
// then the default richness order, deduped.
func appOrder(preferred string) []string {
	if preferred == "" {
		return defaultAppOrder
	}
	out := []string{preferred}
	for _, a := range defaultAppOrder {
		if a != preferred {
			out = append(out, a)
		}
	}
	return out
}

// pick returns the highest-priority candidate carrying the field, else any candidate that has it.
func pick(cands []libdb.SourcedTrack, field, preferred string) (musiclib.Track, bool) {
	for _, app := range appOrder(preferred) {
		for _, c := range cands {
			if c.App == app && hasField(c.Track, field) {
				return c.Track, true
			}
		}
	}
	for _, c := range cands {
		if hasField(c.Track, field) {
			return c.Track, true
		}
	}
	return musiclib.Track{}, false
}

// MergeCanonical builds one canonical track from same-identity candidates across sources. Each
// field is taken from the highest-priority source that has it (fieldSource overrides the default
// per field). Identity (artist/title) + path come from the best-available candidate.
func MergeCanonical(cands []libdb.SourcedTrack, fieldSource map[string]string) musiclib.Track {
	var out musiclib.Track
	if len(cands) == 0 {
		return out
	}
	// Identity + path: prefer a candidate whose file exists on disk, else the first.
	base := cands[0].Track
	for _, c := range cands {
		if c.Track.Path != "" && fileExists(c.Track.Path) {
			base = c.Track
			break
		}
	}
	out.Path, out.Artist, out.Title = base.Path, base.Artist, base.Title
	out.Album, out.BitrateBps, out.FileSizeKB = base.Album, base.BitrateBps, base.FileSizeKB
	out.ImportDate, out.ReleaseDate, out.LastPlayed = base.ImportDate, base.ReleaseDate, base.LastPlayed

	// Longest known duration wins (more complete metadata).
	for _, c := range cands {
		if c.Track.DurationSec > out.DurationSec {
			out.DurationSec = c.Track.DurationSec
		}
	}

	fs := func(field string) string {
		if fieldSource == nil {
			return ""
		}
		return fieldSource[field]
	}
	if t, ok := pick(cands, "beatgrid", fs("beatgrid")); ok {
		out.Beatgrid = t.Beatgrid
	}
	if t, ok := pick(cands, "cues", fs("cues")); ok {
		out.Cues = t.Cues
	}
	if t, ok := pick(cands, "bpm", fs("bpm")); ok {
		out.BPM = t.BPM
	}
	if t, ok := pick(cands, "key", fs("key")); ok {
		out.Key = t.Key
	}
	if t, ok := pick(cands, "genre", fs("genre")); ok {
		out.Genre = t.Genre
	}
	if t, ok := pick(cands, "label", fs("label")); ok {
		out.Label = t.Label
	}
	if t, ok := pick(cands, "comment", fs("comment")); ok {
		out.Comment = t.Comment
	}
	if t, ok := pick(cands, "rating", fs("rating")); ok {
		out.Rating = t.Rating
	}
	if t, ok := pick(cands, "playCount", fs("playCount")); ok {
		out.PlayCount = t.PlayCount
	}
	if out.Album == "" {
		if t, ok := pick(cands, "album", fs("album")); ok {
			out.Album = t.Album
		}
	}
	return out
}

func fileExists(p string) bool { fi, err := os.Stat(p); return err == nil && !fi.IsDir() }
