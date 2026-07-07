package webui

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
)

func (u *UI) pubSelID() string {
	pubStateMu.Lock()
	defer pubStateMu.Unlock()
	return pubSelIDv
}

func (u *UI) pubSetSel(id string) {
	pubStateMu.Lock()
	pubSelIDv = id
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
		u.eval("window.__patch('pub-hero'," + jsQuote(u.pubHeroHTML()) + ")")
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

	// capture file ops
	onPrefix("pub-reveal:", func(u *UI, m actMsg) { u.pubOpenCap(m.arg("pub-reveal:"), true) })
	onPrefix("pub-open:", func(u *UI, m actMsg) { u.pubOpenCap(m.arg("pub-open:"), false) })
	onPrefix("pub-capdel:", func(u *UI, m actMsg) { u.pubCapDelOpen(m.arg("pub-capdel:")) })
	onPrefix("pub-capdel-do:", func(u *UI, m actMsg) { u.pubCapDel(m.arg("pub-capdel-do:")) })

	// set ops
	onPrefix("pub-export:", func(u *UI, m actMsg) { u.pubExportOpen(m.arg("pub-export:")) })
	onPrefix("pub-exportfmt:", func(u *UI, m actMsg) { u.pubExportFmt(m.arg("pub-exportfmt:")) })
	onPrefix("pub-match:", func(u *UI, m actMsg) {
		if u.libRemoteTarget() != "" {
			u.pubRemoteMatch(m.arg("pub-match:"))
			return
		}
		u.pubMatch(m.arg("pub-match:"))
	})
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
	body := `<div class=np-artist>Remove the capture "` + html.EscapeString(filepath.Base(s.Path)) + `" from the library?</div>`
	footer := btnRow(
		btn("Remove", "outline", "pub-capdel-do:"+capID, ""),
		btn("Remove + delete file", "destructive", "pub-capdel-do:"+capID+"\x1ffiles", ""),
		btn("Cancel", "ghost", "modal-close", ""),
	)
	u.openModal(modal("Remove capture", body, footer))
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
	body := `<div class=np-artist>Choose a format for the tracklist export.</div>` + btnRow(
		btn("Text (.txt)", "primary", "pub-exportfmt:"+id+"\x1ftxt", ""),
		btn("CSV (.csv)", "outline", "pub-exportfmt:"+id+"\x1fcsv", ""),
		btn("JSON (.json)", "outline", "pub-exportfmt:"+id+"\x1fjson", ""),
	)
	u.openModal(modal("Export tracklist", body, ""))
}

// pubExportFmt renders the exported tracklist in the modal (preview + copy). Native file-save is a
// follow-up (pickers stub); the preview is fully selectable/copyable in the meantime.
func (u *UI) pubExportFmt(arg string) {
	id, fmtKey, _ := strings.Cut(arg, "\x1f")
	if tgt := u.libRemoteTarget(); tgt != "" {
		u.pubRemoteExport(id, fmtKey)
		return
	}
	if u.svc.Recorder == nil {
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
	body := `<div class=np-artist>Select all + copy, or use Copy below.</div>` +
		`<textarea class=pub-export-ta readonly rows=14>` + html.EscapeString(content) + `</textarea>`
	footer := `<button class="rp-btn rp-btn--primary" data-act="copy" data-val="` + html.EscapeString(content) + `">Copy</button>` +
		btn("Close", "outline", "modal-close", "")
	return modal("Export - "+fmtKey, body, footer)
}

func (u *UI) pubMatch(id string) {
	if u.svc.Recorder == nil {
		return
	}
	histDir := u.pubHistoryDir()
	if histDir == "" {
		u.toast("No Traktor History folder found - can't match.")
		return
	}
	u.toast("Matching to Traktor history…")
	u.bg(func() {
		rec, err := u.svc.Recorder.ReconcileWithHistory(id, histDir, u.pubHistoryResolver())
		if err != nil {
			u.logErr("reconcile", err)
			u.toast("Match failed: " + err.Error())
			return
		}
		u.toast(fmt.Sprintf("Matched %d tracks from Traktor history.", len(rec.Tracks)))
		u.patchMain()
	})
}

func (u *UI) pubDelOpen(id string) {
	name := "Live set"
	if u.svc.Recorder != nil {
		if r, ok := u.svc.Recorder.Get(id); ok {
			name = orSetName(r.Name)
		}
	}
	caps, _ := u.pubCaptures()
	n := len(caps[id])
	body := `<div class=np-artist>Delete "` + html.EscapeString(name) + `"? This can't be undone.</div>`
	btns := []string{btn("Delete set", "destructive", "pub-del-do:"+id, "")}
	if n > 0 {
		btns = append(btns, btn(fmt.Sprintf("Delete set + %d file(s)", n), "destructive", "pub-del-do:"+id+"\x1ffiles", ""))
	}
	btns = append(btns, btn("Cancel", "ghost", "modal-close", ""))
	u.openModal(modal("Delete set", body, btnRow(btns...)))
}

func (u *UI) pubDel(arg string) {
	id, rest, _ := strings.Cut(arg, "\x1f")
	if u.svc.Recorder == nil {
		u.closeModal()
		return
	}
	alsoFiles := rest == "files"
	caps, _ := u.pubCaptures()
	sets := caps[id]
	u.logErr("delete recording", u.svc.Recorder.Delete(id))
	for _, s := range sets {
		if u.svc.Lib == nil {
			continue
		}
		if alsoFiles {
			if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
				u.toast("Couldn't delete " + filepath.Base(s.Path) + ": " + err.Error())
			}
			u.logErr("delete capture", u.svc.Lib.DeleteSetRecording(s.ID))
		} else {
			u.logErr("unlink capture", u.svc.Lib.RelinkSetRecording(s.ID, "")) // keep file, drop dead link
		}
	}
	u.pubSetSel("")
	u.closeModal()
	u.patchMain()
	u.toast("Set deleted")
}

// ── lookups ───────────────────────────────────────────────────────────────────────

func (u *UI) pubCapByID(id string) (libdb.SetRecording, bool) {
	if u.svc.Lib == nil {
		return libdb.SetRecording{}, false
	}
	all, err := u.svc.Lib.ListSetRecordings(300)
	if err != nil {
		return libdb.SetRecording{}, false
	}
	for _, s := range all {
		if s.ID == id {
			return s, true
		}
	}
	return libdb.SetRecording{}, false
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
