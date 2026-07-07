package musiclib

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// SessionCache is an incremental LoadSessions: per-file cache keyed by (mtime, size), so a
// repeated sweep re-parses only new/changed *.nml files instead of the whole history dir
// (hundreds of years-old files otherwise re-parse via encoding/xml every sweep). In-memory
// only - the first Load after launch parses everything once. Parse failures are cached too
// (a broken file isn't retried until it changes).
type SessionCache struct {
	mu      sync.Mutex
	entries map[string]*sessionEntry

	loads, scanned, parsed  uint64 // cumulative
	lastScanned, lastParsed int    // most recent Load
}

type sessionEntry struct {
	mtime time.Time
	size  int64
	sess  Session
	ok    bool // parse succeeded
}

// NewSessionCache returns an empty cache.
func NewSessionCache() *SessionCache {
	return &SessionCache{entries: map[string]*sessionEntry{}}
}

// Load mirrors LoadSessions (every *.nml in historyDir, newest-first, read-only) but parses
// only files unseen or changed since the previous Load. Returned Sessions are shared with
// the cache - callers must treat them as immutable.
func (c *SessionCache) Load(historyDir string) ([]Session, error) {
	ents, err := os.ReadDir(historyDir)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loads++
	c.lastScanned, c.lastParsed = 0, 0

	seen := make(map[string]bool, len(ents))
	var sessions []Session
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".nml" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		c.lastScanned++
		c.scanned++
		seen[e.Name()] = true

		ent := c.entries[e.Name()]
		if ent == nil || !ent.mtime.Equal(info.ModTime()) || ent.size != info.Size() {
			ent = &sessionEntry{mtime: info.ModTime(), size: info.Size()}
			ent.sess, ent.ok = parseSessionFile(historyDir, e.Name())
			c.entries[e.Name()] = ent
			c.lastParsed++
			c.parsed++
		}
		if ent.ok {
			sessions = append(sessions, ent.sess)
		}
	}
	for name := range c.entries {
		if !seen[name] {
			delete(c.entries, name)
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartedAt.After(sessions[j].StartedAt)
	})
	return sessions, nil
}

// parseSessionFile parses one history NML, stamping StartedAt from the filename.
func parseSessionFile(dir, name string) (Session, bool) {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return Session{}, false
	}
	defer f.Close()
	s, err := ParseHistory(name, f)
	if err != nil {
		return Session{}, false
	}
	if ts, ok := ParseHistoryFilename(name); ok {
		s.StartedAt = ts
	}
	return s, true
}

// Stats renders counters for a perf probe: cached files + scanned/parsed last Load and cumulative.
func (c *SessionCache) Stats() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("cached=%d lastLoad scanned=%d parsed=%d | loads=%d scanned=%d parsed=%d",
		len(c.entries), c.lastScanned, c.lastParsed, c.loads, c.scanned, c.parsed)
}
