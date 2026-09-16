package webui

// Rendering for the in-app picker modal (pick_browser.go). Pure Go, reuses the .rp-*/.libnav/
// .field-input recipes; the entries list + footer are patched as fragments so search/sort/select
// never steal focus from the search box.

import (
	"fmt"
	"html"
	"path/filepath"
	"sort"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/library"
	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/shellplaces"
)

// pkModalHTML renders the whole picker modal (into __modal). Locks s.mu.
func (u *UI) pkModalHTML() string {
	s := u.pk()
	s.mu.Lock()
	defer s.mu.Unlock()
	body := `<div class=pk-wrap>` +
		`<div class="pk-side libnav" id=pk-side>` + u.pkSidebarLocked(s) + `</div>` +
		`<div class=pk-main>` +
		`<div class=pk-bar>` + pkNavBtnsLocked(s) + `<span id=pk-crumb>` + pkCrumbLocked(s) + `</span></div>` +
		u.pkToolbarLocked(s) +
		`<div class=pk-entries id=pk-entries>` + u.pkEntriesLocked(s) + `</div>` +
		`</div></div>`
	return modal(s.title, body, `<div id=pk-foot class=pk-foot-l>`+pkFootLocked(s)+`</div>`)
}

// pkPatchEntries re-renders just the entries + footer count fragment (no focus loss).
func (u *UI) pkPatchEntries() {
	s := u.pk()
	s.mu.Lock()
	tok := s.pkTok
	if !tok.live() || u.modalCur() != tok {
		s.mu.Unlock()
		return
	}
	h := u.pkEntriesLocked(s)
	s.mu.Unlock()
	// scroll the keyboard-highlighted row into view after the fragment swap
	u.eval("window.__patch('pk-entries'," + jsQuote(h) + ");var _h=document.getElementById('pk-hl');if(_h)_h.scrollIntoView({block:'nearest'})")
}

func (u *UI) pkPatchFoot() {
	s := u.pk()
	s.mu.Lock()
	tok := s.pkTok
	if !tok.live() || u.modalCur() != tok {
		s.mu.Unlock()
		return
	}
	h := pkFootLocked(s)
	s.mu.Unlock()
	u.eval("window.__patch('pk-foot'," + jsQuote(h) + ")")
}

// ── sidebar ──

func (u *UI) pkSidebarLocked(s *pkState) string {
	var b strings.Builder
	// QUICK ACCESS - OS pinned + frequent folders
	if qa := shellplaces.Places(); len(qa) > 0 {
		b.WriteString(navHd(i18n.T("picker.group.quickAccess")))
		n := 0
		for _, p := range qa {
			ic := "📌"
			if !p.Pinned {
				ic = "🕘"
			}
			b.WriteString(navIt("pk-nav:"+p.Path, ic, p.Name, "", strings.EqualFold(p.Path, s.dir)))
			if n++; n >= pkSideQA {
				break
			}
		}
	}
	// PLACES - known folders + drives
	b.WriteString(navHd(i18n.T("picker.group.places")))
	d := localmedia.Defaults()
	for _, kv := range [][2]string{{"home", d.Home}, {"desktop", d.Desktop}, {"documents", d.Documents},
		{"downloads", d.Downloads}, {"music", d.Music}, {"videos", d.Videos}, {"pictures", d.Pictures}} {
		if kv[1] == "" {
			continue
		}
		b.WriteString(navIt("pk-nav:"+kv[1], "⌂", i18n.T("library.browse."+kv[0]), "", strings.EqualFold(kv[1], s.dir)))
	}
	for _, dr := range libDrives() {
		b.WriteString(navIt("pk-nav:"+dr, "💾", dr, "", strings.EqualFold(dr, s.dir)))
	}
	// RECENT - this app's last folders
	if rec := u.pkRecent(); len(rec) > 0 {
		b.WriteString(navHd(i18n.T("picker.group.recent")))
		for _, r := range rec {
			b.WriteString(navIt("pk-nav:"+r, "🕘", filepath.Base(r), "", strings.EqualFold(r, s.dir)))
		}
	}
	// PINNED - the app's own bookmarks, unpin inline
	if marks := u.libMarks(u.lib()).List(); len(marks) > 0 {
		b.WriteString(navHd(i18n.T("library.nav.pinned")))
		for _, m := range marks {
			b.WriteString(pkPinRow(m, strings.EqualFold(m.Path, s.dir)))
		}
	}
	return b.String()
}

// pkPinRow is a PINNED sidebar row with a trailing unpin ✕ (navIt has no trailing slot).
func pkPinRow(m library.Bookmark, on bool) string {
	cls := "libnav-it"
	if on {
		cls += " on"
	}
	return `<div class="` + cls + `"><span class=libnav-ic data-act="pk-nav:` + html.EscapeString(m.Path) + `">★</span>` +
		`<span class=libnav-t data-act="pk-nav:` + html.EscapeString(m.Path) + `">` + html.EscapeString(m.Label) + `</span>` +
		btn("✕", "ghost", "pk-unpin:"+m.Path, "") + `</div>`
}

// ── breadcrumb + nav buttons ──

func pkNavBtnsLocked(s *pkState) string {
	return btn("‹", "outline", "pk-back", "") + btn("›", "outline", "pk-fwd", "") + btn("↑", "outline", "pk-up", "")
}

func pkCrumbLocked(s *pkState) string {
	var b strings.Builder
	b.WriteString(`<span class=lib-crumb>`)
	segs := pkCrumbSegs(s.dir)
	for i, seg := range segs {
		b.WriteString(btn(seg[0], "ghost", "pk-nav:"+seg[1], ""))
		if i < len(segs)-1 {
			b.WriteString(`<span class=sep>›</span>`)
		}
	}
	b.WriteString(`</span>`)
	return b.String()
}

// pkCrumbSegs splits dir into (label, cumulative-path) breadcrumb parts.
func pkCrumbSegs(dir string) [][2]string {
	dir = filepath.Clean(dir)
	vol := filepath.VolumeName(dir) // "C:" on win, "" on unix
	rest := strings.TrimPrefix(dir, vol)
	rest = strings.Trim(rest, `/\`)
	var out [][2]string
	root := vol + string(filepath.Separator)
	rootLabel := vol
	if rootLabel == "" {
		rootLabel = "/"
	}
	out = append(out, [2]string{rootLabel, root})
	if rest == "" {
		return out
	}
	cur := root
	for _, part := range strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' }) {
		cur = filepath.Join(cur, part)
		out = append(out, [2]string{part, cur})
	}
	return out
}

// ── toolbar ──

func (u *UI) pkToolbarLocked(s *pkState) string {
	var b strings.Builder
	b.WriteString(`<div class=pk-bar>`)
	b.WriteString(`<span class=pk-path>` + fieldRaw("pk-goto", s.dir, i18n.T("picker.pathHint")) + `</span>`)
	b.WriteString(fieldRaw("pk-search", s.search, i18n.T("picker.searchHint")))
	b.WriteString(`</div>`)
	b.WriteString(`<div class=pk-bar>`)
	// sort chips
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
		b.WriteString(fchip(lbl, "", "pk-sort:"+sc[0], s.sortBy == sc[0]))
	}
	// view toggle
	b.WriteString(`<span class=seg>` + fchip(i18n.T("picker.view.list"), "", "pk-view:list", !s.grid) +
		fchip(i18n.T("picker.view.grid"), "", "pk-view:grid", s.grid) + `</span>`)
	// hidden + pin
	b.WriteString(fchip(i18n.T("picker.hidden"), "", "pk-hidden", s.hidden))
	pinned := u.libMarks(u.lib()).Has(s.dir)
	pinLbl := i18n.T("picker.pin")
	if pinned {
		pinLbl = i18n.T("picker.pinned")
	}
	b.WriteString(fchip(pinLbl, "", "pk-pin", pinned))
	// filter toggle (only when the caller narrowed the type)
	if len(s.filter.exts) > 0 && s.kind != "dir" {
		b.WriteString(`<span class=seg>` + fchip(s.filter.label, "", "pk-filter:match", !s.filterAll) +
			fchip(i18n.T("picker.allFiles"), "", "pk-filter:all", s.filterAll) + `</span>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── entries ──

func (u *UI) pkEntriesLocked(s *pkState) string {
	if s.listErr != "" {
		return emptyState(i18n.T("picker.unreadable") + ": " + s.listErr)
	}
	vis, total := pkVisible(s)
	if len(vis) == 0 {
		return emptyState(i18n.T("picker.empty"))
	}
	var b strings.Builder
	if s.grid {
		b.WriteString(u.pkGridLocked(s, vis))
	} else {
		b.WriteString(u.pkListLocked(s, vis))
	}
	if total > len(vis) {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("picker.showingNofM", i18n.A{"n": fmt.Sprint(len(vis)), "m": fmt.Sprint(total)})) + `</p>`)
	}
	return b.String()
}

// pkHL / pkHLAttr mark the keyboard-highlighted row (class + a stable #pk-hl id + aria-selected so
// ctl and screen readers see it, and pkPatchEntries can scroll it into view).
func pkHL(s *pkState, i int) string {
	if i == s.hlIdx {
		return " hl"
	}
	return ""
}
func pkHLAttr(s *pkState, i int) string {
	if i == s.hlIdx {
		return ` id=pk-hl aria-selected=true`
	}
	return ""
}

func (u *UI) pkListLocked(s *pkState, vis []localmedia.Entry) string {
	var b strings.Builder
	b.WriteString(`<div class=pk-cols><span>` + html.EscapeString(i18n.T("picker.col.name")) + `</span><span>` +
		html.EscapeString(i18n.T("picker.col.modified")) + `</span><span>` + html.EscapeString(i18n.T("picker.col.size")) +
		`</span><span>` + html.EscapeString(i18n.T("picker.col.type")) + `</span></div>`)
	for i, e := range vis {
		selCls, checkbox := "", ""
		act := "pk-open:" + e.Path
		if e.IsDirectory {
			act = "pk-nav:" + e.Path
		}
		if s.kind == "multi" && !e.IsDirectory {
			ck := ""
			if s.sel[e.Path] {
				ck = " checked"
			}
			checkbox = `<input type=checkbox data-act="pk-sel:` + html.EscapeString(e.Path) + `"` + ck + `>`
		}
		if (s.kind == "file" && s.selOne == e.Path) || (s.kind == "multi" && s.sel[e.Path]) {
			selCls = " sel"
		}
		size := ""
		if !e.IsDirectory {
			size = humanSize(e.SizeBytes)
		}
		b.WriteString(`<div class="pk-row` + selCls + pkHL(s, i) + `" data-act="` + html.EscapeString(act) + `"` + pkHLAttr(s, i) + `>` +
			`<span class=pk-row-n>` + checkbox + `<span>` + pkGlyph(e) + `</span><span class=t>` + html.EscapeString(e.Name) + `</span></span>` +
			`<span class=pk-row-m>` + html.EscapeString(pkShortMod(e.ModifiedAt)) + `</span>` +
			`<span class=pk-row-s>` + html.EscapeString(size) + `</span>` +
			`<span class=pk-row-k>` + html.EscapeString(pkTypeLabel(e)) + `</span></div>`)
	}
	return b.String()
}

func (u *UI) pkGridLocked(s *pkState, vis []localmedia.Entry) string {
	var b strings.Builder
	b.WriteString(`<div class=lib-grid>`)
	for i, e := range vis {
		act := "pk-open:" + e.Path
		if e.IsDirectory {
			act = "pk-nav:" + e.Path
		}
		tileCls := "gcard"
		if (s.kind == "file" && s.selOne == e.Path) || (s.kind == "multi" && s.sel[e.Path]) {
			tileCls += " pk-tile sel"
		}
		tileCls += pkHL(s, i)
		ic := pkGlyph(e)
		if e.Kind == "image" {
			if url := u.imgURL(e.Path, 160); url != "" {
				ic = `<img src="` + html.EscapeString(url) + `" loading=lazy alt="">`
			}
		}
		b.WriteString(`<div class="` + tileCls + `" data-act="` + html.EscapeString(act) + `"` + pkHLAttr(s, i) + `><div class=gcard-ic>` + ic +
			`</div><div class=gcard-t>` + html.EscapeString(e.Name) + `</div></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── footer ──

func pkFootLocked(s *pkState) string {
	readout, primary := "", ""
	switch s.kind {
	case "dir":
		readout = s.dir
		primary = btn(i18n.T("picker.chooseFolder"), "primary", "pk-choose", "")
	case "file":
		readout = s.selOne
		primary = btn(i18n.T("picker.choose"), "primary", "pk-choose", "")
	case "multi":
		readout = i18n.T("picker.selectedN", i18n.A{"n": fmt.Sprint(len(s.sel))})
		primary = btn(i18n.T("picker.choose"), "primary", "pk-choose", "")
	case "save":
		ph := i18n.T("picker.filename")
		if s.saveExt != "" {
			ph = "*." + s.saveExt
		}
		readout = `<span class=pk-read>` + html.EscapeString(s.dir) + `</span>` + fieldRaw("pk-name", s.saveName, ph)
		lbl := i18n.T("picker.save")
		if s.confirmOW {
			lbl = i18n.T("picker.overwrite")
		}
		primary = btn(lbl, "primary", "pk-choose", "")
	}
	var b strings.Builder
	if s.kind != "save" {
		b.WriteString(`<span class=pk-read>` + html.EscapeString(readout) + `</span>`)
	} else {
		b.WriteString(readout)
	}
	if s.confirmOW {
		b.WriteString(badge(i18n.T("picker.overwriteWarn"), "warning"))
	}
	if pkNativeAvailable() {
		b.WriteString(btn(i18n.T("picker.systemDialog"), "ghost", "pk-sys", ""))
	}
	b.WriteString(btn(i18n.T("common.cancel"), "outline", "modal-close", ""))
	b.WriteString(primary)
	return b.String()
}

// ── entry helpers ──

// pkVisible filters (search + ext + dir-kind) and sorts the cached entries; returns the capped view
// and the pre-cap total. Caller holds s.mu.
func pkVisible(s *pkState) ([]localmedia.Entry, int) {
	q := strings.ToLower(strings.TrimSpace(s.search))
	exts := s.filter.exts
	if s.filterAll {
		exts = nil
	}
	out := make([]localmedia.Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if s.kind == "dir" && !e.IsDirectory {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Name), q) {
			continue
		}
		if !e.IsDirectory && len(exts) > 0 && !extIn(e.Extension, exts) {
			continue
		}
		out = append(out, e)
	}
	sortEntries(out, s.sortBy, s.sortDesc)
	total := len(out)
	if total > pkMaxRows {
		out = out[:pkMaxRows]
	}
	return out, total
}

func extIn(ext string, exts []string) bool {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	for _, e := range exts {
		if ext == e {
			return true
		}
	}
	return false
}

// sortEntries orders dirs-first, then by the chosen key/direction.
func sortEntries(es []localmedia.Entry, by string, desc bool) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.IsDirectory != b.IsDirectory {
			return a.IsDirectory // dirs always first
		}
		var less bool
		switch by {
		case "modified":
			less = a.ModifiedAt < b.ModifiedAt
		case "size":
			less = a.SizeBytes < b.SizeBytes
		case "type":
			if a.Kind != b.Kind {
				less = a.Kind < b.Kind
			} else {
				less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
			}
		default:
			less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		if desc {
			return !less
		}
		return less
	})
}

func pkGlyph(e localmedia.Entry) string {
	switch e.Kind {
	case "directory":
		return "📁"
	case "audio":
		return "🎵"
	case "video":
		return "🎬"
	case "image":
		return "🖼"
	default:
		return "📄"
	}
}

func pkTypeLabel(e localmedia.Entry) string {
	if e.IsDirectory {
		return i18n.T("picker.type.folder")
	}
	if e.Extension != "" {
		return strings.ToUpper(e.Extension)
	}
	return i18n.T("picker.type.file")
}

// pkShortMod trims an RFC3339 modified stamp to "YYYY-MM-DD HH:MM".
func pkShortMod(rfc string) string {
	if len(rfc) < 16 {
		return rfc
	}
	return rfc[:10] + " " + rfc[11:16]
}

func humanSize(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%.0f KB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f MB", float64(n)/(k*k))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
	}
}

func pkNativeAvailable() bool { return nativePickerAvailable() }
