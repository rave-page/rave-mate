// Package vrcloc tracks the VRChat location the user is in over time - a timeline of (joinedAt,
// world, instance, group) entries - so other features can answer "what world/instance was active
// at time T?" (organizing screenshots by when they were taken) and "what world am I in now?"
// (camera-path menu). Source-agnostic: a feeder (log parser / pipeline / API) calls Record; this
// package only stores, queries, and persists. Pure Go - unit-testable without VRChat.
package vrcloc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxEntries caps the persisted timeline (a long session changes instance rarely; this is plenty).
const maxEntries = 5000

// Location is one instance the user joined, stamped with when they joined it.
type Location struct {
	JoinedAt   time.Time `json:"joinedAt"`
	WorldID    string    `json:"worldId"`
	WorldName  string    `json:"worldName"`
	InstanceID string    `json:"instanceId,omitempty"` // full instance id (incl. region/nonce/group tags)
	GroupID    string    `json:"groupId,omitempty"`    // "" if not a group instance
	GroupName  string    `json:"groupName,omitempty"`
}

// IsGroup reports whether this was a group instance.
func (l Location) IsGroup() bool { return l.GroupID != "" }

// Timeline is an append-only, time-ordered history of joined locations, persisted as JSON.
type Timeline struct {
	mu      sync.Mutex
	path    string
	entries []Location // sorted ascending by JoinedAt
}

// NewTimeline loads (or starts) a timeline persisted at path. A load error yields an empty timeline.
func NewTimeline(path string) *Timeline {
	t := &Timeline{path: path}
	t.load()
	return t
}

// Record appends loc if it differs from the most recent entry (same instance → no-op, so repeated
// location events don't bloat the timeline). Persists on change. JoinedAt is kept as given.
func (t *Timeline) Record(loc Location) {
	if loc.WorldID == "" && loc.InstanceID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.entries); n > 0 {
		last := t.entries[n-1]
		if last.InstanceID == loc.InstanceID && last.WorldID == loc.WorldID {
			return // same instance, ignore
		}
	}
	if loc.JoinedAt.IsZero() {
		return // caller must stamp the join time (kept testable: no time.Now here)
	}
	t.entries = append(t.entries, loc)
	sort.SliceStable(t.entries, func(i, j int) bool { return t.entries[i].JoinedAt.Before(t.entries[j].JoinedAt) })
	if len(t.entries) > maxEntries {
		t.entries = t.entries[len(t.entries)-maxEntries:]
	}
	t.save()
}

// At returns the location active at time `when` - the latest entry whose JoinedAt <= when. ok is
// false if `when` precedes the first recorded join (unknown location).
func (t *Timeline) At(when time.Time) (Location, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// rightmost entry with JoinedAt <= when
	i := sort.Search(len(t.entries), func(i int) bool { return t.entries[i].JoinedAt.After(when) })
	if i == 0 {
		return Location{}, false
	}
	return t.entries[i-1], true
}

// Current returns the most recent location, ok=false if none recorded.
func (t *Timeline) Current() (Location, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.entries) == 0 {
		return Location{}, false
	}
	return t.entries[len(t.entries)-1], true
}

// Entries returns a copy of the timeline (most-recent first) for UIs.
func (t *Timeline) Entries() []Location {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Location, len(t.entries))
	for i, e := range t.entries {
		out[len(t.entries)-1-i] = e
	}
	return out
}

func (t *Timeline) load() {
	data, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &t.entries)
}

// save writes the timeline atomically (tmp + rename). Caller holds the lock.
func (t *Timeline) save() {
	if t.path == "" {
		return
	}
	data, err := json.MarshalIndent(t.entries, "", "  ")
	if err != nil {
		return
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, t.path)
}

// reservedNames are Windows device names that can't be a file/dir base name.
var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SanitizeName makes s safe as a single Windows/macOS/Linux folder or file name: illegal chars →
// '_', control chars stripped, trailing dots/spaces removed, reserved device names suffixed, and a
// length cap. Empty/all-illegal input → fallback.
func SanitizeName(s, fallback string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20: // control chars
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(b.String(), " .")
	out = strings.TrimSpace(out)
	if out == "" {
		return fallback
	}
	if reservedNames[strings.ToLower(out)] {
		out += "_"
	}
	if len(out) > 120 { // leave headroom under the 255 path-component limit
		out = strings.TrimRight(out[:120], " .")
	}
	return out
}

// InstanceDirName builds the organized-folder name for a location + date (YYYY-MM-DD). Group
// instances → "Group · World (date)"; public → "World (date)". The caller substitutes an event
// name when rave.page knows the user attended one. All parts are sanitized.
func InstanceDirName(loc Location, date string) string {
	world := loc.WorldName
	if world == "" {
		world = "Unknown World"
	}
	var name string
	if loc.IsGroup() && loc.GroupName != "" {
		name = loc.GroupName + " - " + world + " (" + date + ")"
	} else {
		name = world + " (" + date + ")"
	}
	return SanitizeName(name, "VRChat ("+date+")")
}

// DefaultTimelinePath is the conventional store path under dir.
func DefaultTimelinePath(dir string) string { return filepath.Join(dir, "vrc_location_timeline.json") }
