// Package gistseq maintains monotonic, per-module sequence numbers persisted across restarts -
// the backbone of the VRChat world's live-config SEQ-GATE (docs/WORLD_BRIDGE_CONTRACT.md).
//
// The world commits a module gist only when its seq STRICTLY increases; a reused seq is treated
// as stale (ignored), a non-advanced seq hides a fresh write. rave-mate dedups gist writes by
// CONTENT, so a seq is consumed only on an actual write - this counter guarantees the
// strictly-increasing invariant survives process restarts (a plain in-memory counter would reset
// to 0 on relaunch and re-issue already-committed seqs, wedging the world on last-good).
package gistseq

import (
	"encoding/json"
	"os"
	"sync"
)

// Counter is a persisted module-key -> last-issued-seq ledger. Safe for concurrent use.
type Counter struct {
	mu   sync.Mutex
	path string
	seqs map[string]int64
}

// Open loads the ledger from path. A missing or corrupt file yields an empty counter (not an
// error): a lost ledger risks at most a one-time seq reset, and the world holds last-good until
// seq climbs back past the committed value.
func Open(path string) *Counter {
	c := &Counter{path: path, seqs: map[string]int64{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &c.seqs)
	}
	return c
}

// Next issues the next seq for module (last+1) and persists BEFORE returning, so a crash after the
// caller writes its gist can never re-issue the same seq. Persist is best-effort: on a write
// failure the in-process counter stays monotonic; only cross-restart monotonicity is at risk,
// bounded to a single reset.
func (c *Counter) Next(module string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seqs[module]++
	n := c.seqs[module]
	c.persist()
	return n
}

// Peek returns the last-issued seq for module (0 if never issued). Since a seq is issued only on
// a real write, Peek == the seq the world currently sees for that module.
func (c *Counter) Peek(module string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seqs[module]
}

// persist writes the ledger atomically (tmp + rename). Caller holds the lock.
func (c *Counter) persist() {
	if c.path == "" {
		return
	}
	data, err := json.MarshalIndent(c.seqs, "", "  ")
	if err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, c.path)
}
