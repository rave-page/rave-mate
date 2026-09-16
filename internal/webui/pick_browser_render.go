package webui

// Pure renderer for the in-app picker modal - the byte-exact GOLDEN REFERENCE the Zig twin
// (native/zigui/src/pickbrowse.zig) mirrors. Renders from the resolved pkBrowseSt
// (pick_browser_state.go); reuses the .rp-*/.libnav/.field-input recipes. The entries list + footer
// are patched as fragments so search/sort/select never steal focus from the search box.

import (
	"fmt"
	"html"
	"path/filepath"
	"sort"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/zigui"
)

// pkModalHTML renders the whole picker modal (into __modal) - Zig when linked, else the Go golden.
func (u *UI) pkModalHTML() string {
	s := u.pk()
	s.mu.Lock()
	st := u.pkBrowseState(s)
	s.mu.Unlock()
	if zigui.Available() {
		if h, ok := zigWire("RenderPkBrowseV2", wirePkBrowse(st), zigui.RenderPkBrowseV2,
			zigui.RenderPkBrowse, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return pkBrowseHTMLOf(st)
}

// pkBrowseHTMLOf is the pure full-modal renderer.
func pkBrowseHTMLOf(st pkBrowseSt) string {
	body := `<div class=pk-wrap>` +
		`<div class="pk-side libnav" id=pk-side>` + pkSidebarHTMLOf(st.Groups) + `</div>` +
		`<div class=pk-main>` +
		`<div class=pk-bar>` + pkNavBtnsHTML() + `<span id=pk-crumb>` + pkCrumbHTMLOf(st.Crumbs) + `</span></div>` +
		pkToolbarHTMLOf(st) +
		`<div class=pk-entries id=pk-entries>` + pkEntriesHTMLOf(st) + `</div>` +
		`</div></div>`
	return modal(st.Title, body, `<div id=pk-foot class=pk-foot-l>`+pkFootHTMLOf(st)+`</div>`)
}

// pkPatchEntries re-renders just the entries fragment (no focus loss) + scrolls the highlight in.
func (u *UI) pkPatchEntries() {
	s := u.pk()
	s.mu.Lock()
	tok := s.pkTok
	if !tok.live() || u.modalCur() != tok {
		s.mu.Unlock()
		return
	}
	st := pkBrowseSt{Grid: s.grid, ColName: i18n.T("picker.col.name"), ColMod: i18n.T("picker.col.modified"),
		ColSize: i18n.T("picker.col.size"), ColType: i18n.T("picker.col.type")}
	u.pkEntriesState(s, &st)
	s.mu.Unlock()
	u.eval("window.__patch('pk-entries'," + jsQuote(pkEntriesHTMLOf(st)) + ");var _h=document.getElementById('pk-hl');if(_h)_h.scrollIntoView({block:'nearest'})")
}

func (u *UI) pkPatchFoot() {
	s := u.pk()
	s.mu.Lock()
	tok := s.pkTok
	if !tok.live() || u.modalCur() != tok {
		s.mu.Unlock()
		return
	}
	var st pkBrowseSt
	st.Cancel = i18n.T("common.cancel")
	pkFootState(s, &st)
	s.mu.Unlock()
	u.eval("window.__patch('pk-foot'," + jsQuote(pkFootHTMLOf(st)) + ")")
}

// ── sidebar ──

func pkSidebarHTMLOf(groups []pkGroupSt) string {
	var b strings.Builder
	for _, g := range groups {
		b.WriteString(navHd(g.Header))
		for _, r := range g.Rows {
			if r.Unpin != "" {
				cls := "libnav-it"
				if r.On {
					cls += " on"
				}
				b.WriteString(`<div class="` + cls + `"><span class=libnav-ic data-act="` + html.EscapeString(r.Act) + `">` + r.Icon +
					`</span><span class=libnav-t data-act="` + html.EscapeString(r.Act) + `">` + html.EscapeString(r.Label) + `</span>` +
					btn("✕", "ghost", r.Unpin, "") + `</div>`)
				continue
			}
			b.WriteString(navIt(r.Act, r.Icon, r.Label, "", r.On))
		}
	}
	return b.String()
}

// ── nav buttons + breadcrumb ──

func pkNavBtnsHTML() string {
	return btn("‹", "outline", "pk-back", "") + btn("›", "outline", "pk-fwd", "") + btn("↑", "outline", "pk-up", "")
}

func pkCrumbHTMLOf(crumbs []pkCrumbSt) string {
	var b strings.Builder
	b.WriteString(`<span class=lib-crumb>`)
	for i, seg := range crumbs {
		b.WriteString(btn(seg.Label, "ghost", seg.Act, ""))
		if i < len(crumbs)-1 {
			b.WriteString(`<span class=sep>›</span>`)
		}
	}
	b.WriteString(`</span>`)
	return b.String()
}

// ── toolbar ──

func pkToolbarHTMLOf(st pkBrowseSt) string {
	var b strings.Builder
	b.WriteString(`<div class=pk-bar>`)
	b.WriteString(`<span class=pk-path>` + fieldRaw("pk-goto", st.PathVal, st.PathPH) + `</span>`)
	b.WriteString(fieldRaw("pk-search", st.SearchVal, st.SearchPH))
	b.WriteString(`</div>`)
	b.WriteString(`<div class=pk-bar>`)
	for _, c := range st.Sorts {
		b.WriteString(fchip(c.Label, "", c.Act, c.Active))
	}
	b.WriteString(`<span class=seg>` + fchip(st.ViewList.Label, "", st.ViewList.Act, st.ViewList.Active) +
		fchip(st.ViewGrid.Label, "", st.ViewGrid.Act, st.ViewGrid.Active) + `</span>`)
	b.WriteString(fchip(st.Hidden.Label, "", st.Hidden.Act, st.Hidden.Active))
	b.WriteString(fchip(st.Pin.Label, "", st.Pin.Act, st.Pin.Active))
	if st.HasFilter {
		b.WriteString(`<span class=seg>` + fchip(st.FilterOne.Label, "", st.FilterOne.Act, st.FilterOne.Active) +
			fchip(st.FilterAll.Label, "", st.FilterAll.Act, st.FilterAll.Active) + `</span>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── entries ──

func pkEntriesHTMLOf(st pkBrowseSt) string {
	if st.Empty != "" {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	if st.Grid {
		b.WriteString(pkGridHTMLOf(st.Entries))
	} else {
		b.WriteString(pkListHTMLOf(st))
	}
	if st.More != "" {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(st.More) + `</p>`)
	}
	return b.String()
}

func pkListHTMLOf(st pkBrowseSt) string {
	var b strings.Builder
	b.WriteString(`<div class=pk-cols><span>` + html.EscapeString(st.ColName) + `</span><span>` +
		html.EscapeString(st.ColMod) + `</span><span>` + html.EscapeString(st.ColSize) +
		`</span><span>` + html.EscapeString(st.ColType) + `</span></div>`)
	for _, e := range st.Entries {
		cls, attr := "pk-row", ""
		if e.Sel {
			cls += " sel"
		}
		if e.HL {
			cls += " hl"
			attr = ` id=pk-hl aria-selected=true`
		}
		cb := ""
		if e.SelAct != "" {
			ck := ""
			if e.Checked {
				ck = " checked"
			}
			cb = `<input type=checkbox data-act="` + html.EscapeString(e.SelAct) + `"` + ck + `>`
		}
		b.WriteString(`<div class="` + cls + `" data-act="` + html.EscapeString(e.Act) + `"` + attr + `>` +
			`<span class=pk-row-n>` + cb + `<span>` + e.Glyph + `</span><span class=t>` + html.EscapeString(e.Name) + `</span></span>` +
			`<span class=pk-row-m>` + html.EscapeString(e.Modified) + `</span>` +
			`<span class=pk-row-s>` + html.EscapeString(e.Size) + `</span>` +
			`<span class=pk-row-k>` + html.EscapeString(e.Type) + `</span></div>`)
	}
	return b.String()
}

func pkGridHTMLOf(entries []pkEntrySt) string {
	var b strings.Builder
	b.WriteString(`<div class=lib-grid>`)
	for _, e := range entries {
		cls, attr := "gcard", ""
		if e.Sel {
			cls += " pk-tile sel"
		}
		if e.HL {
			cls += " hl"
			attr = ` id=pk-hl aria-selected=true`
		}
		ic := e.Glyph
		if e.Img != "" {
			ic = `<img src="` + html.EscapeString(e.Img) + `" loading=lazy alt="">`
		}
		b.WriteString(`<div class="` + cls + `" data-act="` + html.EscapeString(e.Act) + `"` + attr + `><div class=gcard-ic>` + ic +
			`</div><div class=gcard-t>` + html.EscapeString(e.Name) + `</div></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── footer ──

func pkFootHTMLOf(st pkBrowseSt) string {
	var b strings.Builder
	if st.SaveMode {
		b.WriteString(`<span class=pk-read>` + html.EscapeString(st.Readout) + `</span>` + fieldRaw("pk-name", st.SaveVal, st.SavePH))
	} else {
		b.WriteString(`<span class=pk-read>` + html.EscapeString(st.Readout) + `</span>`)
	}
	if st.Badge != "" {
		b.WriteString(badge(st.Badge, "warning"))
	}
	if st.SysDialog != "" {
		b.WriteString(btn(st.SysDialog, "ghost", "pk-sys", ""))
	}
	b.WriteString(btn(st.Cancel, "outline", "modal-close", ""))
	b.WriteString(btn(st.Primary, "primary", "pk-choose", ""))
	return b.String()
}

// ── entry helpers (shared with pick_browser_state.go) ──

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

// ── visible-entry computation (filter + sort + cap); used by the resolver + keyboard nav ──

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

// pkCrumbSegs splits dir into (label, cumulative-path) breadcrumb parts.
func pkCrumbSegs(dir string) [][2]string {
	dir = filepath.Clean(dir)
	vol := filepath.VolumeName(dir) // "C:" on win, "" on unix
	rest := strings.Trim(strings.TrimPrefix(dir, vol), `/\`)
	root := vol + string(filepath.Separator)
	rootLabel := vol
	if rootLabel == "" {
		rootLabel = "/"
	}
	out := [][2]string{{rootLabel, root}}
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

// sortEntries orders dirs-first, then by the chosen key/direction.
func sortEntries(es []localmedia.Entry, by string, desc bool) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.IsDirectory != b.IsDirectory {
			return a.IsDirectory
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
