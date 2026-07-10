// Package tagfix scans library tracks for file-tag problems (ID3v1-only files, mojibake
// text, tags missing/diverging from the library) and applies selected repairs through
// tagwrite/tagsync - writes stay atomic, field-scoped and revertible. Scan only proposes;
// Apply never writes anything Scan didn't propose.
package tagfix

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagsync"
	"rave.page/mate/internal/tagwrite"
)

// Kind classifies a tag problem.
type Kind string

const (
	KindV1Only   Kind = "v1_only"   // ID3v1 trailer, no ID3v2 tag: propose equivalent v2 frames
	KindMojibake Kind = "mojibake"  // charset-mangled text frame: propose the repaired string
	KindMissing  Kind = "missing"   // library has BPM/key/genre, file tag lacks it
	KindMismatch Kind = "mismatch"  // file BPM/key disagrees with library
	KindNoBasics Kind = "no_basics" // file missing title/artist the library has
)

// allKinds in stable scan/report order.
var allKinds = []Kind{KindV1Only, KindMojibake, KindMissing, KindMismatch, KindNoBasics}

// Problem is one proposed single-field repair. Current is the file's value at scan time
// (Apply skips the problem if the file changed since); Proposed is the repair.
type Problem struct {
	Path, Field       string
	Kind              Kind
	Detail            string
	Current, Proposed string
}

// Options selects problem kinds (empty = all) and receives progress/skip feedback.
type Options struct {
	Kinds    []Kind
	Progress func(done, total int) // optional per-file progress
	Skipped  *int                  // optional: count of files skipped (unsupported format / unreadable)
}

// Scan inspects each track's file and returns proposed repairs. Unsupported formats
// (non-mp3/flac) and unreadable files are skipped silently, counted via opts.Skipped.
func Scan(tracks []musiclib.Track, opts Options) ([]Problem, error) {
	want := map[Kind]bool{}
	for _, k := range allKinds {
		want[k] = len(opts.Kinds) == 0
	}
	for _, k := range opts.Kinds {
		want[k] = true
	}
	skipped := 0
	var out []Problem
	for i, tr := range tracks {
		if tagwrite.Supported(tr.Path) {
			if ps, ok := scanTrack(tr, want); ok {
				out = append(out, ps...)
			} else {
				skipped++
			}
		} else {
			skipped++
		}
		if opts.Progress != nil {
			opts.Progress(i+1, len(tracks))
		}
	}
	if opts.Skipped != nil {
		*opts.Skipped = skipped
	}
	return out, nil
}

// scanTrack runs all requested checks on one file; ok=false when the file is unreadable.
func scanTrack(tr musiclib.Track, want map[Kind]bool) ([]Problem, bool) {
	cur, err := tagwrite.Read(tr.Path)
	if err != nil {
		return nil, false
	}
	var out []Problem
	if want[KindV1Only] && strings.EqualFold(filepath.Ext(tr.Path), ".mp3") {
		out = append(out, scanV1Only(tr.Path, cur)...)
	}
	if want[KindMojibake] {
		out = append(out, scanMojibake(tr.Path, cur)...)
	}
	if want[KindMissing] {
		out = append(out, scanMissing(tr, cur)...)
	}
	if want[KindMismatch] {
		out = append(out, scanMismatch(tr, cur)...)
	}
	if want[KindNoBasics] {
		out = append(out, scanNoBasics(tr, cur)...)
	}
	return out, true
}

// scanV1Only proposes v2 frames from the ID3v1 trailer when the file has no ID3v2 tag.
func scanV1Only(path string, cur tagwrite.Tags) []Problem {
	hasV2, hasV1, v1, err := v1Presence(path)
	if err != nil || hasV2 || !hasV1 {
		return nil
	}
	fields := []struct{ field, val string }{
		{tagwrite.FieldTitle, v1.title}, {tagwrite.FieldArtist, v1.artist},
		{tagwrite.FieldAlbum, v1.album}, {tagwrite.FieldYear, v1.year},
		{tagwrite.FieldGenre, v1.genre},
	}
	var out []Problem
	for _, f := range fields {
		if f.val == "" || cur[f.field] == f.val {
			continue
		}
		out = append(out, Problem{
			Path: path, Field: f.field, Kind: KindV1Only,
			Detail:  "ID3v1 trailer only (no ID3v2 tag); propose v2 frame from v1 " + f.field,
			Current: cur[f.field], Proposed: f.val,
		})
	}
	return out
}

// mojibakeFields: the text fields worth charset-checking.
var mojibakeFields = []string{
	tagwrite.FieldTitle, tagwrite.FieldArtist, tagwrite.FieldAlbum,
	tagwrite.FieldGenre, tagwrite.FieldComment, tagwrite.FieldLabel,
}

func scanMojibake(path string, cur tagwrite.Tags) []Problem {
	var out []Problem
	for _, f := range mojibakeFields {
		v := cur[f]
		if v == "" {
			continue
		}
		repaired, detail, ok := repairMojibake(v)
		if !ok || repaired == v {
			continue
		}
		out = append(out, Problem{
			Path: path, Field: f, Kind: KindMojibake,
			Detail: detail, Current: v, Proposed: repaired,
		})
	}
	return out
}

func scanMissing(tr musiclib.Track, cur tagwrite.Tags) []Problem {
	var out []Problem
	add := func(field, lib string) {
		if lib == "" || cur[field] != "" {
			return
		}
		out = append(out, Problem{
			Path: tr.Path, Field: field, Kind: KindMissing,
			Detail: "library has " + field + ", file tag empty", Proposed: lib,
		})
	}
	if tr.BPM > 0 {
		add(tagwrite.FieldBPM, strconv.FormatFloat(tr.BPM, 'f', -1, 64))
	}
	add(tagwrite.FieldKey, tr.Key)
	add(tagwrite.FieldGenre, tr.Genre)
	return out
}

func scanMismatch(tr musiclib.Track, cur tagwrite.Tags) []Problem {
	var out []Problem
	if tr.BPM > 0 && cur[tagwrite.FieldBPM] != "" {
		// Only flag a parseable tag value with a real delta (>0.05).
		if fileBPM, err := strconv.ParseFloat(strings.TrimSpace(cur[tagwrite.FieldBPM]), 64); err == nil &&
			math.Abs(fileBPM-tr.BPM) > 0.05 {
			out = append(out, Problem{
				Path: tr.Path, Field: tagwrite.FieldBPM, Kind: KindMismatch,
				Detail:  fmt.Sprintf("file bpm %s vs library %.2f", cur[tagwrite.FieldBPM], tr.BPM),
				Current: cur[tagwrite.FieldBPM], Proposed: strconv.FormatFloat(tr.BPM, 'f', -1, 64),
			})
		}
	}
	if tr.Key != "" && cur[tagwrite.FieldKey] != "" && normKey(cur[tagwrite.FieldKey]) != normKey(tr.Key) {
		out = append(out, Problem{
			Path: tr.Path, Field: tagwrite.FieldKey, Kind: KindMismatch,
			Detail:  fmt.Sprintf("file key %q vs library %q", cur[tagwrite.FieldKey], tr.Key),
			Current: cur[tagwrite.FieldKey], Proposed: tr.Key,
		})
	}
	return out
}

// normKey folds case + whitespace so "8a" == "8A", "F# m" == "f#m".
func normKey(s string) string { return strings.ToUpper(strings.Join(strings.Fields(s), "")) }

func scanNoBasics(tr musiclib.Track, cur tagwrite.Tags) []Problem {
	var out []Problem
	add := func(field, lib string) {
		if lib == "" || strings.TrimSpace(cur[field]) != "" {
			return
		}
		out = append(out, Problem{
			Path: tr.Path, Field: field, Kind: KindNoBasics,
			Detail:  "file has no " + field + "; library value from DJ-software import",
			Current: cur[field], Proposed: lib,
		})
	}
	add(tagwrite.FieldTitle, tr.Title)
	add(tagwrite.FieldArtist, tr.Artist)
	return out
}

// Apply writes the proposed repairs, grouped into ONE atomic write per file, through
// tagsync.ApplyTags (revertible snapshot + change_log for library-mapped fields). A
// problem whose Current no longer matches the file (changed since Scan) is skipped.
// Returns the number of problems applied; per-file errors are joined, the rest proceed.
func Apply(db *libdb.DB, problems []Problem) (int, error) {
	byFile := map[string][]Problem{}
	var order []string
	for _, p := range problems {
		if _, seen := byFile[p.Path]; !seen {
			order = append(order, p.Path)
		}
		byFile[p.Path] = append(byFile[p.Path], p)
	}
	applied := 0
	var errs []error
	for _, path := range order {
		if !tagwrite.Supported(path) {
			errs = append(errs, fmt.Errorf("%s: unsupported format", path))
			continue
		}
		cur, err := tagwrite.Read(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		desired := tagwrite.Tags{}
		n := 0
		for _, p := range byFile[path] {
			if cur[p.Field] != p.Current || p.Proposed == p.Current {
				continue // stale or no-op
			}
			desired[p.Field] = p.Proposed
			n++
		}
		if len(desired) == 0 {
			continue
		}
		t := musiclib.Track{Path: path}
		if db != nil {
			if lt, ok, terr := db.TrackByPath(path); terr == nil && ok {
				t = lt
			}
		}
		if _, err := tagsync.ApplyTags(db, t, desired); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		applied += n
	}
	return applied, errors.Join(errs...)
}
