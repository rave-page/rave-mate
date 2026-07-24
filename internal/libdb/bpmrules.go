package libdb

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/musiclib"
)

// Per-playlist + per-genre BPM target ranges. Out-of-octave analyses (DnB tagged 87
// instead of 174) fold into the band via musiclib.FoldBPM wherever tracks are
// analyzed, enforced, or synced. Playlist rules live as columns on playlists;
// genre rules (keyed by exact genre or musiclib.GenreFamily, case-folded) in
// bpm_genre_rules.

const bpmGenreRuleSchema = `
CREATE TABLE IF NOT EXISTS bpm_genre_rules (
  genre      TEXT PRIMARY KEY,  -- lower(genre) or lower(GenreFamily)
  bpm_min    REAL NOT NULL,
  bpm_max    REAL NOT NULL,
  updated_at TEXT
);`

// GenreBPMRule is one stored per-genre range.
type GenreBPMRule struct {
	Genre string
	Range musiclib.BPMRange
}

// SetPlaylistBPMRange stores (or clears, with min=max=0) a playlist's target range.
func (d *DB) SetPlaylistBPMRange(id int64, r musiclib.BPMRange) error {
	if !r.Valid() {
		r = musiclib.BPMRange{}
	}
	_, err := d.db.Exec(`UPDATE playlists SET bpm_min=?, bpm_max=?, updated_at=? WHERE id=?`,
		r.Min, r.Max, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// PlaylistBPMRange returns a playlist's target range (ok=false when unset).
func (d *DB) PlaylistBPMRange(id int64) (musiclib.BPMRange, bool) {
	var r musiclib.BPMRange
	err := d.db.QueryRow(`SELECT COALESCE(bpm_min,0), COALESCE(bpm_max,0) FROM playlists WHERE id=?`, id).
		Scan(&r.Min, &r.Max)
	return r, err == nil && r.Valid()
}

// SetGenreBPMRange upserts (or deletes, with min=max=0) a genre's target range.
func (d *DB) SetGenreBPMRange(genre string, r musiclib.BPMRange) error {
	key := strings.ToLower(strings.TrimSpace(genre))
	if key == "" {
		return nil
	}
	if !r.Valid() {
		_, err := d.db.Exec(`DELETE FROM bpm_genre_rules WHERE genre=?`, key)
		return err
	}
	_, err := d.db.Exec(`INSERT INTO bpm_genre_rules (genre, bpm_min, bpm_max, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(genre) DO UPDATE SET bpm_min=excluded.bpm_min, bpm_max=excluded.bpm_max, updated_at=excluded.updated_at`,
		key, r.Min, r.Max, time.Now().UTC().Format(time.RFC3339))
	return err
}

// GenreBPMRules lists stored per-genre ranges, sorted by genre.
func (d *DB) GenreBPMRules() ([]GenreBPMRule, error) {
	rows, err := d.db.Query(`SELECT genre, bpm_min, bpm_max FROM bpm_genre_rules ORDER BY genre`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GenreBPMRule
	for rows.Next() {
		var g GenreBPMRule
		if err := rows.Scan(&g.Genre, &g.Range.Min, &g.Range.Max); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UpdateTrackBPMFold persists a folded BPM + scaled beatgrid on every source row
// carrying the path (mirrors UpdateTrackBeatgrid) and journals both fields.
// old must be the pre-fold track; folded carries the new BPM/Beatgrid.
func (d *DB) UpdateTrackBPMFold(old, folded musiclib.Track) error {
	if d == nil || d.db == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.db.Exec(`UPDATE tracks SET bpm=?, updated_at=? WHERE path=?`,
		folded.BPM, now, old.Path); err != nil {
		return err
	}
	h := TrackHash(old.Artist, old.Title, old.DurationSec)
	evs := []ChangeEvent{{
		TrackHash: h, Path: old.Path, Field: "bpm", Op: "set",
		OldValue: jsonNum(old.BPM), NewValue: jsonNum(folded.BPM), Origin: "bpmrange",
	}}
	if len(folded.Beatgrid) > 0 {
		raw, err := json.Marshal(folded.Beatgrid)
		if err != nil {
			return err
		}
		oldRaw, _ := json.Marshal(old.Beatgrid)
		if _, err := d.db.Exec(`UPDATE tracks SET beatgrid=?, updated_at=? WHERE path=?`,
			string(raw), now, old.Path); err != nil {
			return err
		}
		evs = append(evs, ChangeEvent{
			TrackHash: h, Path: old.Path, Field: "beatgrid", Op: "set",
			OldValue: string(oldRaw), NewValue: string(raw), Origin: "bpmrange",
		})
	}
	d.bumpTracks()
	return d.AppendChanges(evs)
}

func jsonNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// BPMRules resolves a track's target range: playlist membership first (narrowest
// band wins when a track is in several ruled playlists - most specific intent),
// then exact genre, then genre family.
type BPMRules struct {
	byPath map[string]musiclib.BPMRange // key: lower(path)
	genre  map[string]musiclib.BPMRange // key: lower(genre|family)
}

// Empty reports whether no rules exist at all.
func (r *BPMRules) Empty() bool {
	return r == nil || (len(r.byPath) == 0 && len(r.genre) == 0)
}

// Resolve returns the target range for a track path+genre (ok=false when unruled).
func (r *BPMRules) Resolve(path, genre string) (musiclib.BPMRange, bool) {
	if r == nil {
		return musiclib.BPMRange{}, false
	}
	if rg, ok := r.byPath[strings.ToLower(path)]; ok {
		return rg, true
	}
	g := strings.ToLower(strings.TrimSpace(genre))
	if g == "" {
		return musiclib.BPMRange{}, false
	}
	if rg, ok := r.genre[g]; ok {
		return rg, true
	}
	if fam := strings.ToLower(musiclib.GenreFamily(genre)); fam != "" && fam != g {
		if rg, ok := r.genre[fam]; ok {
			return rg, true
		}
	}
	return musiclib.BPMRange{}, false
}

// LoadBPMRules snapshots all playlist + genre range rules for fast per-track lookup.
func (d *DB) LoadBPMRules() (*BPMRules, error) {
	if d == nil || d.db == nil {
		return &BPMRules{}, nil
	}
	out := &BPMRules{byPath: map[string]musiclib.BPMRange{}, genre: map[string]musiclib.BPMRange{}}
	rows, err := d.db.Query(`SELECT pt.path, p.bpm_min, p.bpm_max FROM playlists p
		JOIN playlist_tracks pt ON pt.playlist_id = p.id
		WHERE COALESCE(p.bpm_min,0) > 0 AND COALESCE(p.bpm_max,0) >= p.bpm_min`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var r musiclib.BPMRange
		if err := rows.Scan(&path, &r.Min, &r.Max); err != nil {
			return nil, err
		}
		key := strings.ToLower(path)
		if prev, ok := out.byPath[key]; !ok || r.Max-r.Min < prev.Max-prev.Min {
			out.byPath[key] = r
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	gr, err := d.GenreBPMRules()
	if err != nil {
		return nil, err
	}
	for _, g := range gr {
		out.genre[g.Genre] = g.Range
	}
	return out, nil
}
