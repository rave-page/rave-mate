package nmlsrc

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

const (
	srcLog     = "nml"
	confidence = 0.85 // collection-accurate metadata
	reloadWait = 2 * time.Second
)

// Source enriches the live session with Traktor collection metadata. It watches the merged
// state for deck title/artist changes and fills in album/genre/key/bpm from the indexed
// collection.nml (reloaded on change).
type Source struct {
	log            *logbus.Bus
	merger         *session.Merger
	collectionPath string
	historyDir     string

	mu  sync.RWMutex
	idx map[string]Meta
}

// New constructs the source. Empty paths auto-detect the newest Traktor install.
func New(log *logbus.Bus, merger *session.Merger, collectionPath, historyDir string) *Source {
	return &Source{log: log, merger: merger, collectionPath: collectionPath, historyDir: historyDir, idx: map[string]Meta{}}
}

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceNML }

// Capabilities implements session.Source: collection-backed metadata for all decks.
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{{
		Scope:  session.ScopeDeck,
		IDs:    []string{"A", "B", "C", "D"},
		Fields: []string{session.FieldAlbum, session.FieldGenre, session.FieldKey, session.FieldBPM, session.FieldPath},
	}}
}

// Start loads + watches the collection and enriches deck metadata until ctx is cancelled.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	col, hist := s.resolvePaths()
	if col == "" {
		s.log.Warn(srcLog, "no Traktor collection found; metadata enrichment idle", nil)
	} else {
		s.collectionPath, s.historyDir = col, hist
		s.reload()
	}

	// Watch the collection file's directory for changes (debounced reload).
	debuglog.Go(s.log, srcLog, func() { s.watchCollection(ctx) })

	// React to deck title/artist changes by emitting enrichment.
	ch, unsub := s.merger.Subscribe()
	defer unsub()
	last := map[string]string{} // deck → last enriched key
	for {
		select {
		case <-ctx.Done():
			return nil
		case u, ok := <-ch:
			if !ok {
				return nil
			}
			if u.Scope.Kind != session.ScopeDeck {
				continue
			}
			s.enrich(u.Scope.ID, last, emit)
		}
	}
}

// enrich looks up the deck's current title/artist and emits collection metadata if found.
func (s *Source) enrich(deck string, last map[string]string, emit func(session.Observation)) {
	snap := s.merger.Snapshot()
	fields := snap.Decks[deck]
	title := session.StringField(fields, session.FieldTitle)
	artist := session.StringField(fields, session.FieldArtist)
	if title == "" || artist == "" {
		return
	}
	k := Key(title, artist)
	if last[deck] == k {
		return // already enriched this track
	}

	s.mu.RLock()
	m, ok := s.idx[k]
	s.mu.RUnlock()
	if !ok {
		last[deck] = k // remember miss too, so we don't re-look-up every update
		return
	}
	last[deck] = k

	out := map[string]any{}
	if m.Album != "" {
		out[session.FieldAlbum] = m.Album
	}
	if m.Genre != "" {
		out[session.FieldGenre] = m.Genre
	}
	if m.Key != "" {
		out[session.FieldKey] = m.Key
	}
	if m.BPM > 0 {
		out[session.FieldBPM] = m.BPM
	}
	if m.Path != "" {
		out[session.FieldPath] = m.Path
	}
	if len(out) == 0 {
		return
	}
	emit(session.Observation{
		Source:     session.SourceNML,
		Scope:      session.Scope{Kind: session.ScopeDeck, ID: deck},
		Fields:     out,
		Confidence: confidence,
	})
}

// reload rebuilds the metadata index from collection.nml + the newest history file.
func (s *Source) reload() {
	idx, err := ParseCollection(s.collectionPath)
	if err != nil {
		s.log.Warn(srcLog, "parse collection failed", map[string]any{"path": s.collectionPath, "error": err.Error()})
		return
	}
	// Merge in the newest history file (covers tracks not yet in the collection), without
	// overriding collection entries.
	if hp := s.newestHistory(); hp != "" {
		if hidx, herr := ParseCollection(hp); herr == nil {
			for k, v := range hidx {
				if _, exists := idx[k]; !exists {
					idx[k] = v
				}
			}
		}
	}
	s.mu.Lock()
	s.idx = idx
	s.mu.Unlock()
	s.log.Info(srcLog, "collection indexed", map[string]any{"tracks": len(idx), "path": s.collectionPath})
}

// watchCollection reloads the index when collection.nml changes (debounced).
func (s *Source) watchCollection(ctx context.Context) {
	if s.collectionPath == "" {
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		s.log.Warn(srcLog, "collection watcher unavailable", map[string]any{"error": err.Error()})
		return
	}
	defer func() { _ = w.Close() }()
	if err := w.Add(filepath.Dir(s.collectionPath)); err != nil {
		s.log.Warn(srcLog, "watch collection dir failed", map[string]any{"error": err.Error()})
		return
	}
	var timer *time.Timer
	base := filepath.Base(s.collectionPath)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != base || !ev.Op.Has(fsnotify.Write) {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(reloadWait, s.reload)
		case <-w.Errors:
		}
	}
}

// resolvePaths returns the (collection.nml, historyDir) to use, auto-detecting if unset.
func (s *Source) resolvePaths() (string, string) {
	col, hist := s.collectionPath, s.historyDir
	if col != "" {
		if hist == "" {
			hist = filepath.Join(filepath.Dir(col), "History")
		}
		return col, hist
	}
	dir := findTraktorDir()
	if dir == "" {
		return "", ""
	}
	return filepath.Join(dir, "collection.nml"), filepath.Join(dir, "History")
}

// newestHistory returns the most recently modified .nml in the history dir (or "").
func (s *Source) newestHistory() string {
	if s.historyDir == "" {
		return ""
	}
	ents, err := os.ReadDir(s.historyDir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".nml") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestMod) {
			newestMod = info.ModTime()
			newest = filepath.Join(s.historyDir, e.Name())
		}
	}
	return newest
}

// findTraktorDir returns the newest "Traktor *" install dir under Documents/Native
// Instruments (Windows + macOS/Linux Documents layout), or "".
func findTraktorDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	base := filepath.Join(home, "Documents", "Native Instruments")
	ents, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	var dirs []string
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), "Traktor") {
			dirs = append(dirs, filepath.Join(base, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return ""
	}
	// Newest version directory wins (lexical sort approximates version order; "Traktor
	// 4.1.0" > "Traktor 3.11.1" holds for same-major, good enough as a default).
	sort.Strings(dirs)
	return dirs[len(dirs)-1]
}
