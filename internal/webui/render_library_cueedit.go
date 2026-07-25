package webui

// Cue-editor render layer (the deepest library subview): impure state builders + PURE
// renderers + the Zig bridge. The Go renderers stay the golden reference
// (zigui_golden_cueedit_test.go); native/zigui/src/cueedit.zig mirrors them byte-for-byte.
//
// Surfaces: `#ce-topbar` (readouts above the waveform, patched on every cursor/edit move),
// the full-width wave strip (topbar + player), and the editor rail inside `#lib-detail`.
//
// Deliberately RAW (trusted pre-rendered markup from renderers that own it, inserted
// unescaped exactly where the pre-split code inserted it):
//   - the player strip (player.go mpHTML) - this is the 30 fps `__rt` playhead surface, all
//     of its float math + ids stay Go-side
//   - the write-back rail (library_cuewrite.go ceWriteHTML / library_remotecue.go rceSaveHTML)
//   - the prep-playlist picker (library_prep.go prepSelectHTML)
//   - the cue-edit tooltip (tooltip.go tipTopic)
//
// Floats never cross the ABI: `ce-goto:%f` acts, pubClock readouts and ceBarBeat are
// formatted Go-side and ride as strings (emitted raw, same as Go).

import (
	"fmt"
	"strings"

	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/zigui"
)

// ── state ──

// ceTbDropSt is one clickable drop chip in the topbar (Act carries the Go-formatted ms).
type ceTbDropSt struct {
	Act  string `json:"act"`
	Lbl  string `json:"lbl"`  // ceDropLabel - digits/"X", spliced RAW like Go
	When string `json:"when"` // pubClock, spliced RAW like Go
}

// ceTopbarSt is the resolved `#ce-topbar` readout strip. Show=false = the editor is off and
// both renderers emit "" (an empty fragment makes renderJSON return NULL ⇒ Go fallback).
type ceTopbarSt struct {
	Show    bool   `json:"show"`
	Eyebrow string `json:"eyebrow"`
	Title   string `json:"title"`

	HasRce   bool   `json:"hasRce"` // remote session: explicit flag, never "RceMeta != ''"
	RceMeta  string `json:"rceMeta"`
	Dirty    bool   `json:"dirty"`
	DirtyTip string `json:"dirtyTip"`

	Meta    string `json:"meta"` // "<bpm> BPM · <key>" ("" = neither known, Go's own condition)
	Cursor  string `json:"cursor"`
	BarLbl  string `json:"barLbl"`
	BarBeat string `json:"barBeat"`
	Jump    string `json:"jump"`

	Drops  []ceTbDropSt `json:"drops,omitempty"`
	Census string       `json:"census"`

	NoTag    bool   `json:"noTag"`
	NoTagTip string `json:"noTagTip"`

	Verified    bool   `json:"verified"`
	Verifiable  bool   `json:"verifiable"`
	VerifyAct   string `json:"verifyAct"`
	VerifiedTip string `json:"verifiedTip"`
	VerifiedLbl string `json:"verifiedLbl"`
	VerifyTip   string `json:"verifyTip"`
	VerifyLbl   string `json:"verifyLbl"`
	Tip         string `json:"tip"` // raw tipTopic("cue-edit")
	Close       uiBtn  `json:"close"`
}

// ceWaveSt is the full-width player strip: the topbar + the player markup (raw, player.go).
type ceWaveSt struct {
	Topbar ceTopbarSt `json:"topbar"`
	Player string     `json:"player"`
}

// ceDefaultsSt is the collapsible per-mode defaults block. Open=false = header only (the
// selects are then never registered - the side effect stays where Go had it).
type ceDefaultsSt struct {
	Arrow string   `json:"arrow"` // ▸/▾ literal, spliced raw like Go
	Title string   `json:"title"`
	Open  bool     `json:"open"`
	Pads  selState `json:"pads"`
	Ow    uiToggle `json:"ow"`
	Split uiToggle `json:"split"`

	HasPromote bool     `json:"hasPromote"`
	Promote    uiToggle `json:"promote"`
	HasGrid    bool     `json:"hasGrid"`
	Grid       uiToggle `json:"grid"`
	Note       string   `json:"note"`
}

// ceARowSt is one drop→pattern assign row. Placed=false = the "unplaced" hint variant.
type ceARowSt struct {
	Placed      bool     `json:"placed"`
	Tag         string   `json:"tag"` // "DROP <n>", spliced RAW like Go
	Act         string   `json:"act"`
	When        string   `json:"when"` // pubClock, spliced RAW like Go
	UnplacedTip string   `json:"unplacedTip"`
	UnplacedLbl string   `json:"unplacedLbl"`
	HasSel      bool     `json:"hasSel"` // false = no pattern store ⇒ no picker at all
	Sel         selState `json:"sel"`
}

// ceAssignSt is the assign grid (drops 1-4 + X, plus any extra placed drops).
type ceAssignSt struct {
	Title       string     `json:"title"`
	Rows        []ceARowSt `json:"rows,omitempty"`
	ShowNoDrops bool       `json:"showNoDrops"`
	NoDropsHint string     `json:"noDropsHint"`
}

// ceBatchSt is the checked-rows batch block (hidden in rce mode - a peer session edits one track).
type ceBatchSt struct {
	Show       bool   `json:"show"`
	Header     string `json:"header"`
	ApplyHot   uiBtn  `json:"applyHot"`
	ApplyMem   uiBtn  `json:"applyMem"`
	PromoteSel uiBtn  `json:"promoteSel"`
	ConvertSel uiBtn  `json:"convertSel"`
	ClearSel   uiBtn  `json:"clearSel"`
	Note       string `json:"note"`
}

// ceRailSt is the resolved cue-editor rail (the `#lib-detail` inner in cue-edit mode).
type ceRailSt struct {
	Show    bool   `json:"show"`
	Eyebrow string `json:"eyebrow"`
	Title   string `json:"title"`

	Mode     selState     `json:"mode"`
	Defaults ceDefaultsSt `json:"defaults"`
	PrepSel  string       `json:"prepSel"` // raw (library_prep.go)
	PrepHint string       `json:"prepHint"`
	Assign   ceAssignSt   `json:"assign"`
	AddDrop  uiBtn        `json:"addDrop"`
	DelDrop  uiBtn        `json:"delDrop"`

	HasSel    bool   `json:"hasSel"`
	SelLbl    string `json:"selLbl"`
	PatNamePH string `json:"patNamePh"`
	SavePat   uiBtn  `json:"savePat"`

	HasDSel     bool   `json:"hasDsel"`
	DSelLbl     string `json:"dselLbl"`
	ShowDelHint bool   `json:"showDelHint"`
	DelHint     string `json:"delHint"`

	HasPats bool  `json:"hasPats"`
	Manage  uiBtn `json:"manage"`

	HasDrops   bool   `json:"hasDrops"`
	ApplyHot   uiBtn  `json:"applyHot"`
	ApplyMem   uiBtn  `json:"applyMem"`
	ShowOwNote bool   `json:"showOwNote"`
	OwNote     string `json:"owNote"`

	PromoteAll uiBtn `json:"promoteAll"`
	ConvertAll uiBtn `json:"convertAll"`
	ClearOne   uiBtn `json:"clearOne"`

	Hints []libHintSt `json:"hints,omitempty"`
	Batch ceBatchSt   `json:"batch"`

	WriteBack string `json:"writeBack"` // raw (library_cuewrite.go / library_remotecue.go)
	Close     uiBtn  `json:"close"`
}

// ── state builders (impure) ──

// ceTopbarState resolves the topbar readouts. Locks ceSt.
func (u *UI) ceTopbarState() ceTopbarSt {
	c := u.ce()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return ceTopbarSt{}
	}
	// verified-grid status: gfVerified()/vs.Has() take their OWN package locks (never c.mu),
	// so this is safe under c.mu. rce edits a cached copy - the store is keyed on local paths.
	verified, verifiable := false, false
	if c.rce == nil {
		if vs := u.gfVerified(); vs != nil {
			verified = vs.Has(c.path)
		}
		verifiable = len(c.track.Beatgrid) == 1 && c.track.BPM > 0 // gfToggleVerify needs one marker + a BPM
	}
	st := ceTopbarSt{Show: true, Eyebrow: i18n.T("library.ce.eyebrow"), Title: trackTitle(c.track)}
	if r := c.rce; r != nil { // remote session: whose track + unsaved marker
		st.HasRce = true
		st.RceMeta = i18n.T("library.rce.topbarOn", i18n.A{"name": r.peerName})
		st.Dirty, st.DirtyTip = c.rceDirtyLocked(), i18n.T("library.rce.unsaved")
	}
	meta := ""
	if c.track.BPM > 0 {
		meta = fmt.Sprintf("%.1f BPM", c.track.BPM)
	}
	if k := strings.TrimSpace(c.track.Key); k != "" {
		if meta != "" {
			meta += " · "
		}
		meta += k
	}
	st.Meta = meta
	st.Cursor = pubClock(c.cursorMs / 1000)
	st.BarLbl, st.BarBeat = i18n.T("library.ce.bar"), ceBarBeat(c.grid, c.cursorMs)
	st.Jump = i18n.T("library.ce.jump", i18n.A{"n": fmt.Sprint(int(c.jump))})
	st.Drops = make([]ceTbDropSt, 0, len(c.drops))
	for i, d := range c.drops {
		st.Drops = append(st.Drops, ceTbDropSt{
			Act: fmt.Sprintf("ce-goto:%f", d), Lbl: ceDropLabel(i), When: pubClock(d / 1000)})
	}
	st.Census = i18n.Tn("library.ce.patternCues", ceCueCount(c.track.Cues))
	st.NoTag, st.NoTagTip = !c.fileTag, i18n.T("library.ce.noFileTag")
	// verified-grid chip: mint ✓ when verified (click = unmark), outline "Mark verified" when
	// eligible. Verified locks nudging (ceGridShift), so the chip doubles as the toggle for it.
	st.Verified, st.Verifiable = verified, verifiable
	st.VerifyAct = "gf-verify:" + c.path
	st.VerifiedTip, st.VerifiedLbl = i18n.T("library.ce.verifiedTip"), i18n.T("library.gf.verifiedBadge")
	st.VerifyTip, st.VerifyLbl = i18n.T("library.ce.verifyTip"), i18n.T("library.gf.markVerified")
	st.Tip = tipTopic("cue-edit")
	st.Close = uiBtn{Label: "✕ " + i18n.T("common.close"), Variant: "ghost", Act: "ce-close"}
	return st
}

// ceModeSelState registers + resolves the target-software picker (detected installs badged).
func (u *UI) ceModeSelState(cur string) selState {
	det := map[string]bool{}
	for _, t := range u.ceTargets(false) { // cached - no fs probes per repaint
		det[t.key] = true
	}
	s := resolveSmartSelect("ce-mode", "ce-mode:", cur, func() []ssOpt {
		opts := []ssOpt{{Val: "", Label: i18n.T("library.ce.modeAll"), Sub: i18n.T("library.ce.modeAllSub")}}
		for _, sw := range ceSoftwares {
			o := ssOpt{Val: sw[0], Label: sw[1], Sub: i18n.T("library.ce.modeSwSub", i18n.A{"app": sw[1]})}
			if det[sw[0]] {
				o.Badge = i18n.T("library.ce.modeDetected")
			}
			opts = append(opts, o)
		}
		return opts
	})
	s.Label = i18n.T("library.ce.modeLabel")
	return s
}

// ceDefaultsState resolves the collapsible per-mode defaults. c LOCKED by the caller.
func ceDefaultsState(c *ceSt, mode string, pref ceSWPref) ceDefaultsSt {
	title := i18n.T("library.ce.defaultsAll")
	if mode != "" {
		title = i18n.T("library.ce.defaultsFor", i18n.A{"app": ceSoftwareLabel(mode)})
	}
	arrow := "▸"
	if c.prefsOpen {
		arrow = "▾"
	}
	st := ceDefaultsSt{Arrow: arrow, Title: title, Open: c.prefsOpen, Pads: emptySel()}
	if !c.prefsOpen {
		return st // collapsed: Go registers nothing here either
	}
	padOpts := [][2]string{{"2", "2"}, {"4", "4"}, {"6", "6"}, {"8", "8"}, {"16", "16"}, {"32", "32"}}
	st.Pads = resolveSelectBox(i18n.T("library.ce.prefPads"), "ce-pref-pads", padOpts, fmt.Sprint(pref.MaxPadsOr()))
	st.Ow = newToggle(i18n.T("library.ce.prefOw"), "ce-pref-ow", pref.Overwrite)
	st.Split = newToggle(i18n.T("library.ce.prefSplit"), "ce-pref-split", !pref.NoSplitEven)
	if mode != "" {
		st.HasPromote = true
		st.Promote = newToggle(i18n.T("library.ce.prefPromote", i18n.A{"app": ceSoftwareLabel(mode)}),
			"ce-pref-promote", pref.AutoPromoteOn(mode))
	}
	if mode == "traktor" {
		st.HasGrid = true
		st.Grid = newToggle(i18n.T("library.ce.prefGridAnchor"), "ce-pref-grid", !pref.NoGridAnchor)
	}
	st.Note = i18n.T("library.ce.defaultsNote")
	return st
}

// ceAssignState resolves the drop→pattern assign grid: one row per drop (1-4 + X, plus any
// extra placed drops), each carrying the drop label (click = jump when placed), its position
// (or an "unplaced" hint), and the pattern picker. Assignments persist in c.assign (drop index
// → pattern id) - the same map ceSavePattern auto-fills and ceApply reads.
// c is LOCKED by the caller (ceRailState); never re-lock it here.
func ceAssignState(c *ceSt, st *cuepattern.Store) ceAssignSt {
	rows := ceAssignRows
	if len(c.drops) > rows {
		rows = len(c.drops)
	}
	out := ceAssignSt{Title: i18n.T("library.ce.assignTitle"), Rows: make([]ceARowSt, 0, rows)}
	for i := 0; i < rows; i++ {
		placed := i < len(c.drops)
		r := ceARowSt{Placed: placed, Tag: `DROP ` + ceDropLabel(i)}
		if placed {
			r.Act, r.When = fmt.Sprintf("ce-goto:%f", c.drops[i]), pubClock(c.drops[i]/1000)
		} else {
			r.UnplacedTip, r.UnplacedLbl = i18n.T("library.ce.unplacedTip"), i18n.T("library.ce.unplaced")
		}
		r.Sel = emptySel()
		if st != nil {
			r.HasSel, r.Sel = true, ceAssignSelState(i, c.assign[i], st)
		}
		out.Rows = append(out.Rows, r)
	}
	if len(c.drops) == 0 {
		out.ShowNoDrops, out.NoDropsHint = true, i18n.T("library.ce.noDropsHint")
	}
	return out
}

// ceAssignSelState registers + resolves the per-drop pattern picker. NOTE the quirk kept from
// the pre-split code: `cur` is the pattern NAME (not its id), so no option row ever matches it -
// CurLabel comes straight through and no row is marked current.
func ceAssignSelState(dropIdx int, cur string, st *cuepattern.Store) selState {
	id := fmt.Sprintf("ce-assign-%d", dropIdx)
	curLabel := ""
	if p, ok := st.Get(cur); ok {
		curLabel = p.Name
	}
	return resolveSmartSelect(id, fmt.Sprintf("ce-assign:%d:", dropIdx), curLabel, func() []ssOpt {
		opts := []ssOpt{{Val: "", Label: "—"}}
		for _, p := range st.List() {
			opts = append(opts, ssOpt{Val: p.ID, Label: p.Name,
				Sub: i18n.Tn("library.ce.patternCues", len(p.Cues)), Badge: p.FromTrack})
		}
		return opts
	})
}

// ceRailState resolves the cue-editor rail. s is LOCKED by the caller (libDetailState path) -
// never re-lock it. The ORDER below is load-bearing: the write-back rails + both pickers lock
// ceSt / register smart selects BEFORE c.mu is taken, and the defaults block registers its pad
// select before the pattern store opens - same sequence the pre-split renderer had.
func (u *UI) ceRailState(s *libSt) ceRailSt {
	wb := u.ceWriteHTML(s) // built first - locks ceSt itself (never nested under c.mu)
	if rs := u.rceSaveHTML(); rs != "" {
		wb = rs // rce mode: save-to-peer rail replaces the local write-back router
	}
	mode := u.ceMode()
	pref := u.cePrefFor(mode)
	modeSel := u.ceModeSelState(mode)
	prepSel := u.prepSelectHTML("prep-rail")
	nChecked := len(s.collSel)
	c := u.ce()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return ceRailSt{}
	}
	// controls only - the readouts (cursor, drop times, cue census) live in the
	// ce-topbar on the waveform strip
	eyebrow := i18n.T("library.ce.eyebrow")
	if r := c.rce; r != nil {
		eyebrow = i18n.T("library.rce.eyebrow", i18n.A{"name": r.peerName}) // editing the PEER's track
	}
	st := ceRailSt{Show: true, Eyebrow: eyebrow, Title: trackTitle(c.track),
		// software mode: scopes new cues + apply/promote/write to one DJ app ("" = all)
		Mode: modeSel, Defaults: ceDefaultsState(c, mode, pref),
		// preparation playlist: P adds the open track, holding P removes it again
		PrepSel: prepSel, PrepHint: i18n.T("library.prep.hint"), WriteBack: wb,
		Close: uiBtn{Label: i18n.T("common.close"), Variant: "ghost", Act: "ce-close"}}

	// drops → pattern assign grid (fixed rows drop 1-4 + X; unplaced rows still show)
	pats := u.cePatterns() // ensure the store is open so the pickers render on first use
	st.Assign = ceAssignState(c, pats)
	st.AddDrop = uiBtn{Label: i18n.T("library.ce.addDrop"), Variant: "outline", Act: "ce-drop-add"}
	st.DelDrop = uiBtn{Label: i18n.T("library.ce.removeDrop"), Variant: "ghost", Act: "ce-drop-del"}

	// selection → pattern (cues) / delete (cues + drops)
	nsel, ndsel := 0, 0
	for _, on := range c.sel {
		if on {
			nsel++
		}
	}
	for _, on := range c.dsel {
		if on {
			ndsel++
		}
	}
	if nsel > 0 {
		st.HasSel = true
		st.SelLbl = i18n.T("library.ce.selection", i18n.A{"n": fmt.Sprint(nsel)})
		st.PatNamePH = i18n.T("library.ce.patternName")
		st.SavePat = uiBtn{Label: i18n.T("library.ce.savePattern"), Variant: "outline", Act: "ce-pat-save"}
	}
	if ndsel > 0 {
		st.HasDSel = true
		st.DSelLbl = i18n.T("library.ce.selDrops", i18n.A{"n": fmt.Sprint(ndsel)})
	}
	if nsel+ndsel > 0 {
		st.ShowDelHint, st.DelHint = true, i18n.T("library.ce.delHint")
	}
	if pats != nil && len(pats.List()) > 0 {
		st.HasPats = true
		st.Manage = uiBtn{Label: i18n.T("library.ce.managePatterns"), Variant: "ghost", Act: "ce-pat-manage"}
	}

	// apply
	if len(c.drops) > 0 {
		st.HasDrops = true
		st.ApplyHot = uiBtn{Label: i18n.T("library.ce.applyHot"), Variant: "primary", Act: "ce-apply:hot"}
		st.ApplyMem = uiBtn{Label: i18n.T("library.ce.applyMem"), Variant: "outline", Act: "ce-apply:mem"}
		if pref.Overwrite {
			if n := ceInScopeMusical(c.track.Cues, mode); n > 0 {
				st.ShowOwNote = true
				st.OwNote = i18n.T("library.ce.owNote", i18n.A{"n": fmt.Sprint(n)})
			}
		}
	}
	st.PromoteAll = uiBtn{Label: i18n.T("library.ce.promoteAll"), Variant: "ghost", Act: "ce-promote"}
	st.ConvertAll = uiBtn{Label: i18n.T("library.ce.convertAll"), Variant: "ghost", Act: "ce-convert"}
	st.ClearOne = uiBtn{Label: i18n.T("library.ce.clearOne"), Variant: "ghost", Act: "ce-clear"}
	st.Hints = []libHintSt{}
	if c.report != nil {
		r := c.report
		st.Hints = append(st.Hints, libHintSt{Tone: "ok", Text: i18n.T("library.ce.reportHint", i18n.A{
			"added": fmt.Sprint(r.Added), "cut": fmt.Sprint(r.Cut),
			"skipped": fmt.Sprint(r.Skipped), "demoted": fmt.Sprint(r.Demoted)})})
		if r.Replaced > 0 {
			st.Hints = append(st.Hints, libHintSt{Tone: "info",
				Text: i18n.T("library.ce.replacedHint", i18n.A{"n": fmt.Sprint(r.Replaced)})})
		}
	}
	if c.lastErr != "" {
		st.Hints = append(st.Hints, libHintSt{Tone: "bad", Text: c.lastErr})
	}

	// batch: every action below runs over the CHECKED collection rows
	if nChecked > 0 && c.rce == nil {
		st.Batch = ceBatchSt{Show: true,
			Header:     i18n.T("library.ce.batchHeader", i18n.A{"n": fmt.Sprint(nChecked)}),
			ApplyHot:   uiBtn{Label: i18n.T("library.ce.applySelHot"), Variant: "outline", Act: "ce-apply-sel:hot"},
			ApplyMem:   uiBtn{Label: i18n.T("library.ce.applySelMem"), Variant: "outline", Act: "ce-apply-sel:mem"},
			PromoteSel: uiBtn{Label: i18n.T("library.ce.promoteSel"), Variant: "ghost", Act: "ce-promote-sel"},
			ConvertSel: uiBtn{Label: i18n.T("library.ce.convertSel"), Variant: "ghost", Act: "ce-convert-sel"},
			ClearSel: uiBtn{Label: i18n.T("library.ce.clearSel", i18n.A{"n": fmt.Sprint(nChecked)}),
				Variant: "ghost", Act: "ce-clear-sel"},
			Note: i18n.T("library.ce.batchNote")}
	}
	return st
}

// ── pure renderers (golden reference for cueedit.zig) ──

// ceTopbarHTMLOf renders the readout strip: track identity, cursor position (time +
// bar.beat), jump size, drops (clickable = jump) and cue census.
func ceTopbarHTMLOf(st ceTopbarSt) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=ce-topbar>`)
	b.WriteString(`<span class=ce-tb-eyebrow>` + esc(st.Eyebrow) + `</span>`)
	b.WriteString(`<span class=ce-tb-title>` + esc(st.Title) + `</span>`)
	if st.HasRce {
		b.WriteString(`<span class=ce-tb-meta>` + esc(st.RceMeta) + `</span>`)
		if st.Dirty {
			b.WriteString(`<span class=ce-tb-warn title=` + attrQ(st.DirtyTip) + `>●</span>`)
		}
	}
	if st.Meta != "" {
		b.WriteString(`<span class=ce-tb-meta>` + esc(st.Meta) + `</span>`)
	}
	b.WriteString(`<span class=ce-tb-cursor>▸ ` + st.Cursor + ` · ` + esc(st.BarLbl) + ` ` + st.BarBeat + `</span>`)
	b.WriteString(`<span class=ce-jump>` + esc(st.Jump) + `</span>`)
	for _, d := range st.Drops {
		b.WriteString(`<span class=ce-tb-drop data-act=` + attrQ(d.Act) + `>D` + d.Lbl + ` ` + d.When + `</span>`)
	}
	b.WriteString(`<span class=ce-tb-meta>` + esc(st.Census) + `</span>`)
	if st.NoTag {
		b.WriteString(`<span class=ce-tb-warn title=` + attrQ(st.NoTagTip) + `>⚠</span>`)
	}
	switch {
	case st.Verified:
		b.WriteString(`<span class=ce-tb-verified title=` + attrQ(st.VerifiedTip) +
			` data-act=` + attrQ(st.VerifyAct) + `>✓ ` + esc(st.VerifiedLbl) + `</span>`)
	case st.Verifiable:
		b.WriteString(`<span class=ce-tb-verify title=` + attrQ(st.VerifyTip) +
			` data-act=` + attrQ(st.VerifyAct) + `>` + esc(st.VerifyLbl) + `</span>`)
	}
	b.WriteString(`<span class=ce-tb-spacer></span>` + st.Tip + st.Close.html())
	b.WriteString(`</div>`)
	return b.String()
}

// ceWaveHTMLOf frames the topbar + the player strip (raw, player.go).
func ceWaveHTMLOf(st ceWaveSt) string {
	return `<div id=ce-topbar>` + ceTopbarHTMLOf(st.Topbar) + `</div>` + st.Player
}

// ceDefaultsHTMLOf renders the collapsible per-mode defaults.
func ceDefaultsHTMLOf(st ceDefaultsSt) string {
	var b strings.Builder
	b.WriteString(`<div class="pb-label ce-prefs-hd" data-act=ce-prefs-tgl>` + st.Arrow + ` ` + esc(st.Title) + `</div>`)
	if !st.Open {
		return b.String()
	}
	b.WriteString(selHTML(st.Pads))
	b.WriteString(st.Ow.html())
	b.WriteString(st.Split.html())
	if st.HasPromote {
		b.WriteString(st.Promote.html())
	}
	if st.HasGrid {
		b.WriteString(st.Grid.html())
	}
	b.WriteString(`<div class=set-note>` + esc(st.Note) + `</div>`)
	return b.String()
}

// ceAssignGridHTMLOf renders the compact drop→pattern assign grid.
func ceAssignGridHTMLOf(st ceAssignSt) string {
	var b strings.Builder
	b.WriteString(`<div class=pb-label>` + esc(st.Title) + `</div>`)
	b.WriteString(`<div class=ce-agrid>`)
	for _, r := range st.Rows {
		cls := "ce-arow"
		if !r.Placed {
			cls += " unplaced"
		}
		b.WriteString(`<div class="` + cls + `">`)
		if r.Placed {
			b.WriteString(`<span class=ce-arow-tag data-act=` + attrQ(r.Act) + `>` + r.Tag + `</span>`)
			b.WriteString(`<span class=ce-arow-when>` + r.When + `</span>`)
		} else {
			b.WriteString(`<span class=ce-arow-tag>` + r.Tag + `</span>`)
			b.WriteString(`<span class="ce-arow-when unplaced" title=` + attrQ(r.UnplacedTip) +
				`>` + esc(r.UnplacedLbl) + `</span>`)
		}
		if r.HasSel {
			b.WriteString(selHTML(r.Sel))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	if st.ShowNoDrops {
		b.WriteString(`<div class=set-note>` + esc(st.NoDropsHint) + `</div>`)
	}
	return b.String()
}

// ceRailHTMLOf renders the cue-editor card in the library detail rail.
func ceRailHTMLOf(st ceRailSt) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(st.Eyebrow) + `</div><div class=insp-title>` +
		esc(st.Title) + `</div></div>`)
	b.WriteString(selHTML(st.Mode))
	b.WriteString(ceDefaultsHTMLOf(st.Defaults))
	b.WriteString(st.PrepSel)
	b.WriteString(`<div class=set-note>` + esc(st.PrepHint) + `</div>`)
	b.WriteString(ceAssignGridHTMLOf(st.Assign))
	b.WriteString(btnRow(st.AddDrop.html(), st.DelDrop.html()))
	if st.HasSel {
		b.WriteString(`<div class=pb-label>` + esc(st.SelLbl) + `</div>`)
		b.WriteString(`<div class=lib-toolbar>` + fieldRaw("ce-pat-name", "", st.PatNamePH) +
			st.SavePat.html() + `</div>`)
	}
	if st.HasDSel {
		b.WriteString(`<div class=pb-label>` + esc(st.DSelLbl) + `</div>`)
	}
	if st.ShowDelHint {
		b.WriteString(`<div class=set-note>` + esc(st.DelHint) + `</div>`)
	}
	if st.HasPats {
		b.WriteString(btnRow(st.Manage.html()))
	}
	if st.HasDrops {
		b.WriteString(`<div class=btn-col>` + st.ApplyHot.html() + st.ApplyMem.html() + `</div>`)
		if st.ShowOwNote {
			b.WriteString(`<div class=set-note>` + esc(st.OwNote) + `</div>`)
		}
	}
	b.WriteString(btnRow(st.PromoteAll.html(), st.ConvertAll.html()))
	b.WriteString(btnRow(st.ClearOne.html()))
	b.WriteString(libHintsHTML(st.Hints))
	if st.Batch.Show {
		b.WriteString(`<div class=pb-label>` + esc(st.Batch.Header) + `</div>`)
		b.WriteString(`<div class=btn-col>` + st.Batch.ApplyHot.html() + st.Batch.ApplyMem.html() + `</div>`)
		b.WriteString(btnRow(st.Batch.PromoteSel.html(), st.Batch.ConvertSel.html()))
		b.WriteString(btnRow(st.Batch.ClearSel.html()))
		b.WriteString(`<div class=set-note>` + esc(st.Batch.Note) + `</div>`)
	}
	b.WriteString(st.WriteBack)
	b.WriteString(btnRow(st.Close.html()))
	return b.String()
}

// ── bridges (Zig when available, Go renderer as fallback + golden reference) ──

// ceTopbarRender renders the topbar via Zig when available.
func ceTopbarRender(st ceTopbarSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderCueEditTopbar(stateJSON(st)); ok {
			return h
		}
	}
	return ceTopbarHTMLOf(st)
}

// ceWaveRender renders the full-width strip via Zig when available.
func ceWaveRender(st ceWaveSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderCueEditWave(stateJSON(st)); ok {
			return h
		}
	}
	return ceWaveHTMLOf(st)
}

// ceRailRender renders the editor rail via Zig when available.
func ceRailRender(st ceRailSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderCueEditRail(stateJSON(st)); ok {
			return h
		}
	}
	return ceRailHTMLOf(st)
}
