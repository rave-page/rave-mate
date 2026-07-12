// Package remotecache is a byte-verbatim content cache for library files pulled from paired
// peers (remote cue editing, #89): entries live under
// <root>/<peerNodeID[:8]>/<sha256(path)[:16]>-<mtimeUnix>-<basename> so the same source path on
// two peers - or two mtimes of one file - never collide. Writes go through a .part temp +
// rename (a torn pull is never visible as a cache hit); eviction is LRU by file mtime under a
// byte cap (Lookup touches the mtime - atime is unreliable on Windows volumes).
package remotecache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultCap is the byte cap for evict (Settings knob is P3).
const DefaultCap int64 = 4 << 30

// partMaxAge: .part temps older than this are orphans (crashed pull) and get swept by evict.
const partMaxAge = time.Hour

// Cache is one on-disk cache root. Safe for concurrent use.
type Cache struct {
	mu   sync.Mutex
	root string
	cap  int64
}

// New builds a cache at root with the given byte cap (<=0 = DefaultCap). The dir is created lazily.
func New(root string, capBytes int64) *Cache {
	if capBytes <= 0 {
		capBytes = DefaultCap
	}
	return &Cache{root: root, cap: capBytes}
}

// peerKey folds a peer nodeID to its dir name. NodeIDs are base64url (fs-safe) - the filter is
// belt-and-braces for foreign input.
func peerKey(peer string) string {
	k := sanitize(peer)
	if len(k) > 8 {
		k = k[:8]
	}
	if k == "" {
		k = "peer"
	}
	return k
}

// entryName keys one (source path, mtime) version: path hash + mtime + a readable basename
// (extension kept - probes sniff nicer with it).
func entryName(path string, mtimeUnix int64) string {
	h := sha256.Sum256([]byte(path))
	base := sanitize(baseName(path))
	if len(base) > 80 {
		base = base[len(base)-80:] // keep the tail (extension)
	}
	return hex.EncodeToString(h[:])[:16] + "-" + strconv.FormatInt(mtimeUnix, 10) + "-" + base
}

// baseName is filepath.Base tolerant of the PEER's separators (its OS may differ).
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// sanitize keeps [A-Za-z0-9._ -]; everything else becomes '_' (Windows-invalid chars included).
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), ". ")
}

func (c *Cache) entryPath(peer, path string, mtimeUnix int64) string {
	return filepath.Join(c.root, peerKey(peer), entryName(path, mtimeUnix))
}

// Lookup returns the cached local copy of (peer, path, mtime) if present, touching its mtime
// so LRU eviction sees the hit.
func (c *Cache) Lookup(peer, path string, mtimeUnix int64) (string, bool) {
	p := c.entryPath(peer, path, mtimeUnix)
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return "", false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now) // best-effort LRU touch
	return p, true
}

// PartWriter streams one pull into a .part temp; Commit renames it into place.
type PartWriter struct {
	c           *Cache
	f           *os.File
	part, final string
}

// Writer opens a .part temp for (peer, path, mtime). Abort or Commit it.
func (c *Cache) Writer(peer, path string, mtimeUnix int64) (*PartWriter, error) {
	final := c.entryPath(peer, path, mtimeUnix)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(final), filepath.Base(final)+".*.part")
	if err != nil {
		return nil, err
	}
	return &PartWriter{c: c, f: f, part: f.Name(), final: final}, nil
}

func (w *PartWriter) Write(p []byte) (int, error) { return w.f.Write(p) }

// Commit renames the temp into place, drops superseded mtime versions of the same source, and
// evicts LRU entries over the byte cap (never the just-committed file). Returns the final path.
func (w *PartWriter) Commit() (string, error) {
	if err := w.f.Close(); err != nil {
		_ = os.Remove(w.part)
		return "", err
	}
	_ = os.Remove(w.final) // Windows: rename-over-existing fails
	if err := os.Rename(w.part, w.final); err != nil {
		_ = os.Remove(w.part)
		return "", err
	}
	w.c.dropStale(w.final)
	w.c.evict(w.final)
	return w.final, nil
}

// Abort discards the temp.
func (w *PartWriter) Abort() {
	_ = w.f.Close()
	_ = os.Remove(w.part)
}

// dropStale removes older-mtime versions of final's source (same 16-hex path-hash prefix).
func (c *Cache) dropStale(final string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dir, name := filepath.Dir(final), filepath.Base(final)
	i := strings.IndexByte(name, '-')
	if i != 16 {
		return
	}
	prefix := name[:i+1]
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if n := e.Name(); n != name && strings.HasPrefix(n, prefix) && !strings.HasSuffix(n, ".part") {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// evict enforces the byte cap: sweeps orphaned .part temps (older than partMaxAge), then removes
// the least-recently-used entries (mtime asc) until total <= cap. keep is never removed.
func (c *Cache) evict(keep string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	type ent struct {
		path string
		size int64
		mod  time.Time
	}
	var all []ent
	var total int64
	peers, err := os.ReadDir(c.root)
	if err != nil {
		return
	}
	for _, pd := range peers {
		if !pd.IsDir() {
			continue
		}
		dir := filepath.Join(c.root, pd.Name())
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			fi, err := e.Info()
			if err != nil || fi.IsDir() {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if strings.HasSuffix(e.Name(), ".part") {
				if time.Since(fi.ModTime()) > partMaxAge {
					_ = os.Remove(p) // crashed-pull orphan
				}
				continue // live temps don't count toward the cap
			}
			all = append(all, ent{p, fi.Size(), fi.ModTime()})
			total += fi.Size()
		}
	}
	if total <= c.cap {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.Before(all[j].mod) })
	for _, e := range all {
		if total <= c.cap {
			break
		}
		if e.path == keep {
			continue
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}

// Purge removes every cached entry (explicit user action; the root dir stays).
func (c *Cache) Purge() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ents, err := os.ReadDir(c.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var firstErr error
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(c.root, e.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
