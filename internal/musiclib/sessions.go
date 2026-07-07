package musiclib

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// SessionSummary is a lightweight digest of a Session.
type SessionSummary struct {
	Name             string
	StartedAt        time.Time
	TrackCount       int
	TotalDurationSec float64
}

// Summarize computes a SessionSummary from s.
func Summarize(s Session) SessionSummary {
	sum := SessionSummary{
		Name:       s.Name,
		StartedAt:  s.StartedAt,
		TrackCount: len(s.Played),
	}
	for _, p := range s.Played {
		sum.TotalDurationSec += p.DurationSec
	}
	return sum
}

// historyFilenameRE matches e.g. "history_2025y02m28d_14h47m09s.nml".
var historyFilenameRE = regexp.MustCompile(
	`^history_(\d{4})y(\d{2})m(\d{2})d_(\d{2})h(\d{2})m(\d{2})s\.nml$`,
)

// ParseHistoryFilename extracts the timestamp from a Traktor history filename.
// Returns ok=false if name doesn't match the expected pattern.
func ParseHistoryFilename(name string) (time.Time, bool) {
	m := historyFilenameRE.FindStringSubmatch(filepath.Base(name))
	if m == nil {
		return time.Time{}, false
	}
	t := time.Date(
		atoiSafe(m[1]), time.Month(atoiSafe(m[2])), atoiSafe(m[3]),
		atoiSafe(m[4]), atoiSafe(m[5]), atoiSafe(m[6]),
		0, time.Local,
	)
	return t, true
}

// LoadSessions reads every *.nml in historyDir, parses each via ParseHistory,
// stamps Session.StartedAt from the filename, and returns them newest-first.
// Files that fail to parse are silently skipped. Read-only - never modifies historyDir.
func LoadSessions(historyDir string) ([]Session, error) {
	ents, err := os.ReadDir(historyDir)
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".nml" {
			continue
		}
		path := filepath.Join(historyDir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		s, err := ParseHistory(e.Name(), f)
		f.Close()
		if err != nil {
			continue
		}
		if ts, ok := ParseHistoryFilename(e.Name()); ok {
			s.StartedAt = ts
		}
		sessions = append(sessions, s)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartedAt.After(sessions[j].StartedAt)
	})
	return sessions, nil
}
