// Package vrclog parses the VRChat output log to learn the user's current world/instance over
// time and feeds it into a vrcloc.Timeline. The log is the authoritative, timestamped source for
// the local user's own location (the API pipeline is coarse + needs auth). Pure parsing is
// unit-tested; the watcher tails the live log file.
package vrclog

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/vrcloc"
)

// Log timestamps are "YYYY.MM.DD HH:MM:SS" in local time.
const tsLayout = "2006.01.02 15:04:05"

var (
	enteringRe = regexp.MustCompile(`\[Behaviour\] Entering Room: (.+?)\s*$`)
	joiningRe  = regexp.MustCompile(`\[Behaviour\] Joining (wrld_[0-9a-fA-F-]+):(\S+)`)
	groupRe    = regexp.MustCompile(`~group\((grp_[0-9a-fA-F-]+)\)`)
)

// Parser converts log lines into vrcloc.Location entries. Stateful: an "Entering Room: <name>"
// line sets the pending world name; the next "Joining wrld_..." line completes + emits the join.
type Parser struct{ pendingName string }

// Feed processes one log line. Returns a Location + true when a join completes. Lines that are
// "Joining or Creating Room:" / "Joining friend:" are NOT location joins and are ignored by the
// regexes (they don't contain "Joining wrld_").
func (p *Parser) Feed(line string) (vrcloc.Location, bool) {
	if m := enteringRe.FindStringSubmatch(line); m != nil {
		p.pendingName = strings.TrimSpace(m[1])
		return vrcloc.Location{}, false
	}
	m := joiningRe.FindStringSubmatch(line)
	if m == nil {
		return vrcloc.Location{}, false
	}
	loc := vrcloc.Location{
		JoinedAt:   parseTS(line),
		WorldID:    m[1],
		WorldName:  p.pendingName,
		InstanceID: m[2],
	}
	if g := groupRe.FindStringSubmatch(m[2]); g != nil {
		loc.GroupID = g[1]
	}
	p.pendingName = ""
	return loc, true
}

// parseTS reads the leading "YYYY.MM.DD HH:MM:SS" timestamp (local time). Zero time if absent.
func parseTS(line string) time.Time {
	if len(line) < 19 {
		return time.Time{}
	}
	t, err := time.ParseInLocation(tsLayout, line[:19], time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// DefaultLogDir is VRChat's log directory on Windows.
func DefaultLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "AppData", "LocalLow", "VRChat", "VRChat")
}

// LatestLog returns the newest output_log_*.txt in dir, or "" if none.
func LatestLog(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestMod time.Time
	for _, e := range ents {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "output_log_") || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || fi.ModTime().After(bestMod) {
			best, bestMod = filepath.Join(dir, e.Name()), fi.ModTime()
		}
	}
	return best
}

// Tailer follows VRChat's live log, feeding new Location joins to a sink. Poll-based (no fsnotify
// dep): on each Poll it re-resolves the newest log (a new VRChat session rotates the file), reads
// any bytes appended since last time, and emits completed joins. Back-fills the whole current log
// on the first Poll.
type Tailer struct {
	dir    string
	sink   func(vrcloc.Location)
	parser Parser
	path   string // current log being followed
	offset int64  // bytes consumed in path
}

// NewTailer follows logs in dir (DefaultLogDir() if empty), emitting each join to sink.
func NewTailer(dir string, sink func(vrcloc.Location)) *Tailer {
	if dir == "" {
		dir = DefaultLogDir()
	}
	return &Tailer{dir: dir, sink: sink}
}

// Poll consumes newly-appended log lines (switching to a newer log file if VRChat rotated it) and
// emits any completed joins. Call on a timer (e.g. every 2s).
func (t *Tailer) Poll() {
	latest := LatestLog(t.dir)
	if latest == "" {
		return
	}
	if latest != t.path { // new session → follow the new file from its start
		t.path, t.offset, t.parser = latest, 0, Parser{}
	}
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var consumed int64
	for sc.Scan() {
		line := sc.Text()
		consumed += int64(len(line)) + 1 // +1 ≈ newline
		if loc, ok := t.parser.Feed(line); ok {
			t.sink(loc)
		}
	}
	t.offset += consumed
}

// ScanFile parses an entire log file into ordered Locations (for back-filling the timeline on
// startup). Best-effort: unreadable file → nil.
func ScanFile(path string) []vrcloc.Location {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var p Parser
	var out []vrcloc.Location
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if loc, ok := p.Feed(sc.Text()); ok {
			out = append(out, loc)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].JoinedAt.Before(out[j].JoinedAt) })
	return out
}
