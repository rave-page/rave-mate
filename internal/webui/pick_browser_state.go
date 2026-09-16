package webui

// Resolved state for the in-app picker modal. Go resolves everything (i18n, rows, controls,
// thumbnails) into pkBrowseSt; the pure renderer (pick_browser_render.go) and the Zig twin
// (native/zigui/src/pickbrowse.zig) render it byte-identically - the golden gate pins them.

import (
	"fmt"
	"path/filepath"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/shellplaces"
)

// pkNavRowSt is one sidebar row. Unpin != "" adds a trailing unpin button (pinned rows only).
type pkNavRowSt struct {
	Act   string `json:"act"`
	Icon  string `json:"icon"`
	Label string `json:"label"`
	On    bool   `json:"on"`
	Unpin string `json:"unpin"`
}

type pkGroupSt struct {
	Header string       `json:"header"`
	Rows   []pkNavRowSt `json:"rows"`
}

type pkCrumbSt struct {
	Label string `json:"label"`
	Act   string `json:"act"`
}

type pkChipSt struct {
	Label  string `json:"label"`
	Act    string `json:"act"`
	Active bool   `json:"active"`
}

// pkEntrySt is one visible row/tile. Cols are list-only; Img (a resolved loopback URL) wins over
// Glyph in grid view; SelAct != "" is the multi-select checkbox act.
type pkEntrySt struct {
	Act      string `json:"act"`
	SelAct   string `json:"selAct"`
	Glyph    string `json:"glyph"`
	Img      string `json:"img"`
	Name     string `json:"name"`
	Modified string `json:"modified"`
	Size     string `json:"size"`
	Type     string `json:"typ"`
	Checked  bool   `json:"checked"`
	Sel      bool   `json:"sel"`
	HL       bool   `json:"hl"`
}

// pkBrowseSt is the whole picker modal resolved. Root wire message.
type pkBrowseSt struct {
	Title  string      `json:"title"`
	Groups []pkGroupSt `json:"groups"`
	Crumbs []pkCrumbSt `json:"crumbs"`

	PathVal   string `json:"pathVal"`
	PathPH    string `json:"pathPH"`
	SearchVal string `json:"searchVal"`
	SearchPH  string `json:"searchPH"`

	Sorts     []pkChipSt `json:"sorts"`
	ViewList  pkChipSt   `json:"viewList"`
	ViewGrid  pkChipSt   `json:"viewGrid"`
	Hidden    pkChipSt   `json:"hidden"`
	Pin       pkChipSt   `json:"pin"`
	HasFilter bool       `json:"hasFilter"`
	FilterOne pkChipSt   `json:"filterOne"`
	FilterAll pkChipSt   `json:"filterAll"`

	Grid    bool   `json:"grid"`
	ColName string `json:"colName"`
	ColMod  string `json:"colMod"`
	ColSize string `json:"colSize"`
	ColType string `json:"colType"`

	Entries []pkEntrySt `json:"entries"`
	Empty   string      `json:"empty"` // set -> render empty/error state instead of rows
	More    string      `json:"more"`

	// footer
	Readout   string `json:"readout"`
	SaveMode  bool   `json:"saveMode"`
	SaveVal   string `json:"saveVal"`
	SavePH    string `json:"savePH"`
	Badge     string `json:"badge"`     // overwrite warning ("" = none)
	SysDialog string `json:"sysDialog"` // escape-hatch button label ("" = none)
	Cancel    string `json:"cancel"`
	Primary   string `json:"primary"`
}

// pkBrowseState resolves the picker session into render state. Caller holds s.mu.
func (u *UI) pkBrowseState(s *pkState) pkBrowseSt {
	st := pkBrowseSt{
		Title:     s.title,
		Groups:    u.pkGroups(s),
		Crumbs:    pkCrumbs(s.dir),
		PathVal:   s.dir,
		PathPH:    i18n.T("picker.pathHint"),
		SearchVal: s.search,
		SearchPH:  i18n.T("picker.searchHint"),
		Grid:      s.grid,
		ColName:   i18n.T("picker.col.name"),
		ColMod:    i18n.T("picker.col.modified"),
		ColSize:   i18n.T("picker.col.size"),
		ColType:   i18n.T("picker.col.type"),
		Cancel:    i18n.T("common.cancel"),
	}
	for _, sc := range [][2]string{{"name", i18n.T("picker.sort.name")}, {"modified", i18n.T("picker.sort.modified")},
		{"size", i18n.T("picker.sort.size")}, {"type", i18n.T("picker.sort.type")}} {
		lbl := sc[1]
		if s.sortBy == sc[0] {
			if s.sortDesc {
				lbl += " ↓"
			} else {
				lbl += " ↑"
			}
		}
		st.Sorts = append(st.Sorts, pkChipSt{lbl, "pk-sort:" + sc[0], s.sortBy == sc[0]})
	}
	st.ViewList = pkChipSt{i18n.T("picker.view.list"), "pk-view:list", !s.grid}
	st.ViewGrid = pkChipSt{i18n.T("picker.view.grid"), "pk-view:grid", s.grid}
	st.Hidden = pkChipSt{i18n.T("picker.hidden"), "pk-hidden", s.hidden}
	pinned := u.libMarks(u.lib()).Has(s.dir)
	pinLbl := i18n.T("picker.pin")
	if pinned {
		pinLbl = i18n.T("picker.pinned")
	}
	st.Pin = pkChipSt{pinLbl, "pk-pin", pinned}
	if len(s.filter.exts) > 0 && s.kind != "dir" {
		st.HasFilter = true
		st.FilterOne = pkChipSt{s.filter.label, "pk-filter:match", !s.filterAll}
		st.FilterAll = pkChipSt{i18n.T("picker.allFiles"), "pk-filter:all", s.filterAll}
	}
	u.pkEntriesState(s, &st)
	if st.Entries == nil { // JSON-fallback decode rejects "entries":null; empty dirs render via Empty
		st.Entries = []pkEntrySt{}
	}
	pkFootState(s, &st)
	return st
}

// pkGroups resolves the sidebar groups (QUICK ACCESS / PLACES / RECENT / PINNED). Caller holds s.mu.
func (u *UI) pkGroups(s *pkState) []pkGroupSt {
	var groups []pkGroupSt
	if qa := shellplaces.Places(); len(qa) > 0 {
		g := pkGroupSt{Header: i18n.T("picker.group.quickAccess")}
		for i, p := range qa {
			if i >= pkSideQA {
				break
			}
			ic := "📌"
			if !p.Pinned {
				ic = "🕘"
			}
			g.Rows = append(g.Rows, pkNavRowSt{"pk-nav:" + p.Path, ic, p.Name, strings.EqualFold(p.Path, s.dir), ""})
		}
		groups = append(groups, g)
	}
	places := pkGroupSt{Header: i18n.T("picker.group.places")}
	d := localmedia.Defaults()
	for _, kv := range [][2]string{{"home", d.Home}, {"desktop", d.Desktop}, {"documents", d.Documents},
		{"downloads", d.Downloads}, {"music", d.Music}, {"videos", d.Videos}, {"pictures", d.Pictures}} {
		if kv[1] == "" {
			continue
		}
		places.Rows = append(places.Rows, pkNavRowSt{"pk-nav:" + kv[1], "⌂", i18n.T("library.browse." + kv[0]), strings.EqualFold(kv[1], s.dir), ""})
	}
	for _, dr := range libDrives() {
		places.Rows = append(places.Rows, pkNavRowSt{"pk-nav:" + dr, "💾", dr, strings.EqualFold(dr, s.dir), ""})
	}
	groups = append(groups, places)
	if rec := u.pkRecent(); len(rec) > 0 {
		g := pkGroupSt{Header: i18n.T("picker.group.recent")}
		for _, r := range rec {
			g.Rows = append(g.Rows, pkNavRowSt{"pk-nav:" + r, "🕘", filepath.Base(r), strings.EqualFold(r, s.dir), ""})
		}
		groups = append(groups, g)
	}
	if marks := u.libMarks(u.lib()).List(); len(marks) > 0 {
		g := pkGroupSt{Header: i18n.T("library.nav.pinned")}
		for _, m := range marks {
			g.Rows = append(g.Rows, pkNavRowSt{"pk-nav:" + m.Path, "★", m.Label, strings.EqualFold(m.Path, s.dir), "pk-unpin:" + m.Path})
		}
		groups = append(groups, g)
	}
	return groups
}

// pkCrumbs splits dir into breadcrumb parts.
func pkCrumbs(dir string) []pkCrumbSt {
	var out []pkCrumbSt
	for _, seg := range pkCrumbSegs(dir) {
		out = append(out, pkCrumbSt{seg[0], "pk-nav:" + seg[1]})
	}
	return out
}

// pkEntriesState fills st.Entries / Empty / More from the filtered+sorted visible list. Caller holds s.mu.
func (u *UI) pkEntriesState(s *pkState, st *pkBrowseSt) {
	if s.listErr != "" {
		st.Empty = i18n.T("picker.unreadable") + ": " + s.listErr
		return
	}
	vis, total := pkVisible(s)
	if len(vis) == 0 {
		st.Empty = i18n.T("picker.empty")
		return
	}
	st.Entries = make([]pkEntrySt, 0, len(vis))
	for i, e := range vis {
		row := pkEntrySt{
			Name:  e.Name,
			Glyph: pkGlyph(e),
			HL:    i == s.hlIdx,
			Sel:   (s.kind == "file" && s.selOne == e.Path) || (s.kind == "multi" && s.sel[e.Path]),
		}
		if e.IsDirectory {
			row.Act = "pk-nav:" + e.Path
		} else {
			row.Act = "pk-open:" + e.Path
			row.Modified = pkShortMod(e.ModifiedAt)
			row.Size = humanSize(e.SizeBytes)
			row.Type = pkTypeLabel(e)
			if s.kind == "multi" {
				row.SelAct = "pk-sel:" + e.Path
				row.Checked = s.sel[e.Path]
			}
			if st.Grid && e.Kind == "image" {
				row.Img = u.imgURL(e.Path, 160)
			}
		}
		st.Entries = append(st.Entries, row)
	}
	if total > len(vis) {
		st.More = i18n.T("picker.showingNofM", i18n.A{"n": fmt.Sprint(len(vis)), "m": fmt.Sprint(total)})
	}
}

// pkFootState fills the footer fields. Caller holds s.mu.
func pkFootState(s *pkState, st *pkBrowseSt) {
	switch s.kind {
	case "dir":
		st.Readout, st.Primary = s.dir, i18n.T("picker.chooseFolder")
	case "file":
		st.Readout, st.Primary = s.selOne, i18n.T("picker.choose")
	case "multi":
		st.Readout, st.Primary = i18n.T("picker.selectedN", i18n.A{"n": fmt.Sprint(len(s.sel))}), i18n.T("picker.choose")
	case "save":
		st.SaveMode, st.Readout = true, s.dir
		st.SaveVal, st.SavePH = s.saveName, i18n.T("picker.filename")
		if s.saveExt != "" {
			st.SavePH = "*." + s.saveExt
		}
		st.Primary = i18n.T("picker.save")
		if s.confirmOW {
			st.Primary, st.Badge = i18n.T("picker.overwrite"), i18n.T("picker.overwriteWarn")
		}
	}
	if pkNativeAvailable() {
		st.SysDialog = i18n.T("picker.systemDialog")
	}
}
