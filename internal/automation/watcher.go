package automation

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceDelay waits for writes to settle before dispatching (file may still be copying).
const debounceDelay = 2 * time.Second

// partialSuffixes mark in-progress writes; skip until the final rename lands.
var partialSuffixes = []string{".tmp", ".part", ".crdownload", ".download", ".filepart"}

// Watcher fans fsnotify Create/Write events for watched dirs out to the automations
// watching each dir, debounced per-path. Concurrency-safe.
type Watcher struct {
	log    Logger
	onFile func(automationID, path string)
	fsw    *fsnotify.Watcher

	mu      sync.Mutex
	dirs    map[string][]string    // watched dir → automation IDs
	timers  map[string]*time.Timer // pending per-path debounce
	stopped bool

	done chan struct{}
}

// NewWatcher creates the fsnotify watcher + starts the event loop. Caller must Stop.
func NewWatcher(log Logger, onFile func(automationID, path string)) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		log:    log,
		onFile: onFile,
		fsw:    fsw,
		dirs:   map[string][]string{},
		timers: map[string]*time.Timer{},
		done:   make(chan struct{}),
	}
	go w.loop()
	return w, nil
}

// Set reconciles the watched dir set to the enabled automations.
func (w *Watcher) Set(automations []Automation) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}

	// build desired dir → IDs from enabled automations with a WatchDir
	desired := map[string][]string{}
	for _, a := range automations {
		if !a.Enabled || a.WatchDir == "" {
			continue
		}
		dir := filepath.Clean(a.WatchDir)
		desired[dir] = append(desired[dir], a.ID)
	}

	// remove dirs no longer wanted
	for dir := range w.dirs {
		if _, ok := desired[dir]; !ok {
			if err := w.fsw.Remove(dir); err != nil {
				w.log.Warn(source, "watch remove failed", map[string]any{"dir": dir, "error": err.Error()})
			} else {
				w.log.Info(source, "watch removed", map[string]any{"dir": dir})
			}
		}
	}

	// add newly wanted dirs
	for dir, ids := range desired {
		if _, ok := w.dirs[dir]; !ok {
			if err := w.fsw.Add(dir); err != nil {
				w.log.Warn(source, "watch add failed", map[string]any{"dir": dir, "error": err.Error()})
				delete(desired, dir) // not actually watched; don't track it
				continue
			}
			w.log.Info(source, "watch added", map[string]any{"dir": dir, "automations": len(ids)})
		}
	}

	w.dirs = desired
}

// Stop closes the fsnotify watcher, stops timers, ends the loop. Idempotent.
func (w *Watcher) Stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	for p, t := range w.timers {
		t.Stop()
		delete(w.timers, p)
	}
	err := w.fsw.Close()
	w.mu.Unlock()

	if err != nil {
		w.log.Warn(source, "watcher close failed", map[string]any{"error": err.Error()})
	}
	<-w.done
}

// loop drains fsnotify channels until the watcher is closed. recover-guarded.
func (w *Watcher) loop() {
	defer close(w.done)
	defer func() {
		if r := recover(); r != nil {
			w.log.Warn(source, "watcher loop panic", map[string]any{"panic": r})
		}
	}()
	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.onEvent(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			if err != nil {
				w.log.Warn(source, "watcher error", map[string]any{"error": err.Error()})
			}
		}
	}
}

// onEvent debounces Create/Write events for plausible files.
func (w *Watcher) onEvent(ev fsnotify.Event) {
	if !ev.Op.Has(fsnotify.Create) && !ev.Op.Has(fsnotify.Write) {
		return
	}
	if isPartial(ev.Name) {
		return
	}
	// quick non-file reject (event may race deletion; the debounce re-stats)
	if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	path := ev.Name
	if t := w.timers[path]; t != nil {
		t.Reset(debounceDelay)
		return
	}
	w.timers[path] = time.AfterFunc(debounceDelay, func() { w.dispatch(path) })
}

// dispatch fires after a path settles: stat, then fan out to watching automations.
func (w *Watcher) dispatch(path string) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Warn(source, "dispatch panic", map[string]any{"path": path, "panic": r})
		}
	}()

	w.mu.Lock()
	delete(w.timers, path)
	stopped := w.stopped
	ids := append([]string(nil), w.dirs[filepath.Dir(path)]...)
	w.mu.Unlock()

	if stopped || len(ids) == 0 {
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() { // vanished or a directory
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	for _, id := range ids {
		w.log.Info(source, "file dispatched", map[string]any{"automation": id, "path": abs})
		w.onFile(id, abs)
	}
}

// isPartial reports whether name is a dotfile or in-progress-write temp file.
func isPartial(name string) bool {
	base := filepath.Base(name)
	if strings.HasPrefix(base, ".") {
		return true
	}
	lower := strings.ToLower(base)
	for _, s := range partialSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}
