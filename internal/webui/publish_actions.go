package webui

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/session/sinks/recorder"
)

// Publish-tab action handlers + live tick. All actions are `pub-`-namespaced; the Finish-set
// button reuses the already-wired core `rec-finish`. Selection state (which set + which subtab)
// is package-level - the webview UI is a single instance - guarded by pubStateMu.

var (
	pubStateMu sync.Mutex
	pubSelIDv  string
	pubSubtabv = "captures"
	pubTSelv   = map[string]bool{} // works-together selection over resolved tracklist paths
)

func (u *UI) pubSelID() string {
	pubStateMu.Lock()
	defer pubStateMu.Unlock()
	return pubSelIDv
}

func (u *UI) pubSetSel(id string) {
	pubStateMu.Lock()
	if id != pubSelIDv {
		pubTSelv = map[string]bool{} // selection is per set
	}
	pubSelIDv = id
	pubStateMu.Unlock()
}

// pubTSel returns a copy of the tracklist works-together selection.
func (u *UI) pubTSel() map[string]bool {
	pubStateMu.Lock()
	defer pubStateMu.Unlock()
	out := make(map[string]bool, len(pubTSelv))
	for p := range pubTSelv {
		out[p] = true
	}
	return out
}

func (u *UI) pubTSelToggle(path string) {
	pubStateMu.Lock()
	if pubTSelv[path] {
		delete(pubTSelv, path)
	} else {
		pubTSelv[path] = true
	}
	pubStateMu.Unlock()
}

func (u *UI) pubTSelClear() {
	pubStateMu.Lock()
	pubTSelv = map[string]bool{}
	pubStateMu.Unlock()
}

func (u *UI) pubSubtab() string {
	pubStateMu.Lock()
	defer pubStateMu.Unlock()
	if pubSubtabv == "" {
		return "captures"
	}
	return pubSubtabv
}

func (u *UI) pubSetSubtab(v string) {
	pubStateMu.Lock()
	pubSubtabv = v
	pubStateMu.Unlock()
}

func init() {
	onLiveTick("publish", func(u *UI) {
		if u.libRemoteTarget() != "" {
			return // remote view has no live hero/player - status stays on the controlled box
		}
		u.eval("window.__patch('pub-hero'," + jsQuote(u.publishHeroHTML()) + ")")
		mpTick(u, "publish") // unified player clock/playhead
	})

	onPrefix("pub-select:", func(u *UI, m actMsg) {
		if u.libRemoteTarget() != "" {
			u.pubRemoteSelect(m.arg("pub-select:"))
			return
		}
		u.pubSetSel(m.arg("pub-select:"))
		u.mpMut("publish", func(t *mpSt) { t.pinned = false }) // release a loose-capture pin
		u.patchMain()
	})
	onPrefix("pub-tab:", func(u *UI, m actMsg) { u.pubSetSubtab(m.arg("pub-tab:")); u.patchMain() })

	// tracklist works-together marking (paths resolved to the library; store = libdb track_compat)
	onPrefix("pub-tsel:", func(u *UI, m actMsg) { u.pubTSelToggle(m.arg("pub-tsel:")); u.patchMain() })
	onExact("pub-tsel-clear", func(u *UI, m actMsg) { u.pubTSelClear(); u.patchMain() })
	onPrefix("pub-tctx:", func(u *UI, m actMsg) { u.pubTrackCtxModal(m.arg("pub-tctx:")) })

	// capture file ops
	onPrefix("pub-reveal:", func(u *UI, m actMsg) { u.pubOpenCap(m.arg("pub-reveal:"), true) })
	onPrefix("pub-open:", func(u *UI, m actMsg) { u.pubOpenCap(m.arg("pub-open:"), false) })
	onPrefix("pub-capdel:", func(u *UI, m actMsg) { u.pubCapDelOpen(m.arg("pub-capdel:")) })
	onPrefix("pub-capdel-do:", func(u *UI, m actMsg) { u.pubCapDel(m.arg("pub-capdel-do:")) })

	// set ops
	onPrefix("pub-rename:", func(u *UI, m actMsg) { u.pubRenameOpen(m.arg("pub-rename:")) })
	onExact("pub-rename-do", func(u *UI, m actMsg) { u.pubRename(parseForm(m.Form)) })
	onPrefix("pub-export:", func(u *UI, m actMsg) { u.pubExportOpen(m.arg("pub-export:")) })
	onPrefix("pub-exportfmt:", func(u *UI, m actMsg) { u.pubExportFmt(m.arg("pub-exportfmt:")) })
	onPrefix("pub-export-save:", func(u *UI, m actMsg) { // pick-save target: val = chosen path
		if m.Val != "" {
			u.pubExportSave(m.arg("pub-export-save:"), m.Val)
		}
	})
	onPrefix("pub-match:", func(u *UI, m actMsg) {
		if u.libRemoteTarget() != "" {
			u.pubRemoteMatch(m.arg("pub-match:"))
			return
		}
		u.pubMatch(m.arg("pub-match:"), false)
	})
	// full-session variant: crash-truncated set - take the history through its end
	onPrefix("pub-match-full:", func(u *UI, m actMsg) { u.pubMatch(m.arg("pub-match-full:"), true) })
	onPrefix("pub-del:", func(u *UI, m actMsg) {
		if u.libRemoteTarget() != "" {
			u.pubRemoteDelOpen(m.arg("pub-del:"))
			return
		}
		u.pubDelOpen(m.arg("pub-del:"))
	})
	onPrefix("pub-del-do:", func(u *UI, m actMsg) {
		if u.libRemoteTarget() != "" {
			id, _, _ := strings.Cut(m.arg("pub-del-do:"), "\x1f")
			u.pubRemoteDel(id)
			return
		}
		u.pubDel(m.arg("pub-del-do:"))
	})
}

// pubTrackCtxModal: right-click on a resolved tracklist row - mark the selection /
// discover compatible tracks (same store + flows as the Collection).
func (u *UI) pubTrackCtxModal(path string) {
	u.openModal(dlgChoiceHTML(dlgChoiceSt{
		Title: filepath.Base(path), InBody: true, Btns: u.pubCompatBtns(path),
	}))
}

// pubCompatBtns resolves the works-together buttons a tracklist row context menu offers for a
// library-resolved path: mark the selection, discover compatible tracks, copy the path. Shared
// by both context menus (pubTrackCtxModal, pubTrackCtx2).
func (u *UI) pubCompatBtns(path string) []uiBtn {
	sel := u.pubTSel()
	row := make([]uiBtn, 0, 3)
	if sel[path] && len(sel) >= 2 {
		row = append(row, uiBtn{Label: i18n.T("library.compat.ctxMark", i18n.A{"count": fmt.Sprint(len(sel))}),
			Variant: "primary", Act: "lib-compat-mark:pub"})
	}
	return append(row,
		uiBtn{Label: i18n.T("library.compat.findBtn"), Variant: "outline", Act: "lib-compat-find:" + path},
		uiBtn{Label: i18n.T("library.copyPath"), Variant: "ghost", Act: "copy", Val: path})
}

// ── capture file ops ──────────────────────────────────────────────────────────────

// pubOpenCap opens a capture's containing folder (reveal) or the file itself in the OS handler.
func (u *UI) pubOpenCap(capID string, reveal bool) {
	s, ok := u.pubCapByID(capID)
	if !ok {
		return
	}
	target := s.Path
	if reveal {
		target = filepath.Dir(s.Path)
	}
	u.logErr("pub open", openURL(target))
}

func (u *UI) pubCapDelOpen(capID string) {
	s, ok := u.pubCapByID(capID)
	if !ok {
		return
	}
	// The message literal quotes an ALREADY-ESCAPED file name, so it rides raw (MsgRaw) -
	// escaping the whole line would turn its quotes into &#34; and change the DOM.
	u.openModal(dlgChoiceHTML(dlgChoiceSt{
		Title:  "Remove capture",
		HasMsg: true, MsgRaw: true,
		Msg: `Remove the capture "` + html.EscapeString(filepath.Base(s.Path)) + `" from the library?`,
		Btns: []uiBtn{
			{Label: "Remove", Variant: "outline", Act: "pub-capdel-do:" + capID},
			{Label: "Remove + delete file", Variant: "destructive", Act: "pub-capdel-do:" + capID + "\x1ffiles"},
			{Label: "Cancel", Variant: "ghost", Act: "modal-close"},
		},
	}))
}

func (u *UI) pubCapDel(arg string) {
	id, rest, _ := strings.Cut(arg, "\x1f")
	s, ok := u.pubCapByID(id)
	if !ok {
		u.closeModal()
		return
	}
	if rest == "files" {
		if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
			u.toast("Couldn't delete file: " + err.Error())
		}
	}
	if u.svc.Lib != nil {
		u.logErr("delete capture", u.svc.Lib.DeleteSetRecording(id))
	}
	u.closeModal()
	u.patchMain()
	u.toast("Capture removed")
}

// ── set ops: export / match / delete ──────────────────────────────────────────────

func (u *UI) pubExportOpen(id string) {
	// Two ways out per format: preview (select/copy) or straight to a file through the native
	// save picker (pick-save re-dispatches pub-export-save with the chosen path as its value).
	btns := []uiBtn{
		{Label: "Text (.txt)", Variant: "primary", Act: "pub-exportfmt:" + id + "\x1ftxt"},
		{Label: "CSV (.csv)", Variant: "outline", Act: "pub-exportfmt:" + id + "\x1fcsv"},
		{Label: "JSON (.json)", Variant: "outline", Act: "pub-exportfmt:" + id + "\x1fjson"},
	}
	if !u.virtual() { // a native dialog would open on the CONTROLLED box in remote mode
		for _, f := range []string{recorder.FormatText, recorder.FormatCSV, recorder.FormatJSON} {
			btns = append(btns, uiBtn{
				Label:   i18n.T("publish.saveAs", i18n.A{"ext": "." + f}),
				Variant: "ghost",
				Act:     "pick-save:" + f + ":pub-export-save:" + id + "\x1f" + f,
			})
		}
	}
	u.openModal(dlgChoiceHTML(dlgChoiceSt{
		Title:  "Export tracklist",
		HasMsg: true, MsgRaw: true, Msg: "Choose a format for the tracklist export.",
		InBody: true, // buttons live in the body; the footer stays Go's default Close
		Btns:   btns,
	}))
}

// pubExportSave writes the tracklist straight to the picked path. arg = "<id>\x1f<format>".
func (u *UI) pubExportSave(arg, out string) {
	id, fmtKey, _ := strings.Cut(arg, "\x1f")
	if u.svc.Recorder == nil || id == "" || out == "" {
		return
	}
	content, err := u.svc.Recorder.Export(id, fmtKey)
	if err != nil {
		u.toast(i18n.T("publish.saveFailed", i18n.A{"error": err.Error()}))
		return
	}
	u.closeModal()
	u.bg(func() { // a network/slow volume must not stall the act lane
		if werr := os.WriteFile(out, []byte(content), 0o644); werr != nil {
			u.toast(i18n.T("publish.saveFailed", i18n.A{"error": werr.Error()}))
			return
		}
		u.toast(i18n.T("publish.saveDone", i18n.A{"name": filepath.Base(out)}))
	})
}

// pubExportFmt renders the exported tracklist in the modal (preview + copy). Text gets the
// style dialog (pubTxtOpen); CSV/JSON stay direct. Native file-save is a follow-up (pickers
// stub); the preview is fully selectable/copyable in the meantime.
func (u *UI) pubExportFmt(arg string) {
	id, fmtKey, _ := strings.Cut(arg, "\x1f")
	if tgt := u.libRemoteTarget(); tgt != "" {
		u.pubRemoteExport(id, fmtKey)
		return
	}
	if u.svc.Recorder == nil {
		return
	}
	if fmtKey == recorder.FormatText {
		u.pubTxtOpen(id)
		return
	}
	content, err := u.svc.Recorder.Export(id, fmtKey)
	if err != nil {
		u.toast("Export failed: " + err.Error())
		return
	}
	u.openModal(pubExportModal(fmtKey, content))
}

// pubExportModal renders the tracklist-export preview dialog (copyable textarea). Shared by the
// local and remote (peer-driven) export flows.
func pubExportModal(fmtKey, content string) string {
	return pubExpDlgHTML(pubExportState(fmtKey, content))
}

func (u *UI) pubMatch(id string, full bool) {
	if u.svc.Recorder == nil {
		return
	}
	// pubHistoryDir → DiscoverTraktor is a filesystem scan of Traktor install dirs; run it (and the
	// reconcile) off the act lane so a slow/spun-down disk can't freeze the UI.
	u.bg(func() {
		histDir := u.pubHistoryDir()
		if histDir == "" {
			u.toast("No Traktor History folder found - can't match.")
			return
		}
		u.toast("Matching to Traktor history…")
		match := u.svc.Recorder.ReconcileWithHistory
		if full {
			match = u.svc.Recorder.ReconcileWithHistoryFull
		}
		rec, err := match(id, histDir, u.pubHistoryResolver())
		if err != nil {
			u.logErr("reconcile", err)
			u.toast("Match failed: " + err.Error())
			return
		}
		u.toast(fmt.Sprintf("Matched %d tracks from Traktor history.", len(rec.Tracks)))
		u.patchMain()
	})
}

// pubRenameOpen prefills the set's current name from the epoch-keyed cache + the live Active()
// (pubRecordings) - Recorder.Get would be a bbolt read on the act lane, and the list this button
// lives in already rendered from that cache. An untitled set opens blank, not with its placeholder.
func (u *UI) pubRenameOpen(id string) {
	if u.svc.Recorder == nil {
		return
	}
	var cur string
	for _, r := range u.pubRecordings() {
		if r.ID == id {
			cur = r.Name
			break
		}
	}
	lbl := i18n.T("publish.setName")
	u.openModal(pubRenameDlgHTML(pubRenameDlgSt{
		Title: i18n.T("publish.renameSet"), ID: id,
		NameLbl: lbl, NameDL: strings.ToLower(lbl), Cur: cur,
		Submit: i18n.T("publish.renameSet"),
	}))
}

func (u *UI) pubRename(f map[string]string) {
	u.closeModal()
	id, name := f["id"], strings.TrimSpace(f["name"])
	if id == "" || name == "" || u.svc.Recorder == nil {
		return
	}
	// Rename resolves across active / persist queue / store, and the non-active path drainPersist()s
	// (fsync) before its read-modify-write - never on the act lane. It bumps recVer, so the render
	// below reloads pubRecList (epoch-keyed) with the new name.
	u.bg(func() {
		if err := u.svc.Recorder.Rename(id, name); err != nil {
			u.logErr("rename recording", err)
			if !u.stopped() {
				u.toast(i18n.T("publish.toast.renameFailed") + err.Error())
			}
			return
		}
		if !u.stopped() {
			u.toast(i18n.T("publish.toast.renamed"))
			u.patchMain()
		}
	})
}

func (u *UI) pubDelOpen(id string) {
	name := i18n.T("publish.liveSet")
	if u.svc.Recorder != nil {
		if r, ok := u.svc.Recorder.Get(id); ok {
			name = orSetName(r.Name)
		}
	}
	caps, _ := u.pubCaptures()
	n := len(caps[id])
	btns := []uiBtn{{Label: i18n.T("publish.del.do"), Variant: "destructive", Act: "pub-del-do:" + id}}
	if n > 0 {
		btns = append(btns, uiBtn{Label: i18n.T("publish.del.doFiles", i18n.A{"n": strconv.Itoa(n)}),
			Variant: "destructive", Act: "pub-del-do:" + id + "\x1ffiles"})
	}
	btns = append(btns, uiBtn{Label: i18n.T("actions.cancel"), Variant: "ghost", Act: "modal-close"})
	u.openModal(dlgChoiceHTML(dlgChoiceSt{
		Title:  i18n.T("publish.del.title"),
		HasMsg: true, Msg: i18n.T("publish.del.confirm", i18n.A{"name": name}),
		Btns: btns,
	}))
}

// pubDel removes a set + reconciles its captures. Recorder.Delete takes storeMu and drainPersist()es
// (an fsync) - and storeMu can be held for hundreds of ms by an auto-reconcile's per-track resolve -
// while the capture cleanup is SQLite writes + os.Remove. None of that may run on the act lane, so
// the whole I/O body goes to u.bg; same split as pubRename. Selection/modal/paint stay on the lane so
// the row disappears on click rather than after the fsync.
func (u *UI) pubDel(arg string) {
	id, rest, _ := strings.Cut(arg, "\x1f")
	if u.svc.Recorder == nil {
		u.closeModal()
		return
	}
	alsoFiles := rest == "files"
	caps, _ := u.pubCaptures() // epoch-keyed cache: no I/O on the act lane
	sets := caps[id]
	u.pubSetSel("")
	u.closeModal()
	u.patchMain()
	u.bg(func() {
		if err := u.svc.Recorder.Delete(id); err != nil {
			u.logErr("delete recording", err)
			if !u.stopped() {
				u.toast(i18n.T("publish.toast.deleteFailed", i18n.A{"err": err.Error()}))
				u.patchMain() // nothing was deleted - put the row back
			}
			return
		}
		for _, s := range sets {
			if u.svc.Lib == nil {
				continue
			}
			if alsoFiles {
				if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) && !u.stopped() {
					u.toast(i18n.T("publish.toast.fileDeleteFailed", i18n.A{"file": filepath.Base(s.Path), "err": err.Error()}))
				}
				u.logErr("delete capture", u.svc.Lib.DeleteSetRecording(s.ID))
			} else {
				u.logErr("unlink capture", u.svc.Lib.RelinkSetRecording(s.ID, "")) // keep file, drop dead link
			}
		}
		if !u.stopped() {
			u.toast(i18n.T("publish.toast.deleted"))
			u.patchMain() // Delete bumped recVer → pubRecList reloads without the row
		}
	})
}

// ── lookups ───────────────────────────────────────────────────────────────────────

// pubCapByID looks a capture up from the shared epoch-keyed cache (pubCapList) - no per-click libdb
// read on the serialized act lane. The set list rendered first, so the cache is warm by the time a
// capture's ⋯ menu / player load can be clicked.
func (u *UI) pubCapByID(id string) (libdb.SetRecording, bool) {
	all, _ := u.pubCapList()
	for _, s := range all {
		if s.ID == id {
			return s, true
		}
	}
	return libdb.SetRecording{}, false
}

// capByPath resolves a media path to its set-recording row + parent recording (zero
// Recording when the row has none). loaded=false while the capture cache is still cold.
func (u *UI) capByPath(path string) (s libdb.SetRecording, r recorder.Recording, ok, loaded bool) {
	all, loaded := u.pubCapList()
	for i := range all {
		if all[i].Path != path && !strings.EqualFold(filepath.Clean(all[i].Path), filepath.Clean(path)) {
			continue
		}
		s, ok = all[i], true
		if u.svc.Recorder != nil && s.RecordingID != "" {
			if rr, rok := u.svc.Recorder.Get(s.RecordingID); rok {
				r = rr
			}
		}
		return s, r, ok, loaded
	}
	return libdb.SetRecording{}, recorder.Recording{}, false, loaded
}

// pubHistoryDir resolves the Traktor History folder (config override, else newest install).
func (u *UI) pubHistoryDir() string {
	if u.svc.Cfg != nil && u.svc.Cfg.Features.NML.HistoryDir != "" {
		return u.svc.Cfg.Features.NML.HistoryDir
	}
	if ins, err := musiclib.DiscoverTraktor(); err == nil && len(ins) > 0 {
		return ins[0].HistoryDir
	}
	return ""
}

// pubHistoryResolver bridges the library DB (path → collection metadata) for reconciliation.
func (u *UI) pubHistoryResolver() recorder.HistoryResolver {
	return func(path string) (recorder.HistoryMeta, bool) {
		if u.svc.Lib == nil {
			return recorder.HistoryMeta{}, false
		}
		t, ok, _ := u.svc.Lib.TrackByPath(path)
		if !ok {
			return recorder.HistoryMeta{}, false
		}
		return recorder.HistoryMeta{Title: t.Title, Artist: t.Artist, Album: t.Album, Key: t.Key, BPM: t.BPM}, true
	}
}
