// Package vrcphotos organizes VRChat screenshots into per-instance folders: it maps each photo's
// capture time → the world/instance the user was in then (via the vrcloc timeline fed by the log
// parser) and copies/moves the photo into a folder named after that instance (group · world ·
// date) - or after a rave.page event when one is known. Pure helpers (time parse, destination
// planning) are unit-tested; the organizer + watcher do the file I/O.
package vrcphotos

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/vrcloc"
)

// organizedDir is the subfolder (under the screenshots root) that holds the organized copies, kept
// separate from VRChat's own YYYY-MM date folders so we never re-process our own output.
const organizedDir = "Organized"

// tsRe matches the YYYY-MM-DD_hh-mm-ss timestamp embedded in a VRChat screenshot filename
// (present in both the date-first and resolution-first historical orderings).
var tsRe = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})_(\d{2})-(\d{2})-(\d{2})`)

// PhotoTime extracts the capture time from a VRChat screenshot filename (local time); falls back to
// modTime when the name has no recognizable stamp.
func PhotoTime(name string, modTime time.Time) time.Time {
	if m := tsRe.FindStringSubmatch(name); m != nil {
		if t, err := time.ParseInLocation("2006-01-02 15-04-05",
			fmt.Sprintf("%s-%s-%s %s-%s-%s", m[1], m[2], m[3], m[4], m[5], m[6]), time.Local); err == nil {
			return t
		}
	}
	return modTime
}

// Event is a rave.page event window a photo can be filed under (Name = folder when matched).
type Event struct {
	ID    string
	Name  string
	Start time.Time
	End   time.Time
}

// EventSource answers which rave.page event (if any) was happening at time t - the primary organize
// key: a photo whose capture time falls in an event window is filed under that event. May be nil
// (then organizing falls back to the world/instance timeline).
type EventSource interface {
	// EventAt returns the event whose window contains t, ok=false if none.
	EventAt(t time.Time) (Event, bool)
}

// PlanFolder returns the destination folder name for a photo taken at photoTime, ok=false when it
// can't be placed (no event match and unknown location → leave it where it is). Event match is
// primary (works even with no timeline data); the world/instance timeline is the fallback.
func PlanFolder(photoTime time.Time, tl *vrcloc.Timeline, ev EventSource) (string, bool) {
	date := photoTime.Format("2006-01-02")
	if ev != nil {
		if e, ok := ev.EventAt(photoTime); ok && strings.TrimSpace(e.Name) != "" {
			return vrcloc.SanitizeName(e.Name+" ("+date+")", "VRChat ("+date+")"), true
		}
	}
	if tl != nil {
		if loc, ok := tl.At(photoTime); ok {
			return vrcloc.InstanceDirName(loc, date), true
		}
	}
	return "", false
}

// Mode selects whether organizing copies or moves the original.
type Mode int

const (
	Copy Mode = iota
	Move
)

// Organizer sorts new screenshots into Organized/<folder>/ under root.
type Organizer struct {
	root string
	tl   *vrcloc.Timeline
	mode Mode
	ev   EventSource
	log  func(string)

	mu   sync.Mutex
	seen map[string]bool // absolute source paths already handled
}

// New builds an organizer over the screenshots root dir. ev (optional) is the primary organize key
// (file by rave.page event); nil falls back to the world/instance timeline.
func New(root string, tl *vrcloc.Timeline, mode Mode, ev EventSource, log func(string)) *Organizer {
	return &Organizer{root: root, tl: tl, mode: mode, ev: ev, log: log, seen: map[string]bool{}}
}

// OrganizedRoot is where sorted photos go.
func (o *Organizer) OrganizedRoot() string { return filepath.Join(o.root, organizedDir) }

// Process organizes one screenshot file. Returns the destination path, or ok=false (no error) when
// the location is unknown / it's already organized. Idempotent per source path.
func (o *Organizer) Process(srcPath string) (string, bool, error) {
	abs, _ := filepath.Abs(srcPath)
	if strings.Contains(abs, string(os.PathSeparator)+organizedDir+string(os.PathSeparator)) {
		return "", false, nil // already under Organized/
	}
	o.mu.Lock()
	if o.seen[abs] {
		o.mu.Unlock()
		return "", false, nil
	}
	o.mu.Unlock()

	fi, err := os.Stat(srcPath)
	if err != nil || fi.IsDir() {
		return "", false, err
	}
	if !isImage(srcPath) {
		return "", false, nil
	}
	folder, ok := PlanFolder(PhotoTime(filepath.Base(srcPath), fi.ModTime()), o.tl, o.ev)
	if !ok {
		return "", false, nil // unknown location → leave it
	}
	destDir := filepath.Join(o.OrganizedRoot(), folder)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", false, err
	}
	dest := uniqueDest(filepath.Join(destDir, filepath.Base(srcPath)))
	if err := placeFile(srcPath, dest, o.mode); err != nil {
		return "", false, err
	}
	o.mu.Lock()
	o.seen[abs] = true
	o.mu.Unlock()
	if o.log != nil {
		o.log(fmt.Sprintf("%s → %s", filepath.Base(srcPath), folder))
	}
	return dest, true, nil
}

// Scan organizes every screenshot in root's date folders (YYYY-MM) + any at the root. Returns the
// count organized. Skips the Organized/ tree.
func (o *Organizer) Scan() int {
	n := 0
	entries, err := os.ReadDir(o.root)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() {
			if e.Name() == organizedDir {
				continue
			}
			sub := filepath.Join(o.root, e.Name())
			files, _ := os.ReadDir(sub)
			for _, f := range files {
				if !f.IsDir() {
					if _, ok, _ := o.Process(filepath.Join(sub, f.Name())); ok {
						n++
					}
				}
			}
			continue
		}
		if _, ok, _ := o.Process(filepath.Join(o.root, e.Name())); ok {
			n++
		}
	}
	return n
}

// Unorganized labels a photo with no known event/world (old/loose, no timeline match).
const Unorganized = "Unorganized"

// Photo is one VRChat screenshot found by ScanAll: its path, capture time, and a human label
// (event/world when known, else "Unorganized"). Organized=true when it lives under Organized/.
type Photo struct {
	File      string    `json:"file"`
	Name      string    `json:"name"`
	TakenAt   time.Time `json:"takenAt"`
	Folder    string    `json:"folder,omitempty"` // Organized/<folder> name; "" if loose
	Label     string    `json:"label"`
	Organized bool      `json:"organized"`
}

// ScanAll lists every screenshot under root (recursively - organized subfolders AND loose/old
// ones), deduped by base name (an organized copy preferred over its loose original so the entry
// knows its event/world), newest first. Un-organizable photos still appear so they stay viewable.
// Label = the Organized/ folder when organized, else the event (ev) or world (tl) at capture time,
// else "Unorganized". ev/tl may be nil (degrades to "Unorganized" for loose photos).
func ScanAll(root string, tl *vrcloc.Timeline, ev EventSource) []Photo {
	var out []Photo
	seen := map[string]int{} // lower base name → index in out
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isImage(p) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		name := d.Name()
		low := strings.ToLower(name)
		ph := Photo{
			File:    p,
			Name:    strings.TrimSuffix(name, filepath.Ext(name)),
			TakenAt: PhotoTime(name, info.ModTime()),
		}
		if folder, ok := organizedFolder(root, p); ok {
			ph.Organized, ph.Folder, ph.Label = true, folder, folder
		} else {
			ph.Label = labelAt(ph.TakenAt, tl, ev)
		}
		if idx, dup := seen[low]; dup {
			if !out[idx].Organized && ph.Organized {
				out[idx] = ph // keep the copy that knows its event/world
			}
			return nil
		}
		seen[low] = len(out)
		out = append(out, ph)
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].TakenAt.After(out[j].TakenAt) })
	return out
}

// organizedFolder returns the immediate folder name under root/Organized/ for p, ok=false unless p
// is an organized copy (Organized/<folder>/<file>).
func organizedFolder(root, p string) (string, bool) {
	rel, err := filepath.Rel(filepath.Join(root, organizedDir), p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) >= 2 && parts[0] != "" {
		return parts[0], true
	}
	return "", false
}

// labelAt resolves a display label for a loose photo: event name (primary) → world/group (timeline)
// → Unorganized.
func labelAt(t time.Time, tl *vrcloc.Timeline, ev EventSource) string {
	if ev != nil {
		if e, ok := ev.EventAt(t); ok && strings.TrimSpace(e.Name) != "" {
			return e.Name
		}
	}
	if tl != nil {
		if loc, ok := tl.At(t); ok {
			world := loc.WorldName
			if world == "" {
				world = "Unknown World"
			}
			if loc.IsGroup() && loc.GroupName != "" {
				return loc.GroupName + " · " + world
			}
			return world
		}
	}
	return Unorganized
}

func isImage(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

// uniqueDest avoids clobbering an existing destination by suffixing " (2)", " (3)", …
func uniqueDest(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}

// placeFile copies or moves src → dst. Move falls back to copy+remove across volumes.
func placeFile(src, dst string, mode Mode) error {
	if mode == Move {
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
		// cross-volume or locked → copy then remove
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if mode == Move {
		return os.Remove(src)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
