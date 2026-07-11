package libdb

// Track-compatibility marks: user-declared "works well together" pairs. Symmetric,
// stored once per pair (a_path < b_path), path-keyed like playlist_tracks so marks
// survive re-imports and are source-independent. kind ∈ CompatKinds.

import (
	"fmt"
	"strings"
	"time"
)

const trackCompatSchema = `
CREATE TABLE IF NOT EXISTS track_compat (
  a_path     TEXT NOT NULL,
  b_path     TEXT NOT NULL,
  kind       TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_track_compat_pair ON track_compat(a_path, b_path, kind);
CREATE INDEX IF NOT EXISTS idx_track_compat_b ON track_compat(b_path);
`

// CompatKinds are the accepted mark kinds.
var CompatKinds = []string{"blend", "double_drop", "energy"}

// ValidCompatKind reports whether kind is one of CompatKinds.
func ValidCompatKind(kind string) bool {
	for _, k := range CompatKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// NormPair orders a pair (a ≤ b) so each pair stores once regardless of direction.
func NormPair(a, b string) (string, string) {
	if b < a {
		return b, a
	}
	return a, b
}

// CompatRow is one mark seen from a track: the partner path + kind.
type CompatRow struct {
	Path, Kind, CreatedAt string
}

// AddCompatPairs marks every C(n,2) pair among paths with kind (deduped, one tx).
// Returns how many pairs were newly added.
func (d *DB) AddCompatPairs(kind string, paths []string) (int, error) {
	if !ValidCompatKind(kind) {
		return 0, fmt.Errorf("invalid compat kind %q", kind)
	}
	uniq := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		if p != "" && !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	if len(uniq) < 2 {
		return 0, fmt.Errorf("need at least 2 tracks")
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	added := 0
	for i := 0; i < len(uniq); i++ {
		for j := i + 1; j < len(uniq); j++ {
			a, b := NormPair(uniq[i], uniq[j])
			res, err := tx.Exec(`INSERT OR IGNORE INTO track_compat (a_path, b_path, kind, created_at) VALUES (?,?,?,?)`,
				a, b, kind, now)
			if err != nil {
				_ = tx.Rollback()
				return 0, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added++
			}
		}
	}
	return added, tx.Commit()
}

// RemoveCompat deletes one pair's mark (kind "" = all kinds for the pair).
func (d *DB) RemoveCompat(a, b, kind string) error {
	a, b = NormPair(a, b)
	if kind == "" {
		_, err := d.db.Exec(`DELETE FROM track_compat WHERE a_path=? AND b_path=?`, a, b)
		return err
	}
	_, err := d.db.Exec(`DELETE FROM track_compat WHERE a_path=? AND b_path=? AND kind=?`, a, b, kind)
	return err
}

// CompatFor returns path's direct marks (partner + kind), partner-sorted.
func (d *DB) CompatFor(path string) ([]CompatRow, error) {
	rows, err := d.db.Query(`
		SELECT CASE WHEN a_path=? THEN b_path ELSE a_path END, kind, created_at
		FROM track_compat WHERE a_path=? OR b_path=?
		ORDER BY 1, kind`, path, path, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CompatRow
	for rows.Next() {
		var r CompatRow
		if err := rows.Scan(&r.Path, &r.Kind, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CompatForMany maps each input path to its direct marks (one IN query; input capped
// at 500 paths - callers pass bounded neighbor sets, not whole collections).
func (d *DB) CompatForMany(paths []string) (map[string][]CompatRow, error) {
	out := map[string][]CompatRow{}
	if len(paths) == 0 {
		return out, nil
	}
	if len(paths) > 500 {
		paths = paths[:500]
	}
	ph := strings.Repeat("?,", len(paths))
	ph = ph[:len(ph)-1]
	args := make([]any, 0, 2*len(paths))
	for _, p := range paths {
		args = append(args, p)
	}
	for _, p := range paths {
		args = append(args, p)
	}
	rows, err := d.db.Query(`SELECT a_path, b_path, kind, created_at FROM track_compat
		WHERE a_path IN (`+ph+`) OR b_path IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	want := make(map[string]bool, len(paths))
	for _, p := range paths {
		want[p] = true
	}
	for rows.Next() {
		var a, b, kind, at string
		if err := rows.Scan(&a, &b, &kind, &at); err != nil {
			return nil, err
		}
		if want[a] {
			out[a] = append(out[a], CompatRow{Path: b, Kind: kind, CreatedAt: at})
		}
		if want[b] {
			out[b] = append(out[b], CompatRow{Path: a, Kind: kind, CreatedAt: at})
		}
	}
	return out, rows.Err()
}
