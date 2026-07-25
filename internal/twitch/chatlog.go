package twitch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
)

// Chat-log bounds (state cap + drop policy): per-day file ≤ chatlogMaxDayBytes - excess
// events that day are DROPPED (drop-newest, logged once); whole dir ≤ chatlogMaxDays files
// AND ≤ chatlogMaxTotalBytes - OLDEST day files pruned on Open + at each day rotation.
// Package vars so tests can shrink them.
var (
	chatlogMaxDayBytes   int64 = 10 << 20 // 10 MB per day file
	chatlogMaxTotalBytes int64 = 50 << 20 // 50 MB across the dir
	chatlogMaxDays             = 14       // days kept
)

const chatlogExt = ".jsonl"

// ChatLog persists chat + alert events as append-only JSONL, one file per local day
// (<dir>/YYYY-MM-DD.jsonl), so a set streamed with the tab closed is readable after the
// fact and history survives restarts. Single writer: the daemon-side Twitch proxy
// (low-throughput file appends - in-proc per the DB-bound carve-out).
type ChatLog struct {
	log *logbus.Bus
	dir string

	mu      sync.Mutex
	day     string // date key of the open file ("2006-01-02")
	f       *os.File
	size    int64
	dropped bool // day cap hit - logged once per day
}

// OpenChatLog opens (creating the dir) + prunes the store.
func OpenChatLog(dir string, log *logbus.Bus) (*ChatLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &ChatLog{log: log, dir: dir}
	l.mu.Lock()
	l.pruneLocked()
	l.mu.Unlock()
	return l, nil
}

// Append writes one event to today's file (rotating + pruning on day change). Drops the
// event when today's file is at cap.
func (l *ChatLog) Append(ev Event) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	day := time.Now().Format("2006-01-02")
	if l.f == nil || day != l.day {
		l.rotateLocked(day)
		if l.f == nil {
			return
		}
	}
	if l.size+int64(len(raw))+1 > chatlogMaxDayBytes {
		if !l.dropped {
			l.dropped = true
			l.warn("chat log day cap reached - dropping the rest of today", map[string]any{"day": day})
		}
		return
	}
	n, werr := l.f.Write(append(raw, '\n'))
	if werr != nil {
		l.warn("chat log append failed", map[string]any{"error": werr.Error()})
		return
	}
	l.size += int64(n)
}

// Recent returns the newest n events, oldest→newest (seed order for the feed).
func (l *ChatLog) Recent(n int) []Event {
	if n <= 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	names := l.filesLocked() // ascending by date
	var picked [][]Event
	total := 0
	for i := len(names) - 1; i >= 0 && total < n; i-- { // newest file backwards
		evs := readChatFile(filepath.Join(l.dir, names[i]))
		if len(evs) == 0 {
			continue
		}
		picked = append(picked, evs)
		total += len(evs)
	}
	var out []Event
	for i := len(picked) - 1; i >= 0; i-- { // back to chronological
		out = append(out, picked[i]...)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// Close releases the open day file (appends after Close reopen it).
func (l *ChatLog) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
		l.day = ""
	}
}

// rotateLocked closes the previous day file, prunes, and opens day's file for append.
func (l *ChatLog) rotateLocked(day string) {
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
	l.day, l.size, l.dropped = day, 0, false
	l.pruneLocked()
	p := filepath.Join(l.dir, day+chatlogExt)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.warn("chat log open failed", map[string]any{"error": err.Error()})
		return
	}
	if fi, serr := f.Stat(); serr == nil {
		l.size = fi.Size()
	}
	l.f = f
}

// pruneLocked enforces the age cap then the total-size cap (oldest-first; never the
// current day's file).
func (l *ChatLog) pruneLocked() {
	names := l.filesLocked()
	cutoff := time.Now().AddDate(0, 0, -chatlogMaxDays).Format("2006-01-02")
	var kept []string
	var total int64
	sizes := map[string]int64{}
	for _, name := range names {
		day := name[:len(name)-len(chatlogExt)]
		p := filepath.Join(l.dir, name)
		if day < cutoff && day != l.day {
			_ = os.Remove(p)
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		kept = append(kept, name)
		sizes[name] = fi.Size()
		total += fi.Size()
	}
	for _, name := range kept { // ascending = oldest first
		if total <= chatlogMaxTotalBytes {
			break
		}
		if name[:len(name)-len(chatlogExt)] == l.day {
			continue
		}
		if os.Remove(filepath.Join(l.dir, name)) == nil {
			total -= sizes[name]
		}
	}
}

// filesLocked lists the store's day files, ascending by date (names sort lexically).
func (l *ChatLog) filesLocked() []string {
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && filepath.Ext(e.Name()) == chatlogExt {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func (l *ChatLog) warn(msg string, fields map[string]any) {
	if l.log != nil {
		l.log.Warn(source, msg, fields)
	}
}

// readChatFile parses one JSONL day file (bad lines skipped).
func readChatFile(path string) []Event {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Event
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev Event
		if json.Unmarshal(line, &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}
