package webui

// Library fixer/section SUBVIEWS as render state (Zig migration wave 3): the five seams the
// Library tab used to embed as pre-rendered `Raw` markup now cross the ABI as structured
// state - nav rail, beatgrid-fixer rail + results, tag-fixer results + editor, prep-playlist
// picker, "works well together" section.
//
// The impure builders stay in their feature files (library_navrail.go, library_gridfix.go,
// library_tagfix.go, library_prep.go, library_compat.go); the pure renderers below are the
// untagged fallback AND the golden reference for native/zigui/src/libfixers.zig.
//
// Rules (same as render_library_state.go):
//   - every slice field carries `,omitempty`: a nil Go slice marshals to JSON null, which the
//     Zig parser rejects (silent fallback of the WHOLE view to Go);
//   - numbers are pre-formatted Go-side (no float ever crosses the ABI);
//   - `dl` fields are the Go-resolved strings.ToLower(label) (Unicode lowering stays in Go).

import (
	"strings"

	"rave.page/mate/internal/zigui"
)

// ── nav rail (library_navrail.go) ──

// libNavRowSt is one nav-rail row: a group header (Hd) or a clickable item. Icon is a
// source literal (glyph) spliced UNESCAPED, exactly like the Go navIt original.
type libNavRowSt struct {
	Hd    bool   `json:"hd,omitempty"`
	Label string `json:"label"`
	Act   string `json:"act,omitempty"`
	Icon  string `json:"icon,omitempty"`
	Count string `json:"count,omitempty"`
	On    bool   `json:"on,omitempty"`
}

// libNavSt is the triPane nav column (Collection tree / Browse places+folders).
type libNavSt struct {
	Rows []libNavRowSt `json:"rows,omitempty"`
}

func navHd(label string) string {
	return `<div class=libnav-hd>` + esc(label) + `</div>`
}

func navIt(act, icon, label, count string, on bool) string {
	cls := "libnav-it"
	if on {
		cls += " on"
	}
	n := ""
	if count != "" {
		n = `<span class=libnav-n>` + esc(count) + `</span>`
	}
	return `<div class="` + cls + `" data-act="` + esc(act) + `"><span class=libnav-ic>` + icon +
		`</span><span class=libnav-t>` + esc(label) + `</span>` + n + `</div>`
}

// libNavRailHTMLOf is the pure nav-rail renderer.
func libNavRailHTMLOf(st libNavSt) string {
	var b strings.Builder
	b.WriteString(`<div class=libnav>`)
	for _, r := range st.Rows {
		if r.Hd {
			b.WriteString(navHd(r.Label))
			continue
		}
		b.WriteString(navIt(r.Act, r.Icon, r.Label, r.Count, r.On))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── beatgrid fixer (library_gridfix.go) ──

// gf rail stages. "cal" (live calibration) renders as libGFRunning with an empty tile set -
// same chrome, only the title + the bar-only live fragment differ.
const (
	libGFHealth  = "health"
	libGFConfirm = "confirm"
	libGFRunning = "running"
	libGFDone    = "done"
)

// libGFStatSt is one idle health-card stat (Go gfStat: N is ESCAPED).
type libGFStatSt struct {
	N     string `json:"n"`
	Label string `json:"label"`
	Tone  string `json:"tone,omitempty"`
}

// libGFTileSt is one running/done counter tile (Go gfTile: N is spliced RAW - fmt.Sprint of an
// int, never escaped. Do NOT unify with libGFStatSt, the two differ byte-wise on purpose).
type libGFTileSt struct {
	N     string `json:"n"`
	Label string `json:"label"`
	Tone  string `json:"tone"`
}

// libGFLiveSt is the #gf-live inner fragment (patched ~2 Hz from the run goroutine): counter
// tiles (batch run) or bar-only (calibration) + progress + current track. Pct is progressPct'd
// Go-side - the fill width never crosses the ABI as a float.
type libGFLiveSt struct {
	Tiles   []libGFTileSt `json:"tiles,omitempty"`
	Pct     string        `json:"pct"`
	Caption string        `json:"caption"`
	Current string        `json:"current"`
}

// libGFSt is the beatgrid-fixer rail: it owns the Collection inspector for the whole flow.
type libGFSt struct {
	Kind    string `json:"kind"`
	Eyebrow string `json:"eyebrow"`
	Title   string `json:"title"`

	// health: stats grid + ONE set-note + an optional button row. NoteAfter flips the two
	// (the engine-missing branch notes first, the ready branch notes after the buttons).
	Stats     []libGFStatSt `json:"stats,omitempty"`
	Note      string        `json:"note,omitempty"`
	NoteAfter bool          `json:"noteAfter,omitempty"`
	Btns      []uiBtn       `json:"btns,omitempty"`

	// confirm: scope picker
	ConfirmNote string   `json:"confirmNote,omitempty"`
	Force       uiToggle `json:"force"`
	ForceHint   string   `json:"forceHint,omitempty"`
	Scopes      []uiBtn  `json:"scopes,omitempty"`

	// running / cal
	Live    libGFLiveSt `json:"live"`
	StopLbl string      `json:"stopLbl,omitempty"`

	// done: tiles + ordered hints + the write actions + trailing notes
	Tiles      []libGFTileSt `json:"tiles,omitempty"`
	CachedNote string        `json:"cachedNote,omitempty"`
	Hints      []libHintSt   `json:"hints,omitempty"`
	Acts       []uiBtn       `json:"acts,omitempty"`
	Notes      []string      `json:"notes,omitempty"`
	ApplyNote  string        `json:"applyNote,omitempty"`
}

func gfStat(s libGFStatSt) string {
	cls := "gf-stat"
	if s.Tone != "" {
		cls += " gf-" + s.Tone
	}
	return `<div class="` + cls + `"><div class=gf-n>` + esc(s.N) + `</div><div class=gf-l>` + esc(s.Label) + `</div></div>`
}

func gfTile(t libGFTileSt) string {
	return `<div class="gf-tile gf-` + t.Tone + `"><div class=gf-n>` + t.N + `</div><div class=gf-l>` + esc(t.Label) + `</div></div>`
}

func gfNote(text string) string { return `<div class=set-note>` + esc(text) + `</div>` }

// libGFLiveHTML is the pure #gf-live renderer (no tiles = the calibration variant).
func libGFLiveHTML(st libGFLiveSt) string {
	var b strings.Builder
	if len(st.Tiles) > 0 {
		b.WriteString(`<div class=gf-tiles>`)
		for _, t := range st.Tiles {
			b.WriteString(gfTile(t))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(progressBarStr(st.Pct, st.Caption))
	b.WriteString(`<div class=gf-current>` + esc(st.Current) + `</div>`)
	return b.String()
}

// gfLiveRender renders the #gf-live patch fragment (Zig when linked, Go otherwise).
func gfLiveRender(st libGFLiveSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderLibFixGFLiveV2", wireLibGFLive(st), zigui.RenderLibFixGFLiveV2,
			zigui.RenderLibFixGFLive, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return libGFLiveHTML(st)
}

// libGFRailHTML is the pure fixer-rail renderer.
func libGFRailHTML(st libGFSt) string {
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(st.Eyebrow) + `</div><div class=insp-title>` +
		esc(st.Title) + `</div></div>`)
	switch st.Kind {
	case libGFRunning:
		b.WriteString(`<div id=gf-live>` + libGFLiveHTML(st.Live) + `</div>`)
		b.WriteString(btnRow(btn(st.StopLbl, "outline", "gf-cancel", "")))
	case libGFConfirm:
		b.WriteString(gfNote(st.ConfirmNote))
		// force re-analyze: override the multi-marker/lock skips + the cache (verified stay protected)
		b.WriteString(st.Force.html())
		b.WriteString(gfNote(st.ForceHint))
		b.WriteString(`<div class=btn-col>`)
		for _, a := range st.Scopes {
			b.WriteString(a.html())
		}
		b.WriteString(`</div>`)
	case libGFDone:
		b.WriteString(`<div class=gf-tiles>`)
		for _, t := range st.Tiles {
			b.WriteString(gfTile(t))
		}
		b.WriteString(`</div>`)
		if st.CachedNote != "" {
			b.WriteString(gfNote(st.CachedNote))
		}
		for _, hn := range st.Hints {
			b.WriteString(hint(hn.Tone, hn.Text))
		}
		b.WriteString(`<div class=btn-col>`)
		for _, a := range st.Acts {
			b.WriteString(a.html())
		}
		b.WriteString(`</div>`)
		for _, n := range st.Notes {
			b.WriteString(gfNote(n))
		}
		b.WriteString(gfNote(st.ApplyNote))
	default: // health: collection at a glance + the fixer entry
		b.WriteString(`<div class=gf-stats>`)
		for _, s := range st.Stats {
			b.WriteString(gfStat(s))
		}
		b.WriteString(`</div>`)
		if !st.NoteAfter && st.Note != "" {
			b.WriteString(gfNote(st.Note))
		}
		if len(st.Btns) > 0 {
			b.WriteString(uiBtnRow(st.Btns))
		}
		if st.NoteAfter && st.Note != "" {
			b.WriteString(gfNote(st.Note))
		}
	}
	return b.String()
}

// ── fixer results (they replace the collection track list) ──

const (
	libFixResGF = "gf" // beatgrid batch outcome table
	libFixResTF = "tf" // tag-fixer problem list
)

// libFixResSt is the results view that swaps the collection list: one kind + its sub-state.
type libFixResSt struct {
	Kind string     `json:"kind"`
	GF   libGFResSt `json:"gf"`
	TF   libTFResSt `json:"tf"`
}

// libGFResRowSt is one batch-result row. St ("FIX"/"OK"/"SKIP"/"ERR") is spliced UNESCAPED
// into both the chip class (lowered Go-side: StLow) and the chip text, like the Go original.
type libGFResRowSt struct {
	Path   string `json:"path"`
	St     string `json:"st"`
	StLow  string `json:"stLow"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Delta  string `json:"delta"` // pre-formatted (BPM / ms deltas are float-formatted in Go)
}

// libGFResSt is the beatgrid batch outcome table.
type libGFResSt struct {
	Chips   []libChipSt     `json:"chips,omitempty"`
	Rows    []libGFResRowSt `json:"rows,omitempty"`
	IsEmpty bool            `json:"isEmpty"`
	Empty   string          `json:"empty"`
}

// libTFRowSt is one proposed tag repair. Idx is the raw row index (act = "tf-sel:"+Idx,
// spliced unescaped by Go); Cur/Proposed arrive already truncated to 60 runes.
type libTFRowSt struct {
	Idx      string `json:"idx"`
	Checked  bool   `json:"checked"`
	Path     string `json:"path"`
	Base     string `json:"base"`
	Field    string `json:"field"`
	Cur      string `json:"cur"`
	Proposed string `json:"proposed"`
}

// libTFGrpSt is one problem kind's group (header + bulk toggles + capped rows).
type libTFGrpSt struct {
	Title   string       `json:"title"`
	Badge   string       `json:"badge"`
	AllLbl  string       `json:"allLbl"`
	AllAct  string       `json:"allAct"`
	NoneLbl string       `json:"noneLbl"`
	NoneAct string       `json:"noneAct"`
	Desc    string       `json:"desc"`
	Rows    []libTFRowSt `json:"rows,omitempty"`
	More    string       `json:"more,omitempty"` // showingFirst line (rows were capped)
}

// libTFResSt is the tag-fixer view: scanning progress, or the grouped problem list.
type libTFResSt struct {
	Eyebrow string `json:"eyebrow"`
	Title   string `json:"title"`
	Desc    string `json:"desc"`

	Scanning bool   `json:"scanning,omitempty"`
	Pct      string `json:"pct,omitempty"`
	ScanCap  string `json:"scanCap,omitempty"`
	CloseLbl string `json:"closeLbl"`

	ApplyLbl  string       `json:"applyLbl,omitempty"`
	RescanLbl string       `json:"rescanLbl,omitempty"`
	Hints     []libHintSt  `json:"hints,omitempty"`
	Skipped   string       `json:"skipped,omitempty"`
	IsEmpty   bool         `json:"isEmpty,omitempty"`
	Empty     string       `json:"empty,omitempty"`
	Groups    []libTFGrpSt `json:"groups,omitempty"`
}

// libFixResHTML is the pure results renderer.
func libFixResHTML(st libFixResSt) string {
	if st.Kind == libFixResTF {
		return libTFResHTML(st.TF)
	}
	return libGFResHTML(st.GF)
}

// libGFResHTML renders the batch outcome table.
func libGFResHTML(st libGFResSt) string {
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>`)
	for _, c := range st.Chips {
		b.WriteString(c.html())
	}
	b.WriteString(`</div><div class=trk-table>`)
	for _, r := range st.Rows {
		b.WriteString(`<div class=trk-row data-ctx="lib-ctx:` + esc(r.Path) + `">` +
			`<span class="gf-chip gf-` + r.StLow + `">` + r.St + `</span>` +
			`<span class=trk-main data-act="lib-track:` + esc(r.Path) + `"><span class=trk-title>` + esc(r.Title) + `</span>` +
			`<span class=trk-sub>` + esc(r.Detail) + `</span></span>` +
			`<span class=gf-delta>` + esc(r.Delta) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	if st.IsEmpty {
		b.WriteString(emptyState(st.Empty))
	}
	return b.String()
}

// libTFResHTML renders the tag-fixer view.
func libTFResHTML(st libTFResSt) string {
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(st.Eyebrow) + `</div><div class=insp-title>` +
		esc(st.Title) + `</div></div>`)
	b.WriteString(`<p class=page-sub>` + esc(st.Desc) + `</p>`)
	if st.Scanning {
		b.WriteString(progressBarStr(st.Pct, st.ScanCap))
		b.WriteString(btnRow(btn(st.CloseLbl, "ghost", "tf-close", "")))
		return b.String()
	}
	b.WriteString(`<div class=lib-toolbar>` +
		btn(st.ApplyLbl, "primary", "tf-apply", "") +
		btn(st.RescanLbl, "outline", "lib-tagfix", "") +
		btn(st.CloseLbl, "ghost", "tf-close", "") + `</div>`)
	for _, hn := range st.Hints {
		b.WriteString(hint(hn.Tone, hn.Text))
	}
	if st.Skipped != "" {
		b.WriteString(`<p class=page-sub>` + esc(st.Skipped) + `</p>`)
	}
	if st.IsEmpty {
		return b.String() + emptyState(st.Empty)
	}
	// grouped by kind, stable order
	for _, g := range st.Groups {
		b.WriteString(`<div class=tf-grp><div class=tf-grphead><span class=tf-grptitle>` + esc(g.Title) + `</span>` +
			badge(g.Badge, "secondary") +
			btn(g.AllLbl, "ghost", g.AllAct, "") +
			btn(g.NoneLbl, "ghost", g.NoneAct, "") + `</div>`)
		b.WriteString(`<p class=page-sub>` + esc(g.Desc) + `</p>`)
		for _, r := range g.Rows {
			chk := ""
			if r.Checked {
				chk = " checked"
			}
			b.WriteString(`<label class=tf-row><input type=checkbox data-act="tf-sel:` + r.Idx + `"` + chk + `>` +
				`<span class=tf-file title="` + esc(r.Path) + `">` + esc(r.Base) + `</span>` +
				`<span class=tf-field>` + esc(r.Field) + `</span>` +
				`<span class=tf-diff><s>` + esc(r.Cur) + `</s> → <b>` +
				esc(r.Proposed) + `</b></span></label>`)
		}
		if g.More != "" {
			b.WriteString(`<p class=page-sub>` + esc(g.More) + `</p>`)
		}
		b.WriteString(`</div>`)
	}
	return b.String()
}

// ── per-track tag editor (inspector Tags tail, library_tagfix.go) ──

// libTagEdSt is the file-tag editor. Open=false renders only the "Edit tags" opener.
type libTagEdSt struct {
	Open      bool           `json:"open,omitempty"`
	OpenLbl   string         `json:"openLbl,omitempty"`
	Desc      string         `json:"desc,omitempty"`
	Fields    []libPBFieldSt `json:"fields,omitempty"`
	SaveLbl   string         `json:"saveLbl,omitempty"`
	CancelLbl string         `json:"cancelLbl,omitempty"`
}

// libTagEdHTML is the pure tag-editor renderer.
func libTagEdHTML(st libTagEdSt) string {
	if !st.Open {
		return btnRow(btn(st.OpenLbl, "outline", "tf-edit-open", ""))
	}
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + esc(st.Desc) + `</p>`)
	b.WriteString(`<div class=pbuilder>`)
	for _, f := range st.Fields {
		b.WriteString(f.html())
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div class=btn-row>` +
		btn(st.SaveLbl, "primary", "tf-edit-save", "") +
		btn(st.CancelLbl, "ghost", "tf-edit-close", "") + `</div>`)
	return b.String()
}

// ── "works well together" (library_compat.go) ──

// libCompatRowSt is one marked partner (title + joined mark kinds + open button).
type libCompatRowSt struct {
	Title string `json:"title"`
	Sub   string `json:"sub"`
	Act   string `json:"act"`
}

// libCompatSecSt is the inspector section: capped partners (or a loading/empty line) + find.
type libCompatSecSt struct {
	IsEmpty bool             `json:"isEmpty,omitempty"`
	Empty   string           `json:"empty,omitempty"`
	Rows    []libCompatRowSt `json:"rows,omitempty"`
	OpenLbl string           `json:"openLbl"`
	FindLbl string           `json:"findLbl"`
	FindAct string           `json:"findAct"`
}

// libCompatSecHTML is the pure compat-section renderer.
func libCompatSecHTML(st libCompatSecSt) string {
	var b strings.Builder
	if st.IsEmpty {
		b.WriteString(`<p class=page-sub>` + esc(st.Empty) + `</p>`)
	} else {
		for _, r := range st.Rows {
			b.WriteString(itemRow(r.Title, r.Sub, btn(st.OpenLbl, "ghost", r.Act, "")))
		}
	}
	b.WriteString(btnRow(btn(st.FindLbl, "outline", st.FindAct, "")))
	return b.String()
}
