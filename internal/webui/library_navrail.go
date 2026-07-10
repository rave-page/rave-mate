package webui

// Left nav rail (DJ-software layout): Collection = All tracks + playlist tree;
// Browse = places / pinned / drives / current-dir folders. Rows dispatch the same
// acts as their toolbar counterparts (lib-plgoto / lib-nav), so behavior is shared.

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
)

const libNavMaxRows = 80 // per group; megadirs / huge playlist sets stay scannable

func (u *UI) libNavRailHTML(s *libSt, sec string) string {
	var b strings.Builder
	b.WriteString(`<div class=libnav>`)
	if sec == "collection" {
		u.libNavCollection(&b, s)
	} else {
		u.libNavBrowse(&b, s)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func navHd(label string) string {
	return `<div class=libnav-hd>` + html.EscapeString(label) + `</div>`
}

func navIt(act, icon, label, count string, on bool) string {
	cls := "libnav-it"
	if on {
		cls += " on"
	}
	n := ""
	if count != "" {
		n = `<span class=libnav-n>` + html.EscapeString(count) + `</span>`
	}
	return `<div class="` + cls + `" data-act="` + html.EscapeString(act) + `"><span class=libnav-ic>` + icon +
		`</span><span class=libnav-t>` + html.EscapeString(label) + `</span>` + n + `</div>`
}

func (u *UI) libNavCollection(b *strings.Builder, s *libSt) {
	cur := int64(0)
	if len(s.collPl) == 1 {
		for id := range s.collPl {
			cur = id
		}
	}
	b.WriteString(navHd(i18n.T("library.section.collection")))
	b.WriteString(navIt("lib-plclear", "🎧", i18n.T("library.nav.allTracks"), fmt.Sprint(len(s.tracks)), len(s.collPl) == 0))
	if u.svc.Lib == nil {
		return
	}
	rows, _ := u.svc.Lib.ListPlaylists()
	if len(rows) == 0 {
		return
	}
	b.WriteString(navHd(i18n.T("library.section.playlists")))
	for i, p := range rows {
		if i >= libNavMaxRows {
			b.WriteString(`<div class=libnav-hd>…</div>`)
			break
		}
		ic, n := "🎵", fmt.Sprint(p.TrackCount)
		switch p.Kind {
		case libdb.PlaylistSmart:
			ic, n = "⚡", "" // live rule eval per render would be too hot for 100+ playlists
		case libdb.PlaylistImported:
			ic = "⤓"
		}
		b.WriteString(navIt(fmt.Sprintf("lib-plgoto:%d", p.ID), ic, p.Name, n, p.ID == cur))
	}
}

func (u *UI) libNavBrowse(b *strings.Builder, s *libSt) {
	dir := u.libDirOr()
	home, _ := os.UserHomeDir()
	b.WriteString(navHd(i18n.T("library.nav.places")))
	for _, q := range [][2]string{{"home", ""}, {"desktop", "Desktop"}, {"downloads", "Downloads"}, {"music", "Music"}, {"videos", "Videos"}, {"pictures", "Pictures"}} {
		p := home
		if q[1] != "" {
			p = filepath.Join(home, q[1])
		}
		b.WriteString(navIt("lib-nav:"+p, "⌂", i18n.T("library.browse."+q[0]), "", p == dir))
	}
	if marks := u.libMarks(s).List(); len(marks) > 0 {
		b.WriteString(navHd(i18n.T("library.nav.pinned")))
		for _, bm := range marks {
			b.WriteString(navIt("lib-nav:"+bm.Path, "★", bm.Label, "", bm.Path == dir))
		}
	}
	if drives := libDrives(); len(drives) > 1 {
		b.WriteString(navHd(i18n.T("library.nav.drives")))
		for _, d := range drives {
			b.WriteString(navIt("lib-nav:"+d, "💾", d, "", strings.EqualFold(filepath.VolumeName(dir)+`\`, d)))
		}
	}
	fes, errRead, ok := u.libBrowseEntries(s, dir)
	if !ok || errRead {
		return
	}
	b.WriteString(navHd(i18n.T("library.nav.folders")))
	if parent := filepath.Dir(dir); parent != dir {
		b.WriteString(navIt("lib-nav:"+parent, "↰", "..", "", false))
	}
	n := 0
	for _, e := range fes {
		if !e.isDir {
			continue
		}
		if n++; n > libNavMaxRows {
			b.WriteString(`<div class=libnav-hd>…</div>`)
			break
		}
		b.WriteString(navIt("lib-nav:"+e.path, "📁", e.name, "", false))
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
