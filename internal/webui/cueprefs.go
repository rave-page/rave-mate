package webui

// Cue-editor modes + per-software defaults. A mode scopes the editor to one DJ software:
// new cues carry that software's tag (musiclib.CuePoint.Sw), pattern apply / promote /
// write-back account pads + collisions only within the scope, and each software keeps its
// own defaults (pad limit, overwrite-on-apply, auto-promote, drop split). Persisted as
// prefs.json beside the pattern store - not versioned config; losing it costs nothing but
// re-picking defaults.

import (
	"encoding/json"
	"os"
	"path/filepath"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
)

// ceSoftwares are the selectable modes (keys match cuewriteback target keys).
var ceSoftwares = [][2]string{
	{"traktor", "Traktor"},
	{"rekordbox", "Rekordbox"},
	{"serato", "Serato"},
	{"virtualdj", "VirtualDJ"},
}

func ceSoftwareLabel(key string) string {
	for _, s := range ceSoftwares {
		if s[0] == key {
			return s[1]
		}
	}
	return i18n.T("library.ce.modeAll")
}

// ceSWPref is one software's stored defaults. Zero value = the shipped defaults
// (8 pads, no overwrite, no auto-promote, split-even on).
type ceSWPref struct {
	MaxPads     int  `json:"maxPads,omitempty"`
	Overwrite   bool `json:"overwriteOnApply,omitempty"`
	AutoPromote bool `json:"autoPromoteOnWrite,omitempty"`
	NoSplitEven bool `json:"noSplitEven,omitempty"` // stored inverted: split-even is the default
}

// MaxPadsOr returns the effective pad budget.
func (p ceSWPref) MaxPadsOr() int {
	if p.MaxPads > 0 {
		return p.MaxPads
	}
	return 8
}

type cePrefsSt struct {
	Mode string              `json:"mode,omitempty"` // active scope ("" = all software)
	SW   map[string]ceSWPref `json:"sw,omitempty"`   // key "" = the all-software row
}

// cePrefsLoad lazily loads the prefs (missing/corrupt file = defaults).
func (u *UI) cePrefsLoad() {
	u.ceMu.Lock()
	defer u.ceMu.Unlock()
	if u.cePref != nil {
		return
	}
	p := &cePrefsSt{}
	if raw, err := os.ReadFile(cePrefsPath()); err == nil {
		_ = json.Unmarshal(raw, p) // corrupt file = defaults; next save rewrites it
	}
	if p.SW == nil {
		p.SW = map[string]ceSWPref{}
	}
	u.cePref = p
}

// cePrefs returns a snapshot. The SW map is DEEP-copied: batch jobs (u.bg goroutines)
// read prefs while the act-worker mutates them - the live map is only ever touched
// under ceMu (a shared alias would be a fatal concurrent map read/write).
func (u *UI) cePrefs() cePrefsSt {
	u.cePrefsLoad()
	u.ceMu.Lock()
	defer u.ceMu.Unlock()
	cp := *u.cePref
	sw := make(map[string]ceSWPref, len(cp.SW))
	for k, v := range cp.SW {
		sw[k] = v
	}
	cp.SW = sw
	return cp
}

// ceMode returns the active software scope.
func (u *UI) ceMode() string {
	u.cePrefsLoad()
	u.ceMu.Lock()
	defer u.ceMu.Unlock()
	return u.cePref.Mode
}

// cePrefFor returns the effective defaults for scope sw.
func (u *UI) cePrefFor(sw string) ceSWPref {
	u.cePrefsLoad()
	u.ceMu.Lock()
	defer u.ceMu.Unlock()
	return u.cePref.SW[sw]
}

// cePrefMutSW mutates the ACTIVE mode's per-software row + repaints the rail.
func (u *UI) cePrefMutSW(fn func(*ceSWPref)) {
	u.cePrefsMut(func(p *cePrefsSt) {
		row := p.SW[p.Mode]
		fn(&row)
		p.SW[p.Mode] = row
	})
	u.cePatchRail()
}

// cePrefsMut mutates + persists the prefs (atomic write; best-effort). The marshal
// runs outside the lock over a deep copy - never over the live map.
func (u *UI) cePrefsMut(fn func(*cePrefsSt)) {
	u.cePrefsLoad()
	u.ceMu.Lock()
	fn(u.cePref)
	cp := *u.cePref
	sw := make(map[string]ceSWPref, len(cp.SW))
	for k, v := range cp.SW {
		sw[k] = v
	}
	cp.SW = sw
	u.ceMu.Unlock()
	raw, err := json.MarshalIndent(cp, "", " ")
	if err != nil {
		return
	}
	path := cePrefsPath()
	if path == "" {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		u.logErr("cue prefs", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		u.logErr("cue prefs", err)
	}
}

func cePrefsPath() string {
	dir, err := config.DataPath("cuepatterns")
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "prefs.json")
}
