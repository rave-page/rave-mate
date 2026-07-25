package webui

// Left nav rail (DJ-software layout): Collection = All tracks + playlist tree;
// Browse = places / pinned / drives / current-dir folders. Rows dispatch the same
// acts as their toolbar counterparts (lib-plgoto / lib-nav), so behavior is shared.
//
// Impure half only: the pure renderer + its markup helpers live in
// render_library_fixers.go (Zig twin: native/zigui/src/libfixers.zig).

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
)

const libNavMaxRows = 80 // per group; megadirs / huge playlist sets stay scannable

// libNavRailState resolves the nav rail's rows (DB / fs / i18n). Caller holds s.mu.
func (u *UI) libNavRailState(s *libSt, sec string) libNavSt {
	st := libNavSt{Rows: []libNavRowSt{}}
	if sec == "collection" {
		u.libNavCollection(&st, s)
	} else {
		u.libNavBrowse(&st, s)
	}
	return st
}

func navHdRow(label string) libNavRowSt { return libNavRowSt{Hd: true, Label: label} }

func navItRow(act, icon, label, count string, on bool) libNavRowSt {
	return libNavRowSt{Act: act, Icon: icon, Label: label, Count: count, On: on}
}

func (st *libNavSt) add(r libNavRowSt) { st.Rows = append(st.Rows, r) }

func (u *UI) libNavCollection(st *libNavSt, s *libSt) {
	cur := int64(0)
	if len(s.collPl) == 1 {
		for id := range s.collPl {
			cur = id
		}
	}
	st.add(navHdRow(i18n.T("library.section.collection")))
	st.add(navItRow("lib-plclear", "🎧", i18n.T("library.nav.allTracks"), fmt.Sprint(len(s.tracks)), len(s.collPl) == 0))
	if u.svc.Lib == nil {
		return
	}
	rows := u.libPlaylists(s)
	if len(rows) == 0 {
		return
	}
	st.add(navHdRow(i18n.T("library.section.playlists")))
	for i, p := range rows {
		if i >= libNavMaxRows {
			st.add(navHdRow("…"))
			break
		}
		ic, n := "🎵", fmt.Sprint(p.TrackCount)
		switch p.Kind {
		case libdb.PlaylistSmart:
			ic, n = "⚡", "" // live rule eval per render would be too hot for 100+ playlists
		case libdb.PlaylistImported:
			ic = "⤓"
		}
		st.add(navItRow(fmt.Sprintf("lib-plgoto:%d", p.ID), ic, p.Name, n, p.ID == cur))
	}
}

func (u *UI) libNavBrowse(st *libNavSt, s *libSt) {
	dir := u.libDirOr()
	home, _ := os.UserHomeDir()
	st.add(navHdRow(i18n.T("library.nav.places")))
	for _, q := range [][2]string{{"home", ""}, {"desktop", "Desktop"}, {"downloads", "Downloads"}, {"music", "Music"}, {"videos", "Videos"}, {"pictures", "Pictures"}} {
		p := home
		if q[1] != "" {
			p = filepath.Join(home, q[1])
		}
		st.add(navItRow("lib-nav:"+p, "⌂", i18n.T("library.browse."+q[0]), "", p == dir))
	}
	if marks := u.libMarks(s).List(); len(marks) > 0 {
		st.add(navHdRow(i18n.T("library.nav.pinned")))
		for _, bm := range marks {
			st.add(navItRow("lib-nav:"+bm.Path, "★", bm.Label, "", bm.Path == dir))
		}
	}
	if drives := libDrives(); len(drives) > 1 {
		st.add(navHdRow(i18n.T("library.nav.drives")))
		for _, d := range drives {
			st.add(navItRow("lib-nav:"+d, "💾", d, "", strings.EqualFold(filepath.VolumeName(dir)+`\`, d)))
		}
	}
	fes, errRead, ok := u.libBrowseEntries(s, dir)
	if !ok || errRead {
		return
	}
	st.add(navHdRow(i18n.T("library.nav.folders")))
	if parent := filepath.Dir(dir); parent != dir {
		st.add(navItRow("lib-nav:"+parent, "↰", "..", "", false))
	}
	n := 0
	for _, e := range fes {
		if !e.isDir {
			continue
		}
		if n++; n > libNavMaxRows {
			st.add(navHdRow("…"))
			break
		}
		st.add(navItRow("lib-nav:"+e.path, "📁", e.name, "", false))
	}
}

var (
	drivesOnce  sync.Once
	drivesCache []string
)

// libDrives lists mounted drive roots (Windows A:-Z:, once per process).
func libDrives() []string {
	drivesOnce.Do(func() {
		if runtime.GOOS != "windows" {
			drivesCache = []string{"/"}
			return
		}
		for c := 'C'; c <= 'Z'; c++ {
			p := string(c) + `:\`
			if _, err := os.Stat(p); err == nil {
				drivesCache = append(drivesCache, p)
			}
		}
	})
	return drivesCache
}

func init() {
	onExact("lib-plclear", func(u *UI, m actMsg) {
		u.libSetQuiet(func(s *libSt) { s.collPl, s.collPlSet, s.collPlNames = map[int64]bool{}, nil, nil })
		u.libPatchBody()
	})
}
