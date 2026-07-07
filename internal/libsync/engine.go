package libsync

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagsync"
)

// Result summarizes one sync run (for ctl JSON + the UI).
type Result struct {
	Scanned   int             `json:"scanned"`   // candidate tracks loaded across sources
	Canonical int             `json:"canonical"` // merged tracks in scope
	Tagged    int             `json:"tagged"`    // files whose tags were written
	Dry       bool            `json:"dry"`
	Targets   []TargetOutcome `json:"targets,omitempty"`
	Errors    []string        `json:"errors,omitempty"`
}

// Summary is a one-line human description of a result.
func (r Result) Summary() string {
	parts := []string{fmt.Sprintf("%d in scope", r.Canonical)}
	for _, t := range r.Targets {
		if t.Updated+t.Added > 0 {
			parts = append(parts, fmt.Sprintf("%s/%s +%d ~%d", t.App, t.Mode, t.Added, t.Updated))
		}
	}
	if r.Tagged > 0 {
		parts = append(parts, fmt.Sprintf("%d tagged", r.Tagged))
	}
	if len(r.Errors) > 0 {
		parts = append(parts, fmt.Sprintf("%d errors", len(r.Errors)))
	}
	if r.Dry {
		return "dry-run: " + strings.Join(parts, ", ")
	}
	return strings.Join(parts, ", ")
}

// Run executes one sync job: load all sourced tracks → group by portable hash → filter to scope →
// merge each group into a canonical track → write to each target (+ file tags). dry skips writes
// and reports what would happen. db must be non-nil.
func Run(db *libdb.DB, job config.SyncJob, dry bool) (Result, error) {
	res := Result{Dry: dry}
	if db == nil {
		return res, errors.New("libsync: nil db")
	}
	all, err := db.AllSourcedTracks()
	if err != nil {
		return res, err
	}
	res.Scanned = len(all)

	// Group same-identity candidates across sources.
	groups := map[string][]libdb.SourcedTrack{}
	order := []string{}
	for _, st := range all {
		h := libdb.TrackHash(st.Track.Artist, st.Track.Title, st.Track.DurationSec)
		if _, ok := groups[h]; !ok {
			order = append(order, h)
		}
		groups[h] = append(groups[h], st)
	}

	inScope, err := scopeFilter(db, job.Scope)
	if err != nil {
		return res, err
	}

	var canonical []musiclib.Track
	for _, h := range order {
		cands := groups[h]
		if !inScope(h, cands) {
			continue
		}
		canonical = append(canonical, MergeCanonical(cands, job.Rules.FieldSource))
	}
	res.Canonical = len(canonical)
	if len(canonical) == 0 {
		return res, nil
	}

	if dry {
		for _, t := range job.Targets {
			res.Targets = append(res.Targets, TargetOutcome{
				App: t.App, Mode: t.Mode,
				Note: fmt.Sprintf("would write %d tracks", len(canonical)),
			})
		}
		return res, nil
	}

	wantTags := job.Rules.WriteFileTags
	for _, t := range job.Targets {
		if t.Mode == ModeTags {
			wantTags = true
			continue
		}
		tracks := cloneForTarget(canonical, job.Rules.HotcuesToMemory)
		out, err := applyTarget(t, tracks)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s/%s: %v", t.App, t.Mode, err))
			continue
		}
		res.Targets = append(res.Targets, out)
	}

	if wantTags {
		for _, t := range canonical {
			_, err := tagsync.Apply(db, t)
			if err != nil {
				if !errors.Is(err, tagsync.ErrUnsupported) {
					res.Errors = append(res.Errors, fmt.Sprintf("tag %s: %v", filepath.Base(t.Path), err))
				}
				continue
			}
			res.Tagged++
		}
	}
	return res, nil
}

// scopeFilter returns a predicate selecting which track groups are in the job's scope.
func scopeFilter(db *libdb.DB, sc config.SyncScope) (func(hash string, cands []libdb.SourcedTrack) bool, error) {
	switch sc.Kind {
	case "", "all":
		return func(string, []libdb.SourcedTrack) bool { return true }, nil
	case "dirs":
		prefixes := make([]string, 0, len(sc.Dirs))
		for _, d := range sc.Dirs {
			if d = strings.TrimSpace(d); d != "" {
				prefixes = append(prefixes, normPath(d))
			}
		}
		return func(_ string, cands []libdb.SourcedTrack) bool {
			for _, c := range cands {
				p := normPath(c.Track.Path)
				for _, pre := range prefixes {
					if strings.HasPrefix(p, pre) {
						return true
					}
				}
			}
			return false
		}, nil
	case "playlists":
		paths := map[string]bool{}
		for _, id := range sc.Playlists {
			ps, err := db.PlaylistTracks(id)
			if err != nil {
				return nil, err
			}
			for _, p := range ps {
				paths[normPath(p)] = true
			}
		}
		return func(_ string, cands []libdb.SourcedTrack) bool {
			for _, c := range cands {
				if paths[normPath(c.Track.Path)] {
					return true
				}
			}
			return false
		}, nil
	case "tracks":
		want := map[string]bool{}
		for _, h := range sc.TrackHashes {
			want[h] = true
		}
		return func(hash string, _ []libdb.SourcedTrack) bool { return want[hash] }, nil
	}
	return nil, fmt.Errorf("unknown scope kind %q", sc.Kind)
}

// cloneForTarget copies tracks (and their cue slices) so per-target cue transforms don't bleed
// across targets, then applies the cue rules.
func cloneForTarget(tracks []musiclib.Track, hotcuesToMemory bool) []musiclib.Track {
	out := make([]musiclib.Track, len(tracks))
	copy(out, tracks)
	if hotcuesToMemory {
		for i := range out {
			if len(out[i].Cues) > 0 {
				cs := make([]musiclib.CuePoint, len(out[i].Cues))
				copy(cs, out[i].Cues)
				out[i].Cues = cs
			}
			ApplyCueRules(&out[i], true)
		}
	}
	return out
}

// normPath lowercases + cleans a path for case-insensitive prefix/membership tests (Windows).
func normPath(p string) string { return strings.ToLower(filepath.Clean(p)) }
