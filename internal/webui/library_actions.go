package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/idmark"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/maintenance"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/rekordboxdb"
	"rave.page/mate/internal/tagsync"
	"rave.page/mate/internal/tagwrite"
	"rave.page/mate/internal/transcode"
)

// Library action handlers (registry-wired in init). lib-section:/lib-nav: stay in ui.go's switch;
// everything else self-registers here so parallel tab work never collides on a central switch.
func init() {
	// browse
	onPrefix("lib-view:", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.view = m.arg("lib-view:") }) })
	onPrefix("lib-kind:", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.kindFilter = m.arg("lib-kind:") }) })
	onPrefix("lib-sort:", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.sortBy = m.arg("lib-sort:") }) })
	onExact("lib-search", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.nameFilter = m.Val }) })
	onExact("lib-pin", func(u *UI, m actMsg) { u.libPin() })
	onPrefix("lib-unpin:", func(u *UI, m actMsg) { u.libUnpin(m.arg("lib-unpin:")) })
	onExact("lib-nav-to", func(u *UI, m actMsg) { // pick-dir target: browse to picked folder
		if m.Val != "" {
			u.libNav(m.Val)
		}
	})
	onPrefix("lib-open:", func(u *UI, m actMsg) { u.libSelect(m.arg("lib-open:"), nil) })
	onPrefix("lib-track:", func(u *UI, m actMsg) { u.libSelect(m.arg("lib-track:"), nil) })
	onPrefix("lib-batch:", func(u *UI, m actMsg) {
		u.libToggle(func(s *libSt) map[string]bool { return s.batch }, m.arg("lib-batch:"), m.Val == "true")
	})
	onExact("lib-batch-clear", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.batch = map[string]bool{} }) })
	onPrefix("lib-batch-run:", func(u *UI, m actMsg) { u.libBatchRun(m.arg("lib-batch-run:")) })
	onPrefix("lib-batch-tc:", func(u *UI, m actMsg) { u.libBatchTranscode(m.arg("lib-batch-tc:")) })
	onPrefix("lib-ctx:", func(u *UI, m actMsg) { u.libCtxModal(m.arg("lib-ctx:")) })
	onPrefix("lib-reveal:", func(u *UI, m actMsg) { _ = openURL(filepath.Dir(m.arg("lib-reveal:"))) })
	onPrefix("lib-openext:", func(u *UI, m actMsg) { _ = openURL(m.arg("lib-openext:")) })
	onPrefix("lib-probe:", func(u *UI, m actMsg) { u.libProbeToast(m.arg("lib-probe:")) })
	onPrefix("lib-rename:", func(u *UI, m actMsg) { u.libRenameModal(m.arg("lib-rename:")) })
	onExact("lib-rename-do", func(u *UI, m actMsg) { u.libFileRename(parseForm(m.Form)) })
	onPrefix("lib-move:", func(u *UI, m actMsg) { u.libMoveModal(m.arg("lib-move:")) })
	onExact("lib-move-dir", func(u *UI, m actMsg) { u.libMoveDir(m.Val) }) // typed or pick-dir'd destination
	onExact("lib-move-go", func(u *UI, m actMsg) { u.libFileMove() })
	onPrefix("lib-del:", func(u *UI, m actMsg) { u.libDelModal(m.arg("lib-del:")) })
	onExact("lib-del-do", func(u *UI, m actMsg) { u.libFileDelete(m.Val) })
	onPrefix("lib-mark:", func(u *UI, m actMsg) { u.libMark(m.arg("lib-mark:"), true) })
	onPrefix("lib-unmark:", func(u *UI, m actMsg) { u.libMark(m.arg("lib-unmark:"), false) })

	// collection
	onExact("lib-coll-search", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.collSearch = m.Val }) })
	onPrefix("lib-coll-sort:", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.collSort = m.arg("lib-coll-sort:") }) })
	onExact("lib-coll-dir", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.collDesc = !s.collDesc }) })
	onPrefix("lib-genre:", func(u *UI, m actMsg) {
		u.libToggle(func(s *libSt) map[string]bool { return s.collGenre }, m.arg("lib-genre:"), !u.libHas("genre", m.arg("lib-genre:")))
	})
	onPrefix("lib-label:", func(u *UI, m actMsg) {
		u.libToggle(func(s *libSt) map[string]bool { return s.collLabel }, m.arg("lib-label:"), !u.libHas("label", m.arg("lib-label:")))
	})
	onPrefix("lib-key:", func(u *UI, m actMsg) {
		u.libToggle(func(s *libSt) map[string]bool { return s.keySel }, m.arg("lib-key:"), !u.libHas("key", m.arg("lib-key:")))
	})
	onExact("lib-key-clear", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.keySel = map[string]bool{} }) })
	onPrefix("lib-key-harmonic:", func(u *UI, m actMsg) { u.libKeyHarmonic(m.arg("lib-key-harmonic:")) })
	onExact("lib-clearfilters", func(u *UI, m actMsg) {
		u.libSet(func(s *libSt) {
			s.collSearch, s.collGenre, s.collLabel, s.keySel = "", map[string]bool{}, map[string]bool{}, map[string]bool{}
		})
	})
	onPrefix("lib-collsel:", func(u *UI, m actMsg) {
		u.libToggle(func(s *libSt) map[string]bool { return s.collSel }, m.arg("lib-collsel:"), m.Val == "true")
	})
	onExact("lib-collsel-clear", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.collSel = map[string]bool{} }) })
	onExact("lib-import", func(u *UI, m actMsg) { u.libImportModal() })
	onPrefix("lib-import-do:", func(u *UI, m actMsg) { u.libImport(m.arg("lib-import-do:")) })
	onExact("lib-import-path", func(u *UI, m actMsg) { u.libImportPath(m.Val) }) // typed or pick-file'd
	onExact("lib-import-format", func(u *UI, m actMsg) { u.libSetQuiet(func(s *libSt) { s.impFormat = m.Val }) })
	onExact("lib-import-go", func(u *UI, m actMsg) { u.libImportFile() })
	onExact("lib-backup", func(u *UI, m actMsg) { u.libBackup() })
	onExact("lib-scan", func(u *UI, m actMsg) { u.libScan() })
	onExact("lib-cleanup", func(u *UI, m actMsg) { u.libCleanupModal() })
	onExact("lib-cleanup-do", func(u *UI, m actMsg) { u.libCleanup() })
	onExact("lib-relocate", func(u *UI, m actMsg) { u.libRelocate() })
	onExact("lib-reloc-root", func(u *UI, m actMsg) { u.libRelocRoot(m.Val) })
	onExact("lib-reloc-find", func(u *UI, m actMsg) { u.libRelocFind() })
	onPrefix("lib-reloc-skip:", func(u *UI, m actMsg) { u.libRelocSkip(atoi(m.arg("lib-reloc-skip:")), m.Val != "true") })
	onExact("lib-reloc-apply", func(u *UI, m actMsg) { u.libRelocApply() })
	onExact("lib-export", func(u *UI, m actMsg) { u.libExportModal() })
	onPrefix("lib-export-do:", func(u *UI, m actMsg) { u.libExport(m.arg("lib-export-do:"), "") })
	onPrefix("lib-export-as:", func(u *UI, m actMsg) { // pick-save target: val = chosen path
		if m.Val != "" {
			u.libExport(m.arg("lib-export-as:"), m.Val)
		}
	})
	onExact("lib-sync", func(u *UI, m actMsg) { u.libSyncModal() })
	onExact("lib-addto", func(u *UI, m actMsg) { u.libAddToModal(true, "") })
	onPrefix("lib-track-addto:", func(u *UI, m actMsg) { u.libAddToModal(false, m.arg("lib-track-addto:")) })
	onPrefix("lib-addto-do:", func(u *UI, m actMsg) { u.libAddToDo(m.arg("lib-addto-do:")) })

	// playlists
	onPrefix("lib-pl:", func(u *UI, m actMsg) { u.libOpenPlaylist(atoi64(m.arg("lib-pl:"))) })
	onExact("lib-pl-new", func(u *UI, m actMsg) { u.libNewPlaylistModal() })
	onExact("lib-pl-newsmart", func(u *UI, m actMsg) { u.libSmartOpen(0) })
	onExact("lib-pl-create", func(u *UI, m actMsg) { u.libCreatePlaylist(parseForm(m.Form)) })
	// smart-rules editor (full Fyne smartRulesDialog parity: chips + feel presets + live count)
	onPrefix("lib-sr-edit:", func(u *UI, m actMsg) { u.libSmartOpen(atoi64(m.arg("lib-sr-edit:"))) })
	onExact("lib-sr-name", func(u *UI, m actMsg) { u.libSRQuiet(func(s *libSt) { s.srName = m.Val }) })
	onPrefix("lib-sr-genre:", func(u *UI, m actMsg) { u.libSRGenre(m.arg("lib-sr-genre:")) })
	onExact("lib-sr-feel", func(u *UI, m actMsg) { u.libSRFeel(m.Val) })
	onExact("lib-sr-bpmmin", func(u *UI, m actMsg) { u.libSRQuiet(func(s *libSt) { s.srRules.BPMMin = atof(m.Val) }) })
	onExact("lib-sr-bpmmax", func(u *UI, m actMsg) { u.libSRQuiet(func(s *libSt) { s.srRules.BPMMax = atof(m.Val) }) })
	onExact("lib-sr-key", func(u *UI, m actMsg) {
		u.libSRQuiet(func(s *libSt) { s.srRules.KeyContains = strings.TrimSpace(m.Val) })
	})
	onExact("lib-sr-rating", func(u *UI, m actMsg) { u.libSRQuiet(func(s *libSt) { s.srRules.RatingMin = atoi(m.Val) }) })
	onExact("lib-sr-plays", func(u *UI, m actMsg) { u.libSRQuiet(func(s *libSt) { s.srRules.PlayCountMin = atoi(m.Val) }) })
	onExact("lib-sr-search", func(u *UI, m actMsg) { u.libSRQuiet(func(s *libSt) { s.srRules.Search = strings.TrimSpace(m.Val) }) })
	onExact("lib-sr-save", func(u *UI, m actMsg) { u.libSmartSave() })
	onPrefix("lib-pl-rename:", func(u *UI, m actMsg) { u.libRenamePlaylistModal(atoi64(m.arg("lib-pl-rename:"))) })
	onExact("lib-pl-rename-do", func(u *UI, m actMsg) { u.libRenamePlaylist(parseForm(m.Form)) })
	onPrefix("lib-pl-del:", func(u *UI, m actMsg) { u.libDelPlaylistModal(atoi64(m.arg("lib-pl-del:"))) })
	onPrefix("lib-pl-del-do:", func(u *UI, m actMsg) { u.libDelPlaylist(atoi64(m.arg("lib-pl-del-do:"))) })
	onPrefix("lib-pl-export:", func(u *UI, m actMsg) { u.libExportPlaylist(atoi64(m.arg("lib-pl-export:")), "") })
	onPrefix("lib-pl-exportas:", func(u *UI, m actMsg) { // pick-save target: val = chosen path
		if m.Val != "" {
			u.libExportPlaylist(atoi64(m.arg("lib-pl-exportas:")), m.Val)
		}
	})
	onPrefix("lib-pl-dup:", func(u *UI, m actMsg) { u.libDupPlaylist(atoi64(m.arg("lib-pl-dup:"))) })
	onPrefix("lib-pl-up:", func(u *UI, m actMsg) { u.libMovePlItem(atoi(m.arg("lib-pl-up:")), -1) })
	onPrefix("lib-pl-down:", func(u *UI, m actMsg) { u.libMovePlItem(atoi(m.arg("lib-pl-down:")), 1) })
	onPrefix("lib-pl-rm:", func(u *UI, m actMsg) { u.libRemovePlItem(m.arg("lib-pl-rm:")) })
	onPrefix("lib-pl-push:", func(u *UI, m actMsg) { u.libSync("push", atoi64(m.arg("lib-pl-push:"))) })
	onPrefix("lib-pl-pull:", func(u *UI, m actMsg) { u.libSync("pull", atoi64(m.arg("lib-pl-pull:"))) })
	onPrefix("lib-pl-unlink:", func(u *UI, m actMsg) { u.libSync("unlink", atoi64(m.arg("lib-pl-unlink:"))) })
	onExact("lib-pl-syncall", func(u *UI, m actMsg) { u.libSync("all", 0) })
	onExact("lib-pl-cloud", func(u *UI, m actMsg) { u.libSyncModal() })
	onExact("lib-pl-remote", func(u *UI, m actMsg) { u.libSyncModal() })
	onPrefix("lib-remote-import:", func(u *UI, m actMsg) { u.libRemoteImport(m.arg("lib-remote-import:")) })

	// history
	onExact("lib-hist-load", func(u *UI, m actMsg) { u.libHistLoad() })
	onExact("lib-hist-srcpick", func(u *UI, m actMsg) {
		u.libSet(func(s *libSt) { s.histSrc = m.Val })
		u.libHistLoad()
	})
	onPrefix("lib-session:", func(u *UI, m actMsg) { u.libOpenSession(atoi(m.arg("lib-session:"))) })
	onPrefix("lib-play-sort:", func(u *UI, m actMsg) {
		u.libSet(func(s *libSt) {
			v := m.arg("lib-play-sort:")
			if v == "Play order" {
				v = ""
			}
			s.playSort = v
		})
	})
	onExact("lib-play-dir", func(u *UI, m actMsg) { u.libSet(func(s *libSt) { s.playDesc = !s.playDesc }) })

	// id marks
	onPrefix("lib-id-artist:", func(u *UI, m actMsg) { u.libIDToggle(m.arg("lib-id-artist:"), "artist", m.Val == "true") })
	onPrefix("lib-id-label:", func(u *UI, m actMsg) { u.libIDToggle(m.arg("lib-id-label:"), "label", m.Val == "true") })
	onPrefix("lib-id-del:", func(u *UI, m actMsg) {
		if st := u.svc.IDMarks; st != nil {
			st.Remove(m.arg("lib-id-del:"))
		}
		u.libPatchBody()
	})
	onExact("lib-id-addpath", func(u *UI, m actMsg) { // pick-file/pick-dir target
		if m.Val != "" {
			u.libMarkAdd(map[string]string{"path": m.Val})
		}
	})
	onExact("lib-id-manual", func(u *UI, m actMsg) { u.libMarkPathModal() })
	onExact("lib-id-add", func(u *UI, m actMsg) { u.libMarkAdd(parseForm(m.Form)) })

	// tags (write DJ analysis into the file, revertible)
	onPrefix("lib-tags-write:", func(u *UI, m actMsg) { u.libTagsWrite(m.arg("lib-tags-write:")) })
	onPrefix("lib-tags-revert:", func(u *UI, m actMsg) { u.libTagsRevert(m.arg("lib-tags-revert:")) })

	// queue
	onPrefix("lib-job-cancel:", func(u *UI, m actMsg) { u.libJobCancel(atoi(m.arg("lib-job-cancel:"))) })

	// presets catalog
	onExact("lib-pset-new", func(u *UI, m actMsg) { u.libPresetModal("") })
	onPrefix("lib-pset-edit:", func(u *UI, m actMsg) { u.libPresetModal(m.arg("lib-pset-edit:")) })
	onPrefix("lib-pset-dup:", func(u *UI, m actMsg) { u.libPresetDup(m.arg("lib-pset-dup:")) })
	onPrefix("lib-pset-del:", func(u *UI, m actMsg) { u.libPresetDel(m.arg("lib-pset-del:")) })
	onExact("lib-pset-form", func(u *UI, m actMsg) { u.libPresetForm(parseForm(m.Form)) })
	onExact("lib-pset-save", func(u *UI, m actMsg) { u.libPresetSaveDraft("", "") })
	onExact("lib-pset-saveas", func(u *UI, m actMsg) { u.libPresetSaveAsModal() })
	onExact("lib-pset-saveas-do", func(u *UI, m actMsg) {
		f := parseForm(m.Form)
		u.libPresetSaveDraft(f["id"], f["label"])
	})

	// encode builder
	onPrefix("lib-preset:", func(u *UI, m actMsg) { u.libPickPreset(m.arg("lib-preset:")) })
	onPrefix("lib-pf:", func(u *UI, m actMsg) { u.libPF(m.arg("lib-pf:"), m.Val) })
	onExact("lib-trim-s", func(u *UI, m actMsg) { u.libSetQuiet(func(s *libSt) { s.trimS = m.Val }) })
	onExact("lib-trim-e", func(u *UI, m actMsg) { u.libSetQuiet(func(s *libSt) { s.trimE = m.Val }) })
	onExact("lib-transcode", func(u *UI, m actMsg) { u.libTranscodeSel() })

	// ~1 Hz refresh: queue progress + unified player clock/playhead
	onLiveTick("library", func(u *UI) {
		if u.libSectionOr() == "queue" {
			u.eval("window.__patch('lib-queue-body'," + jsQuote(u.libQueueHTML()) + ")")
			return
		}
		mpTick(u, "library")
	})
}

// ── state mutation helpers ──

func (u *UI) libSet(mut func(*libSt)) {
	s := u.lib()
	s.mu.Lock()
	mut(s)
	s.mu.Unlock()
	u.libPatchBody()
}

// libSetQuiet mutates without re-rendering (for trim inputs mid-edit).
func (u *UI) libSetQuiet(mut func(*libSt)) {
	s := u.lib()
	s.mu.Lock()
	mut(s)
	s.mu.Unlock()
}

func (u *UI) libToggle(get func(*libSt) map[string]bool, key string, on bool) {
	s := u.lib()
	s.mu.Lock()
	mp := get(s)
	if on {
		mp[key] = true
	} else {
		delete(mp, key)
	}
	s.mu.Unlock()
	u.libPatchBody()
}

func (u *UI) libHas(kind, key string) bool {
	s := u.lib()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch kind {
	case "genre":
		return s.collGenre[key]
	case "label":
		return s.collLabel[key]
	case "key":
		return s.keySel[key]
	}
	return false
}

func (u *UI) libPin() {
	dir := u.libDirOr()
	s := u.lib()
	s.mu.Lock()
	u.libMarks(s).Toggle(dir, filepath.Base(dir))
	s.mu.Unlock()
	u.libPatchBody()
}

func (u *UI) libUnpin(path string) {
	s := u.lib()
	s.mu.Lock()
	u.libMarks(s).Toggle(path, filepath.Base(path)) // toggle off (present → removed)
	s.mu.Unlock()
	u.libPatchBody()
}

func (u *UI) libKeyHarmonic(cam string) {
	k, ok := musiclib.ParseKey(cam)
	if !ok {
		return
	}
	sel := map[string]bool{}
	for n := 1; n <= 12; n++ {
		for _, mn := range []bool{false, true} {
			c := musiclib.Key{Num: n, Minor: mn}
			if musiclib.KeyRelation(k, c) != musiclib.RelNone {
				sel[c.Camelot()] = true
			}
		}
	}
	u.mu.Lock()
	u.libSection = "collection"
	u.mu.Unlock()
	s := u.lib()
	s.mu.Lock()
	s.keySel = sel
	s.mu.Unlock()
	u.patchMain()
}

// ── selection + async probe/peaks ──

func (u *UI) libSelect(path string, _ *musiclib.Track) {
	s := u.lib()
	s.mu.Lock()
	kind := libKind(path, false)
	sel := &libSel{path: path, kind: kind}
	if fi, err := os.Stat(path); err == nil {
		sel.size, sel.mod = fi.Size(), fi.ModTime()
	}
	if t, ok := s.byPath[path]; ok {
		sel.track, sel.inColl = t, true
	} else {
		sel.track = musiclib.Track{Path: path, Title: filepath.Base(path)}
	}
	s.sel = sel
	s.draftInit = false
	s.trimS, s.trimE = "", ""
	needProbe := (kind == "audio" || kind == "video") && u.svc.Workers != nil && pathOnDisk(path)
	track := sel.track
	s.mu.Unlock()
	if kind == "audio" && pathOnDisk(path) {
		u.mpEnsureFile("library", path, track) // unified player: peaks/loudness/chips load async
	}
	u.libPatchBody()
	if needProbe {
		u.libProbe(path)
	}
}

func (u *UI) libProbe(path string) {
	s := u.lib()
	s.mu.Lock()
	if s.sel != nil && s.sel.path == path {
		s.sel.srcLoading = true
	}
	s.mu.Unlock()
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		raw, err := u.svc.Workers.Run(ctx, "probe", "probe.streams", map[string]any{"path": path})
		var si transcode.SourceInfo
		if err == nil {
			si, _ = transcode.ParseProbe(raw)
		}
		s.mu.Lock()
		if s.sel != nil && s.sel.path == path {
			s.sel.srcLoading = false
			v := si
			s.sel.src = &v
		}
		s.mu.Unlock()
		u.libPatchDetail()
	})
}

func (u *UI) libProbeToast(path string) {
	if u.svc.Workers == nil {
		u.toast(i18n.T("library.toast.workerUnavailable"))
		return
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		raw, err := u.svc.Workers.Run(ctx, "probe", "probe.streams", map[string]any{"path": path})
		if err != nil {
			u.toast(i18n.T("library.toast.probePrefix") + err.Error())
			return
		}
		u.toast(i18n.T("library.toast.probeOk", i18n.A{"n": strconv.Itoa(len(raw))}))
	})
}

// ── encode builder ──

func (u *UI) libPickPreset(id string) {
	var p transcode.Preset
	found := false
	if u.svc.Cfg != nil {
		for _, cp := range u.svc.Cfg.Features.Transcode.Presets {
			if cp.ID == id {
				p, found = cp, true
				break
			}
		}
	}
	if !found {
		p, found = transcode.Find(id)
	}
	if !found {
		return
	}
	s := u.lib()
	s.mu.Lock()
	s.draft, s.draftInit = p, true
	s.mu.Unlock()
	u.libPatchDetail()
}

func (u *UI) libPF(field, val string) {
	s := u.lib()
	s.mu.Lock()
	d := &s.draft
	switch field {
	case "container":
		d.Container = val
	case "vcodec":
		d.VideoCodec = val
	case "accel":
		d.Accel = val
	case "profile":
		transcode.ApplyProfile(d, val)
	case "ratemode":
		d.RateMode = val
	case "crf":
		d.CRF = atoi(val)
	case "bitratek":
		d.BitrateK = atoi(val)
	case "res":
		switch val {
		case "720":
			d.Width, d.Height = 1280, 720
		case "1080":
			d.Width, d.Height = 1920, 1080
		case "1440":
			d.Width, d.Height = 2560, 1440
		case "2160":
			d.Width, d.Height = 3840, 2160
		default:
			d.Width, d.Height = 0, 0
		}
	case "fps":
		d.FPS = atof(val)
	case "acodec":
		d.AudioCodec = val
	case "abitratek":
		d.AudioBitrateK = atoi(val)
	case "channels":
		d.Channels = atoi(val)
	case "samplerate":
		d.SampleRate = atoi(val)
	case "loudon":
		d.LoudnessOn = val == "true"
	case "loudi":
		d.LoudnessI = atof(val)
	case "loudtp":
		d.LoudnessTP = atof(val)
	case "loudraise":
		d.LoudnessRaiseOnly = val == "true"
	}
	s.mu.Unlock()
	u.libPatchDetail()
}

func (u *UI) libTranscodeSel() {
	s := u.lib()
	s.mu.Lock()
	if s.sel == nil {
		s.mu.Unlock()
		return
	}
	path, pre, ts, te := s.sel.path, s.draft, s.trimS, s.trimE
	s.mu.Unlock()
	u.libStartTranscode(path, pre, ts, te)
	u.mu.Lock()
	u.libSection = "queue"
	u.mu.Unlock()
	u.patchMain()
}

func (u *UI) libStartTranscode(path string, pre transcode.Preset, ts, te string) {
	hub := u.svc.Hub
	if hub == nil {
		u.toast(i18n.T("library.toast.transcodeWorkerUnavailable"))
		return
	}
	id := pre.ID
	if id == "" {
		id = "custom"
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	out := filepath.Join(filepath.Dir(path), "rave-mate-transcoded", base+"-"+id+pre.Ext())
	tsF, _ := strconv.ParseFloat(strings.TrimSpace(ts), 64)
	teF, _ := strconv.ParseFloat(strings.TrimSpace(te), 64)
	jid := fmt.Sprintf("libui-%d", time.Now().UnixNano())
	j := &libJob{id: jid, name: filepath.Base(path), preset: pre.Label, status: "queued", cancel: func() { hub.Cancel(jid) }}
	s := u.lib()
	s.jobsMu.Lock()
	s.jobs = append([]*libJob{j}, s.jobs...)
	s.jobsMu.Unlock()
	params := map[string]any{"input": path, "output": out, "preset": pre, "trimStart": tsF, "trimEnd": teF}
	hub.Start(jid, params,
		func(event string, data json.RawMessage) {
			if event != "progress" {
				return
			}
			var p struct {
				Percent float64 `json:"percent"`
			}
			if json.Unmarshal(data, &p) == nil {
				u.libJobUpd(j, func() {
					if j.status == "queued" {
						j.status = "running"
					}
					j.pct = p.Percent
				})
			}
		},
		func(r jobs.EndResult) {
			switch {
			case r.Canceled:
				u.libJobUpd(j, func() { j.status = "canceled" })
			case !r.OK:
				u.libJobUpd(j, func() { j.status = "error"; j.msg = r.Error })
				u.toast(i18n.T("library.toast.transcodeFailed") + r.Error)
			default:
				u.libJobUpd(j, func() { j.status = "done"; j.pct = 100 })
				u.toast(i18n.T("library.toast.transcodedTo") + out)
			}
		})
	u.toast(i18n.T("library.toast.transcodeQueued"))
}

func (u *UI) libJobUpd(j *libJob, mut func()) {
	s := u.lib()
	s.jobsMu.Lock()
	mut()
	s.jobsMu.Unlock()
	if u.activeTab() == "library" && u.libSectionOr() == "queue" {
		u.eval("window.__patch('lib-queue-body'," + jsQuote(u.libQueueHTML()) + ")")
	}
}

func (u *UI) libJobCancel(idx int) {
	s := u.lib()
	s.jobsMu.Lock()
	if idx >= 0 && idx < len(s.jobs) && s.jobs[idx].cancel != nil {
		s.jobs[idx].cancel()
	}
	s.jobsMu.Unlock()
}

// ── preset catalog ──

func (u *UI) libPresetSaveDraft(newID, label string) {
	if u.svc.Cfg == nil {
		u.toast(i18n.T("library.toast.configUnavailable"))
		return
	}
	u.closeModal()
	s := u.lib()
	s.mu.Lock()
	d := s.draft
	s.mu.Unlock()
	if newID != "" {
		d.ID = newID
	}
	if label != "" {
		d.Label = label
	}
	if d.ID == "" {
		d.ID = "custom"
	}
	if d.Label == "" {
		d.Label = d.ID
	}
	u.libUpsertPreset(d)
	u.toast(i18n.T("library.toast.presetSavedName") + d.Label)
}

func (u *UI) libUpsertPreset(p transcode.Preset) {
	ps := u.svc.Cfg.Features.Transcode.Presets
	for i := range ps {
		if ps[i].ID == p.ID {
			ps[i] = p
			u.svc.Cfg.Features.Transcode.Presets = ps
			u.saveCfg()
			return
		}
	}
	u.svc.Cfg.Features.Transcode.Presets = append(ps, p)
	u.saveCfg()
}

func (u *UI) libPresetDup(id string) {
	p, ok := u.findPreset(id)
	if !ok || u.svc.Cfg == nil {
		return
	}
	p.ID = id + "-copy"
	p.Label = p.Label + " (copy)"
	u.libUpsertPreset(p)
	u.toast(i18n.T("library.toast.presetDuplicated"))
	u.patchMain()
}

func (u *UI) libPresetDel(id string) {
	if u.svc.Cfg == nil {
		return
	}
	ps := u.svc.Cfg.Features.Transcode.Presets
	out := ps[:0]
	for _, p := range ps {
		if p.ID != id {
			out = append(out, p)
		}
	}
	u.svc.Cfg.Features.Transcode.Presets = out
	u.saveCfg()
	u.toast(i18n.T("library.toast.presetDeleted"))
	u.patchMain()
}

func (u *UI) findPreset(id string) (transcode.Preset, bool) {
	if u.svc.Cfg != nil {
		for _, p := range u.svc.Cfg.Features.Transcode.Presets {
			if p.ID == id {
				return p, true
			}
		}
	}
	return transcode.Find(id)
}

func (u *UI) libPresetModal(id string) {
	p := transcode.Preset{ID: "my-preset", Label: "My preset", Container: "mp4", VideoCodec: "h264", Accel: "auto", RateMode: "crf", CRF: 23, AudioCodec: "aac", AudioBitrateK: 192}
	if id != "" {
		if ep, ok := u.findPreset(id); ok {
			p = ep
		}
	}
	body := `<form data-act=lib-pset-form class=mform>` +
		hiddenField("origID", id) +
		labeledInput("id", i18n.T("library.label.id"), p.ID) + labeledInput("label", i18n.T("library.label.label"), p.Label) + labeledInput("desc", i18n.T("library.label.description"), p.Desc) +
		selNamed("container", p.Container, containerOpts) +
		selNamed("vcodec", p.VideoCodec, videoCodecOpts) + selNamed("accel", p.Accel, accelOpts()) +
		selNamed("acodec", p.AudioCodec, audioCodecOpts) +
		labeledInput("crf", i18n.T("library.label.crf"), strconv.Itoa(p.CRF)) + labeledInput("abitratek", i18n.T("library.label.audioKbps"), strconv.Itoa(p.AudioBitrateK)) +
		`<button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(i18n.T("library.enc.savePreset")) + `</button></form>`
	u.openModal(modal(i18n.T("library.modal.preset"), body, ""))
}

func (u *UI) libPresetForm(f map[string]string) {
	if u.svc.Cfg == nil {
		return
	}
	u.closeModal()
	p, _ := u.findPreset(f["origID"])
	p.ID = strings.TrimSpace(f["id"])
	if p.ID == "" {
		p.ID = "custom"
	}
	p.Label = f["label"]
	p.Desc = f["desc"]
	if v := f["container"]; v != "" {
		p.Container = v
	}
	if v := f["vcodec"]; v != "" {
		p.VideoCodec = v
	}
	if v := f["accel"]; v != "" {
		p.Accel = v
	}
	if v := f["acodec"]; v != "" {
		p.AudioCodec = v
	}
	p.CRF = atoi(f["crf"])
	p.AudioBitrateK = atoi(f["abitratek"])
	// dropping the origID under a new ID = rename: remove old entry.
	if o := strings.TrimSpace(f["origID"]); o != "" && o != p.ID {
		u.libPresetDel(o)
	}
	u.libUpsertPreset(p)
	u.toast(i18n.T("library.toast.presetSaved"))
	u.patchMain()
}

func (u *UI) libPresetSaveAsModal() {
	body := `<form data-act=lib-pset-saveas-do class=mform>` +
		labeledInput("id", i18n.T("library.label.newID"), "my-preset") + labeledInput("label", i18n.T("library.label.label"), "My preset") +
		`<button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(i18n.T("library.label.saveAsNew")) + `</button></form>`
	u.openModal(modal(i18n.T("library.modal.savePresetAs"), body, ""))
}

// ── collection: import / backup / scan / cleanup / export ──

func (u *UI) libImportModal() {
	s := u.lib()
	s.mu.Lock()
	path, format := s.impPath, s.impFormat
	s.mu.Unlock()
	if format == "" {
		format = "rekordbox"
	}
	body := `<p class=page-sub>` + html.EscapeString(i18n.T("library.modal.importDesc")) + `</p>` +
		btnRow(btn(i18n.T("library.label.importTraktorAuto"), "primary", "lib-import-do:traktor", ""), btn(i18n.T("library.label.importRekordboxAuto"), "outline", "lib-import-do:rekordbox", "")) +
		`<div class=mform><div class=pb-label>` + html.EscapeString(i18n.T("library.label.importFromFile")) + `</div>` +
		`<div class=lib-toolbar>` + fieldRaw("lib-import-path", path, i18n.T("library.label.xmlPath")) +
		btn(i18n.T("common.browse"), "ghost", "pick-file:lib-import-path", "") + `</div>` +
		pbSelect(i18n.T("library.label.format"), "lib-import-format", [][2]string{{"rekordbox", i18n.T("library.label.rekordboxXML")}, {"virtualdj", i18n.T("library.label.virtualdjXML")}}, format) +
		btnRow(btn(i18n.T("library.label.importFile"), "outline", "lib-import-go", "")) + `</div>`
	u.openModal(modal(i18n.T("library.modal.importLibrary"), body, ""))
}

// libImportPath stores a typed/picked import file path and refreshes the modal.
func (u *UI) libImportPath(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	u.libSetQuiet(func(s *libSt) { s.impPath = strings.TrimSpace(path) })
	u.libImportModal()
}

func (u *UI) libImport(kind string) {
	u.closeModal()
	if u.svc.Lib == nil {
		u.toast(i18n.T("library.dbUnavailable"))
		return
	}
	switch kind {
	case "traktor":
		u.toast(i18n.T("library.toast.importingTraktor"))
		u.bg(func() {
			installs, err := musiclib.DiscoverTraktor()
			if err != nil || len(installs) == 0 || installs[0].Collection == "" {
				u.toast(i18n.T("library.toast.noTraktorCollection"))
				return
			}
			in := installs[0]
			f, oerr := os.Open(in.Collection)
			if oerr != nil {
				u.toast(i18n.T("library.toast.openPrefix") + oerr.Error())
				return
			}
			var tracks []musiclib.Track
			_, perr := musiclib.ParseCollection(f, func(t musiclib.Track) { tracks = append(tracks, t) })
			_ = f.Close()
			if perr != nil {
				u.toast(i18n.T("library.toast.parsePrefix") + perr.Error())
				return
			}
			var pls []musiclib.Playlist
			if pf, e := os.Open(in.Collection); e == nil {
				pls, _ = musiclib.ParseNMLPlaylists(pf)
				_ = pf.Close()
			}
			u.libPersist(musiclib.Source{App: "traktor", Version: in.Version, Path: in.Collection}, tracks, pls, nil)
			u.toast(i18n.Tn("library.toast.importedTraktor", len(tracks)))
			u.libReload()
		})
	case "rekordbox":
		u.toast(i18n.T("library.toast.importingRekordbox"))
		u.bg(func() {
			installs, err := musiclib.DiscoverRekordbox()
			if err != nil || len(installs) == 0 {
				u.toast(i18n.T("library.toast.noRekordboxXML"))
				return
			}
			f, oerr := os.Open(installs[0].XML)
			if oerr != nil {
				u.toast(i18n.T("library.toast.openPrefix") + oerr.Error())
				return
			}
			lib, ierr := musiclib.Import(musiclib.FormatRekordbox, f)
			_ = f.Close()
			if ierr != nil {
				u.toast(i18n.T("library.toast.parsePrefix") + ierr.Error())
				return
			}
			lib.Source.Path = installs[0].XML
			u.libPersist(lib.Source, lib.Tracks, lib.Playlists, lib.Sessions)
			u.toast(i18n.Tn("library.toast.importedRekordbox", len(lib.Tracks)))
			u.libReload()
		})
	}
}

func (u *UI) libImportFile() {
	u.closeModal()
	s := u.lib()
	s.mu.Lock()
	path, fm := strings.TrimSpace(s.impPath), s.impFormat
	s.mu.Unlock()
	if path == "" || u.svc.Lib == nil {
		u.toast(i18n.T("library.toast.enterFilePath"))
		return
	}
	format := musiclib.FormatRekordbox
	if fm == "virtualdj" {
		format = musiclib.FormatVirtualDJ
	}
	u.bg(func() {
		fh, err := os.Open(path)
		if err != nil {
			u.toast(i18n.T("library.toast.openPrefix") + err.Error())
			return
		}
		defer func() { _ = fh.Close() }()
		lib, ierr := musiclib.Import(format, fh)
		if ierr != nil {
			u.toast(i18n.T("library.toast.importPrefix") + ierr.Error())
			return
		}
		lib.Source.Path = path
		u.libPersist(lib.Source, lib.Tracks, lib.Playlists, lib.Sessions)
		u.toast(i18n.Tn("library.toast.importedTracks", len(lib.Tracks)))
		u.libReload()
	})
}

func (u *UI) libPersist(src musiclib.Source, tracks []musiclib.Track, pls []musiclib.Playlist, sessions []musiclib.Session) {
	db := u.svc.Lib
	if db == nil {
		return
	}
	sr, err := db.UpsertSource(src, 0)
	if err != nil {
		u.logErr("upsert source", err)
		return
	}
	ts, err := db.BeginTrackSync(sr.ID)
	if err != nil {
		u.logErr("begin sync", err)
		return
	}
	for _, t := range tracks {
		if e := ts.Add(t); e != nil {
			ts.Rollback()
			u.logErr("track add", e)
			return
		}
	}
	if _, e := ts.Commit(); e != nil {
		u.logErr("commit", e)
		return
	}
	if len(pls) > 0 {
		u.logErr("playlists", db.SyncImportedPlaylists(sr.ID, pls))
	}
	if len(sessions) > 0 {
		u.logErr("sessions", db.SyncSessions(sr.ID, sessions))
	}
}

func (u *UI) libReload() {
	s := u.lib()
	s.mu.Lock()
	s.loaded = false
	s.mu.Unlock()
	u.patchMain()
}

func (u *UI) libBackup() {
	u.toast(i18n.T("library.toast.backingUp"))
	u.bg(func() {
		installs, err := musiclib.DiscoverTraktor()
		if err != nil || len(installs) == 0 || installs[0].Collection == "" {
			u.toast(i18n.T("library.toast.noTraktorBackup"))
			return
		}
		root, _ := config.DataPath("library-backups")
		bk, berr := musiclib.BackupCollection(installs[0], root)
		if berr != nil {
			u.toast(i18n.T("library.toast.backupFailed") + berr.Error())
			return
		}
		u.toast(i18n.T("library.toast.backedUpTo") + bk.Path)
	})
}

func (u *UI) libScan() {
	s := u.lib()
	s.mu.Lock()
	tracks := s.tracks
	s.mu.Unlock()
	if len(tracks) == 0 {
		u.toast(i18n.T("library.toast.importFirst"))
		return
	}
	u.bg(func() {
		_, missing := musiclib.ScanMissing(tracks)
		u.toast(i18n.T("library.toast.filesMissing", i18n.A{"missing": strconv.Itoa(len(missing)), "total": strconv.Itoa(len(tracks))}))
	})
}

func (u *UI) libCleanupModal() {
	body := `<p class=page-sub>` + html.EscapeString(i18n.T("library.modal.cleanupDesc")) + `</p>` +
		btnRow(btn(i18n.T("library.label.backupClean"), "destructive", "lib-cleanup-do", ""), btn(i18n.T("common.cancel"), "outline", "modal-close", ""))
	u.openModal(modal(i18n.T("library.modal.cleanupCollection"), body, ""))
}

func (u *UI) libCleanup() {
	u.closeModal()
	if u.svc.Lib == nil {
		return
	}
	u.toast(i18n.T("library.toast.backingUpCleaning"))
	u.bg(func() {
		col := ""
		if installs, err := musiclib.DiscoverTraktor(); err == nil && len(installs) > 0 {
			col = installs[0].Collection
		}
		root, _ := config.DataPath("library-backups")
		rep, err := maintenance.CleanupMissing(u.svc.Lib, col, root)
		if err != nil {
			u.toast(i18n.T("library.toast.cleanupFailed") + err.Error())
			return
		}
		u.toast(i18n.T("library.toast.cleanedUp", i18n.A{"tracks": strconv.Itoa(rep.TracksDeleted), "entries": strconv.Itoa(rep.PlaylistEntriesDel), "playlists": strconv.Itoa(rep.EmptyPlaylistsDel), "backup": root}))
		u.libReload()
	})
}

// ── relocate missing files (Fyne doRelocate parity) ──
// Scan missing → index a search root → candidates (confidence-scored, per-row opt-out) →
// backup + write a NEW collection.fixed.nml. The source collection.nml is never modified.

func (u *UI) libRelocate() {
	s := u.lib()
	s.mu.Lock()
	u.libEnsureTracks(s)
	tracks := s.tracks
	s.mu.Unlock()
	if len(tracks) == 0 {
		u.toast(i18n.T("library.toast.importFirst"))
		return
	}
	u.toast(i18n.T("library.toast.scanningMissing"))
	u.bg(func() {
		_, missing := musiclib.ScanMissing(tracks)
		if len(missing) == 0 {
			u.toast(i18n.T("library.toast.noMissing"))
			return
		}
		s.mu.Lock()
		s.relocMiss, s.relocCands, s.relocSkip, s.relocMsg, s.relocBusy = missing, nil, map[int]bool{}, "", false
		h := u.libRelocModalHTML(s)
		s.mu.Unlock()
		u.openModal(h)
	})
}

func (u *UI) libRelocRoot(root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return
	}
	s := u.lib()
	s.mu.Lock()
	s.relocRoot = root
	h := u.libRelocModalHTML(s) // re-open so a pick-dir'd path shows in the field
	s.mu.Unlock()
	u.openModal(h)
}

func (u *UI) libRelocFind() {
	s := u.lib()
	s.mu.Lock()
	root, missing := strings.TrimSpace(s.relocRoot), s.relocMiss
	if root == "" || s.relocBusy {
		s.mu.Unlock()
		if root == "" {
			u.toast(i18n.T("library.toast.enterFolder"))
		}
		return
	}
	s.relocBusy, s.relocMsg = true, i18n.T("library.toast.indexing")
	h := u.libRelocModalHTML(s)
	s.mu.Unlock()
	u.openModal(h)
	u.bg(func() {
		idx, err := musiclib.BuildIndex([]string{root})
		var cands []musiclib.Candidate
		msg := ""
		if err != nil {
			msg = i18n.T("library.toast.indexError") + err.Error()
		} else {
			cands = musiclib.Relocate(missing, idx)
			msg = i18n.T("library.toast.relocFound", i18n.A{"found": strconv.Itoa(len(cands)), "total": strconv.Itoa(len(missing))})
		}
		s.mu.Lock()
		s.relocBusy, s.relocCands, s.relocSkip, s.relocMsg = false, cands, map[int]bool{}, msg
		h := u.libRelocModalHTML(s)
		s.mu.Unlock()
		u.openModal(h)
	})
}

func (u *UI) libRelocSkip(i int, skip bool) {
	s := u.lib()
	s.mu.Lock()
	if s.relocSkip == nil {
		s.relocSkip = map[int]bool{}
	}
	if skip {
		s.relocSkip[i] = true
	} else {
		delete(s.relocSkip, i)
	}
	s.mu.Unlock()
}

func (u *UI) libRelocApply() {
	s := u.lib()
	s.mu.Lock()
	var keep []musiclib.Candidate
	for i, c := range s.relocCands {
		if !s.relocSkip[i] {
			keep = append(keep, c)
		}
	}
	busy := s.relocBusy
	s.mu.Unlock()
	if len(keep) == 0 || busy {
		if len(keep) == 0 {
			u.toast(i18n.T("library.toast.findFirst"))
		}
		return
	}
	relocDone := func(msg string) {
		s.mu.Lock()
		s.relocBusy, s.relocMsg = false, msg
		h := u.libRelocModalHTML(s)
		s.mu.Unlock()
		u.openModal(h)
	}
	s.mu.Lock()
	s.relocBusy, s.relocMsg = true, i18n.T("library.toast.backingUpWriting")
	h := u.libRelocModalHTML(s)
	s.mu.Unlock()
	u.openModal(h)
	u.bg(func() {
		installs, err := musiclib.DiscoverTraktor()
		if err != nil || len(installs) == 0 || installs[0].Collection == "" {
			relocDone(i18n.T("library.toast.relocNoTraktor"))
			return
		}
		in := installs[0]
		bkRoot, _ := config.DataPath("library-backups")
		if _, berr := musiclib.BackupCollection(in, bkRoot); berr != nil {
			relocDone(i18n.T("library.toast.relocBackupFailed") + berr.Error())
			return
		}
		out, _ := config.DataPath("collection.fixed.nml")
		src, oerr := os.Open(in.Collection)
		if oerr != nil {
			relocDone(i18n.T("library.toast.openPrefix") + oerr.Error())
			return
		}
		dst, cerr := os.Create(out)
		if cerr != nil {
			_ = src.Close()
			relocDone(i18n.T("library.toast.createPrefix") + cerr.Error())
			return
		}
		fixed, werr := musiclib.WriteFixedCollection(src, musiclib.FixPlan{Fixes: keep}, dst)
		_ = src.Close()
		_ = dst.Close()
		if werr != nil {
			relocDone(i18n.T("library.toast.wroteThenError", i18n.A{"n": strconv.Itoa(fixed), "err": werr.Error()}))
			return
		}
		relocDone(i18n.T("library.toast.relocFixed", i18n.A{"n": strconv.Itoa(fixed), "out": out}))
		u.toast(i18n.Tn("library.toast.relocatedFiles", fixed, i18n.A{"out": out}))
	})
}

func (u *UI) libExportModal() {
	body := `<p class=page-sub>` + html.EscapeString(i18n.T("library.modal.exportDesc")) + `</p>`
	fmtLabels := map[string]string{"rekordbox": i18n.T("library.label.rekordboxXML"), "traktor": i18n.T("library.label.traktorNML"),
		"virtualdj": i18n.T("library.label.virtualdjXML"), "m3u": i18n.T("library.label.m3uPlaylist"), "csv": i18n.T("library.label.csvBackup")}
	for _, f := range [][2]string{{"rekordbox", "xml"}, {"traktor", "nml"}, {"virtualdj", "xml"}, {"m3u", "m3u8"}, {"csv", "csv"}} {
		body += itemRow(fmtLabels[f[0]], "", btn(i18n.T("library.label.export"), "outline", "lib-export-do:"+f[0], ""),
			btn(i18n.T("library.label.saveAs"), "ghost", "pick-save:"+f[1]+":lib-export-as:"+f[0], ""))
	}
	u.openModal(modal(i18n.T("library.modal.exportConvert"), body, ""))
}

// libExport writes the collection in fmtID to out ("" = default app-data path).
func (u *UI) libExport(fmtID, out string) {
	u.closeModal()
	s := u.lib()
	s.mu.Lock()
	tracks := s.tracks
	s.mu.Unlock()
	if len(tracks) == 0 {
		u.toast(i18n.T("library.toast.importFirst"))
		return
	}
	format := musiclib.Format(fmtID)
	if out == "" {
		names := map[string]string{"rekordbox": "rekordbox.xml", "traktor": "collection.exported.nml", "virtualdj": "virtualdj.database.xml", "m3u": "library.m3u8", "csv": "library.csv"}
		out, _ = config.DataPath("library-export-" + names[fmtID])
	}
	u.bg(func() {
		fh, err := os.Create(out)
		if err != nil {
			u.toast(i18n.T("library.toast.exportFailed") + err.Error())
			return
		}
		defer func() { _ = fh.Close() }()
		if eerr := musiclib.Export(format, musiclib.Library{Tracks: tracks}, fh); eerr != nil {
			u.toast(i18n.T("library.toast.exportFailed") + eerr.Error())
			return
		}
		u.toast(i18n.T("library.toast.exportedTo") + out)
	})
}

// ── collection ↔ playlist ──

func (u *UI) libAddToModal(multi bool, single string) {
	if u.svc.Lib == nil {
		return
	}
	s := u.lib()
	s.mu.Lock()
	var paths []string
	if multi {
		for p := range s.collSel {
			paths = append(paths, p)
		}
	} else if single != "" {
		paths = []string{single}
	}
	s.addto = paths
	s.mu.Unlock()
	rows, _ := u.svc.Lib.ListPlaylists()
	body := `<p class=page-sub>` + html.EscapeString(i18n.Tn("library.modal.choosePlaylist", len(paths))) + `</p>`
	for _, p := range rows {
		if p.Kind != "manual" {
			continue
		}
		body += btn(p.Name, "outline", fmt.Sprintf("lib-addto-do:%d", p.ID), "")
	}
	body += `<form data-act=lib-pl-create class=mform><div class=pb-label>` + html.EscapeString(i18n.T("library.label.orCreateManual")) + `</div>` +
		`<input class=field-input name=name placeholder="` + html.EscapeString(i18n.T("library.label.newPlaylistName")) + `"><button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(i18n.T("library.label.createAdd")) + `</button></form>`
	u.openModal(modal(i18n.T("library.modal.addToPlaylist"), body, ""))
}

func (u *UI) libAddToDo(idStr string) {
	u.closeModal()
	if u.svc.Lib == nil {
		return
	}
	id := atoi64(idStr)
	s := u.lib()
	s.mu.Lock()
	paths := s.addto
	s.collSel = map[string]bool{}
	s.mu.Unlock()
	if len(paths) == 0 {
		return
	}
	n, err := u.svc.Lib.AddToPlaylist(id, paths...)
	if err != nil {
		u.toast(i18n.T("library.toast.addFailed") + err.Error())
		return
	}
	u.toast(i18n.Tn("library.toast.addedTracks", n))
	u.patchMain()
}

// ── playlists ──

// libPlaylistItems resolves a playlist's rows: smart = live rule evaluation against the
// loaded collection (Fyne openPlaylist parity); others from the DB.
func (u *UI) libPlaylistItems(row libdb.PlaylistRow) []libdb.PlaylistItemRow {
	if row.Kind == libdb.PlaylistSmart {
		s := u.lib()
		s.mu.Lock()
		u.libEnsureTracks(s)
		tracks := s.tracks
		s.mu.Unlock()
		rules, ok := libParseRules(row.Rules)
		if !ok {
			return nil
		}
		var items []libdb.PlaylistItemRow
		for _, t := range musiclib.FilterSmart(tracks, rules) {
			items = append(items, libdb.PlaylistItemRow{Path: t.Path})
		}
		return items
	}
	if u.svc.Lib == nil {
		return nil
	}
	items, _ := u.svc.Lib.PlaylistItems(row.ID)
	return items
}

func (u *UI) libOpenPlaylist(id int64) {
	if u.svc.Lib == nil {
		return
	}
	row, ok, _ := u.svc.Lib.PlaylistByID(id)
	if !ok {
		return
	}
	items := u.libPlaylistItems(row)
	s := u.lib()
	s.mu.Lock()
	s.plSel, s.plCur, s.plItems = id, row, items
	s.mu.Unlock()
	u.libPatchBody()
}

func (u *UI) libNewPlaylistModal() {
	body := `<form data-act=lib-pl-create class=mform>` + labeledInput("name", i18n.T("library.sr.name"), "") +
		`<button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(i18n.T("library.label.create")) + `</button></form>`
	u.openModal(modal(i18n.T("library.pl.new"), body, ""))
}

func (u *UI) libCreatePlaylist(f map[string]string) {
	u.closeModal()
	if u.svc.Lib == nil {
		return
	}
	name := strings.TrimSpace(f["name"])
	if name == "" {
		u.toast(i18n.T("library.toast.enterName"))
		return
	}
	id, err := u.svc.Lib.CreatePlaylist(name, "manual", "")
	if err != nil {
		u.toast(i18n.T("library.toast.createFailed") + err.Error())
		return
	}
	// "Create + add" from the add-to modal: attach the pending paths.
	s := u.lib()
	s.mu.Lock()
	paths := s.addto
	s.addto = nil
	s.collSel = map[string]bool{}
	s.mu.Unlock()
	if len(paths) > 0 {
		_, _ = u.svc.Lib.AddToPlaylist(id, paths...)
	}
	u.toast(i18n.T("library.toast.createdName") + name)
	u.patchMain()
}

func (u *UI) libRenamePlaylistModal(id int64) {
	body := fmt.Sprintf(`<form data-act=lib-pl-rename-do class=mform>%s%s<button class="rp-btn rp-btn--primary" type=submit>%s</button></form>`,
		hiddenField("id", strconv.FormatInt(id, 10)), labeledInput("name", i18n.T("library.label.newName"), ""), html.EscapeString(i18n.T("library.pl.rename")))
	u.openModal(modal(i18n.T("library.modal.renamePlaylist"), body, ""))
}

func (u *UI) libRenamePlaylist(f map[string]string) {
	u.closeModal()
	if u.svc.Lib == nil {
		return
	}
	name := strings.TrimSpace(f["name"])
	if name == "" {
		return
	}
	if err := u.svc.Lib.RenamePlaylist(atoi64(f["id"]), name); err != nil {
		u.toast(i18n.T("library.toast.renameFailed") + err.Error())
		return
	}
	u.libReloadPlaylist(atoi64(f["id"]))
}

func (u *UI) libDelPlaylistModal(id int64) {
	body := `<p class=page-sub>` + html.EscapeString(i18n.T("library.confirm.deletePlaylist")) + `</p>` +
		btnRow(btn(i18n.T("common.delete"), "destructive", fmt.Sprintf("lib-pl-del-do:%d", id), ""), btn(i18n.T("common.cancel"), "outline", "modal-close", ""))
	u.openModal(modal(i18n.T("library.modal.deletePlaylist"), body, ""))
}

func (u *UI) libDelPlaylist(id int64) {
	u.closeModal()
	if u.svc.Lib == nil {
		return
	}
	if err := u.svc.Lib.DeletePlaylist(id); err != nil {
		u.toast(i18n.T("library.toast.deleteFailed") + err.Error())
		return
	}
	s := u.lib()
	s.mu.Lock()
	if s.plSel == id {
		s.plSel = 0
	}
	s.mu.Unlock()
	u.toast(i18n.T("library.toast.deletedPlaylist"))
	u.patchMain()
}

// libExportPlaylist writes the playlist (smart = live matches) as M3U to out ("" = app data).
func (u *UI) libExportPlaylist(id int64, out string) {
	if u.svc.Lib == nil {
		return
	}
	row, ok, _ := u.svc.Lib.PlaylistByID(id)
	if !ok {
		return
	}
	items := u.libPlaylistItems(row)
	s := u.lib()
	s.mu.Lock()
	var tracks []musiclib.Track
	for _, it := range items {
		if t, tok := s.byPath[it.Path]; tok {
			tracks = append(tracks, t)
		} else {
			tracks = append(tracks, musiclib.Track{Path: it.Path, Title: filepath.Base(it.Path)})
		}
	}
	s.mu.Unlock()
	if out == "" {
		out, _ = config.DataPath(fmt.Sprintf("playlist-%d.m3u8", id))
	}
	u.bg(func() {
		fh, err := os.Create(out)
		if err != nil {
			u.toast(i18n.T("library.toast.exportFailed") + err.Error())
			return
		}
		defer func() { _ = fh.Close() }()
		if e := musiclib.ExportM3U(tracks, fh); e != nil {
			u.toast(i18n.T("library.toast.exportFailed") + e.Error())
			return
		}
		u.toast(i18n.T("library.toast.exportedArrow") + out)
	})
}

// libDupPlaylist materializes a smart/imported playlist as an editable manual copy.
func (u *UI) libDupPlaylist(id int64) {
	if u.svc.Lib == nil {
		return
	}
	row, ok, _ := u.svc.Lib.PlaylistByID(id)
	if !ok {
		return
	}
	var paths []string
	for _, it := range u.libPlaylistItems(row) {
		if it.Path != "" {
			paths = append(paths, it.Path)
		}
	}
	newID, err := u.svc.Lib.CreatePlaylist(row.Name+" (manual)", "manual", "")
	if err != nil {
		u.toast(i18n.T("library.toast.duplicateFailed") + err.Error())
		return
	}
	if len(paths) > 0 {
		_ = u.svc.Lib.ReplacePlaylistTracks(newID, paths)
	}
	u.toast(i18n.T("library.toast.duplicatedManual"))
	u.patchMain()
}

func (u *UI) libMovePlItem(idx, dir int) {
	s := u.lib()
	s.mu.Lock()
	if s.plCur.Kind != "manual" || idx < 0 || idx >= len(s.plItems) {
		s.mu.Unlock()
		return
	}
	j := idx + dir
	if j < 0 || j >= len(s.plItems) {
		s.mu.Unlock()
		return
	}
	paths := make([]string, len(s.plItems))
	for i, it := range s.plItems {
		paths[i] = it.Path
	}
	paths[idx], paths[j] = paths[j], paths[idx]
	id := s.plSel
	s.mu.Unlock()
	if u.svc.Lib != nil {
		_ = u.svc.Lib.ReplacePlaylistTracks(id, paths)
	}
	u.libReloadPlaylist(id)
}

func (u *UI) libRemovePlItem(path string) {
	s := u.lib()
	s.mu.Lock()
	id := s.plSel
	s.mu.Unlock()
	if u.svc.Lib != nil && id != 0 {
		_ = u.svc.Lib.RemoveFromPlaylist(id, path)
	}
	u.libReloadPlaylist(id)
}

func (u *UI) libReloadPlaylist(id int64) {
	if u.svc.Lib == nil || id == 0 {
		u.patchMain()
		return
	}
	row, _, _ := u.svc.Lib.PlaylistByID(id)
	items := u.libPlaylistItems(row)
	s := u.lib()
	s.mu.Lock()
	s.plCur, s.plItems = row, items
	s.mu.Unlock()
	u.patchMain()
}

// ── smart-rules editor ──

// libSmartOpen opens the rules editor (id 0 = create) seeded from the stored rules.
func (u *UI) libSmartOpen(id int64) {
	var row libdb.PlaylistRow
	if id != 0 {
		if u.svc.Lib == nil {
			return
		}
		r, ok, _ := u.svc.Lib.PlaylistByID(id)
		if !ok {
			return
		}
		row = r
	}
	rules, _ := libParseRules(row.Rules)
	s := u.lib()
	s.mu.Lock()
	u.libEnsureTracks(s)
	s.srID, s.srName, s.srRules = id, row.Name, rules
	s.srGenres = map[string]bool{}
	for _, g := range rules.Genres {
		s.srGenres[g] = true
	}
	h := u.libSmartModalHTML(s)
	s.mu.Unlock()
	u.openModal(h)
}

// libSRQuiet mutates the draft and live-patches only the match-count line (keeps input focus).
func (u *UI) libSRQuiet(mut func(*libSt)) {
	s := u.lib()
	s.mu.Lock()
	mut(s)
	txt := libSRCountText(s)
	s.mu.Unlock()
	u.eval("var c=document.getElementById('lib-sr-count');if(c)c.textContent=" + jsQuote(txt) + ";")
}

// libSRGenre toggles a genre chip (re-renders the modal for the active state).
func (u *UI) libSRGenre(g string) {
	s := u.lib()
	s.mu.Lock()
	if s.srGenres == nil {
		s.srGenres = map[string]bool{}
	}
	if s.srGenres[g] {
		delete(s.srGenres, g)
	} else {
		s.srGenres[g] = true
	}
	h := u.libSmartModalHTML(s)
	s.mu.Unlock()
	u.openModal(h)
}

// libSRFeel seeds the BPM band from a feel preset (energy proxy without audio analysis).
func (u *UI) libSRFeel(label string) {
	if label == "" {
		return
	}
	s := u.lib()
	s.mu.Lock()
	for _, f := range musiclib.FeelPresets() {
		if f.Label == label {
			s.srRules.BPMMin = f.BPMMin
			s.srRules.BPMMax = 0
			if f.BPMMax > 0 && f.BPMMax < 200 {
				s.srRules.BPMMax = f.BPMMax
			}
			break
		}
	}
	h := u.libSmartModalHTML(s)
	s.mu.Unlock()
	u.openModal(h)
}

// libSmartSave persists the draft: create, or rename + SetSmartRules on edit.
func (u *UI) libSmartSave() {
	if u.svc.Lib == nil {
		u.toast(i18n.T("library.dbUnavailable"))
		return
	}
	s := u.lib()
	s.mu.Lock()
	id, name, rules := s.srID, strings.TrimSpace(s.srName), s.srCurrent()
	s.mu.Unlock()
	if name == "" {
		u.toast(i18n.T("library.toast.smartNeedsName"))
		return
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return
	}
	if id == 0 {
		nid, cerr := u.svc.Lib.CreatePlaylist(name, libdb.PlaylistSmart, string(raw))
		if cerr != nil {
			u.toast(i18n.T("library.toast.createFailed") + cerr.Error())
			return
		}
		u.closeModal()
		u.toast(i18n.T("library.toast.createdName") + name)
		u.libOpenPlaylist(nid)
		u.patchMain()
		return
	}
	serr := u.svc.Lib.RenamePlaylist(id, name)
	if serr == nil {
		serr = u.svc.Lib.SetSmartRules(id, string(raw))
	}
	if serr != nil {
		u.toast(i18n.T("library.toast.saveFailed") + serr.Error())
		return
	}
	u.closeModal()
	u.toast(i18n.T("library.toast.savedRules"))
	u.libReloadPlaylist(id)
}

// ── cloud sync ──

func (u *UI) libSync(op string, id int64) {
	if u.svc.Syncer == nil {
		u.toast(i18n.T("library.toast.cloudUnavailable"))
		return
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		var err error
		switch op {
		case "push":
			err = u.svc.Syncer.PushPlaylist(ctx, id)
		case "pull":
			err = u.svc.Syncer.PullPlaylist(ctx, id)
		case "unlink":
			err = u.svc.Syncer.UnlinkPlaylist(id)
		case "all":
			var r any
			r, err = u.svc.Syncer.SyncAllPlaylists(ctx)
			_ = r
		}
		if err != nil {
			u.toast(i18n.T("library.toast.syncPrefix") + err.Error())
			return
		}
		u.toast(i18n.T("library.toast.syncOk"))
		u.patchMain()
	})
}

func (u *UI) libSyncModal() {
	if u.svc.Syncer == nil {
		u.toast(i18n.T("library.toast.cloudUnavailable"))
		return
	}
	u.toast(i18n.T("library.toast.fetchingCloud"))
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		ov, err := u.svc.Syncer.PlaylistOverviewCtx(ctx)
		if err != nil {
			u.toast(i18n.T("library.toast.overviewPrefix") + err.Error())
			return
		}
		var body strings.Builder
		body.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.label.localPlaylists")) + `</p>`)
		for _, p := range ov.Pairs {
			body.WriteString(itemRow(p.LocalName, i18n.T("library.label.syncCounts", i18n.A{"status": string(p.Status), "local": strconv.Itoa(p.LocalCount), "remote": strconv.Itoa(p.RemoteCount)}),
				btn(i18n.T("library.label.push"), "outline", fmt.Sprintf("lib-pl-push:%d", p.LocalID), ""), btn(i18n.T("library.label.pull"), "ghost", fmt.Sprintf("lib-pl-pull:%d", p.LocalID), "")))
		}
		if len(ov.RemoteOnly) > 0 {
			body.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.label.remoteOnly")) + `</p>`)
			for _, p := range ov.RemoteOnly {
				body.WriteString(itemRow(p.RemoteTitle, i18n.Tn("library.label.trackCount", p.RemoteCount),
					btn(i18n.T("library.label.import"), "outline", "lib-remote-import:"+p.RemoteID, "")))
			}
		}
		body.WriteString(btnRow(btn(i18n.T("library.pl.syncAll"), "primary", "lib-pl-syncall", "")))
		u.openModal(modal(i18n.T("library.modal.cloudSync"), body.String(), ""))
	})
}

func (u *UI) libRemoteImport(remoteID string) {
	u.closeModal()
	if u.svc.Syncer == nil {
		return
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		if _, err := u.svc.Syncer.ImportRemotePlaylist(ctx, remoteID); err != nil {
			u.toast(i18n.T("library.toast.importPrefix") + err.Error())
			return
		}
		u.toast(i18n.T("library.toast.importedRemote"))
		u.patchMain()
	})
}

// ── history ──

// libHistSources: smart-select options - all detected DJ-history sources.
func libHistSources() []ssOpt {
	out := []ssOpt{{Val: "", Label: i18n.T("library.label.allSources"), Sub: i18n.T("library.label.mergeHistory")}}
	if installs, err := musiclib.DiscoverTraktor(); err == nil {
		for _, in := range installs {
			if in.HistoryDir != "" {
				out = append(out, ssOpt{Val: "traktor", Label: "Traktor", Sub: in.HistoryDir, Badge: "NML"})
				break
			}
		}
	}
	for _, db := range rekordboxdb.DiscoverRekordboxMasterDB() {
		out = append(out, ssOpt{Val: db, Label: "Rekordbox", Sub: db, Badge: "master.db"})
	}
	return out
}

// libHistLoad merges play-history sessions from the picked source ("" = all):
// Traktor NML history + every Rekordbox master.db (djmdHistory), newest first.
func (u *UI) libHistLoad() {
	st := u.lib()
	st.mu.Lock()
	src := st.histSrc
	st.mu.Unlock()
	u.toast(i18n.T("library.toast.loadingHistory"))
	u.bg(func() {
		type tagged struct {
			s   musiclib.Session
			app string
		}
		var all []tagged
		if src == "" || src == "traktor" {
			if installs, err := musiclib.DiscoverTraktor(); err == nil && len(installs) > 0 && installs[0].HistoryDir != "" {
				if sessions, serr := musiclib.LoadSessions(installs[0].HistoryDir); serr == nil {
					for _, s := range sessions {
						all = append(all, tagged{s, "Traktor"})
					}
				} else {
					u.toast(i18n.T("library.toast.traktorHistory") + serr.Error())
				}
			}
		}
		if src == "" || (src != "traktor" && src != "") {
			dbs := rekordboxdb.DiscoverRekordboxMasterDB()
			if src != "" && src != "traktor" {
				dbs = []string{src}
			}
			for _, db := range dbs {
				lib, err := rekordboxdb.Open(db, "")
				if err != nil {
					u.toast(i18n.T("library.toast.rekordboxHistory") + err.Error())
					continue
				}
				for _, s := range lib.Sessions {
					all = append(all, tagged{s, "Rekordbox"})
				}
			}
		}
		if len(all) == 0 {
			u.toast(i18n.T("library.toast.noHistory"))
			return
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].s.StartedAt.After(all[j].s.StartedAt) })
		sessions := make([]musiclib.Session, len(all))
		apps := make([]string, len(all))
		sums := make([]musiclib.SessionSummary, len(all))
		for i, t := range all {
			sessions[i], apps[i] = t.s, t.app
			sums[i] = musiclib.Summarize(t.s)
		}
		s := u.lib()
		s.mu.Lock()
		s.sessions, s.summaries, s.histApps = sessions, sums, apps
		s.mu.Unlock()
		u.toast(i18n.Tn("library.toast.loadedSets", len(sessions)))
		u.libPatchBody()
	})
}

func (u *UI) libOpenSession(idx int) {
	s := u.lib()
	s.mu.Lock()
	if idx < 0 || idx >= len(s.sessions) {
		s.mu.Unlock()
		return
	}
	sess := s.sessions[idx]
	var rows []libPlay
	for _, p := range sess.Played {
		t, ok := s.byPath[p.Path]
		if !ok {
			t = musiclib.Track{Path: p.Path, Title: filepath.Base(p.Path), Artist: p.Artist, BPM: p.BPM, Key: p.Key}
		}
		rows = append(rows, libPlay{track: t, onDisk: pathOnDisk(p.Path)})
	}
	s.played, s.selSess = rows, idx
	s.mu.Unlock()
	u.libPatchBody()
}

// ── id marks ──

func (u *UI) libIDToggle(path, which string, on bool) {
	st := u.svc.IDMarks
	if st == nil {
		return
	}
	var cur idmark.Mark
	for _, e := range st.List() {
		if e.Path == path {
			cur = idmark.Mark{ShowArtist: e.ShowArtist, ShowLabel: e.ShowLabel}
			break
		}
	}
	if which == "artist" {
		cur.ShowArtist = on
	} else {
		cur.ShowLabel = on
	}
	st.Set(path, cur)
	u.libPatchBody()
}

func (u *UI) libMarkPathModal() {
	body := `<form data-act=lib-id-add class=mform><div class=pb-label>` + html.EscapeString(i18n.T("library.label.markPathHint")) + `</div>` +
		`<input class=field-input name=path placeholder="` + html.EscapeString(i18n.T("library.label.fullPath")) + `"><button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(i18n.T("library.label.mark")) + `</button></form>`
	u.openModal(modal(i18n.T("library.label.markAsID"), body, ""))
}

func (u *UI) libMarkAdd(f map[string]string) {
	u.closeModal()
	st := u.svc.IDMarks
	p := strings.TrimSpace(f["path"])
	if st == nil || p == "" {
		return
	}
	st.Set(p, idmark.Mark{})
	u.toast(i18n.T("library.toast.markedAsID"))
	u.libPatchBody()
}

func (u *UI) libMark(path string, mark bool) {
	u.closeModal()
	st := u.svc.IDMarks
	if st == nil {
		return
	}
	if mark {
		st.Set(path, idmark.Mark{})
		u.toast(i18n.T("library.toast.markedAsID"))
	} else {
		st.Remove(path)
		u.toast(i18n.T("library.toast.idMarkRemoved"))
	}
	u.libPatchBody()
}

// ── tags ──

func (u *UI) libTagsWrite(path string) {
	if u.svc.Lib == nil {
		return
	}
	if !tagwrite.Supported(path) {
		u.toast(i18n.T("library.toast.tagsUnsupported"))
		return
	}
	s := u.lib()
	s.mu.Lock()
	t, ok := s.byPath[path]
	if !ok && s.sel != nil {
		t = s.sel.track
	}
	s.mu.Unlock()
	u.bg(func() {
		if _, err := tagsync.Apply(u.svc.Lib, t); err != nil {
			u.toast(i18n.T("library.toast.writeTagsPrefix") + err.Error())
			return
		}
		u.toast(i18n.T("library.toast.tagsWritten"))
		u.libPatchDetail()
	})
}

func (u *UI) libTagsRevert(path string) {
	if u.svc.Lib == nil {
		return
	}
	u.bg(func() {
		if err := tagsync.Revert(u.svc.Lib, path); err != nil {
			u.toast(i18n.T("library.toast.revertPrefix") + err.Error())
			return
		}
		u.toast(i18n.T("library.toast.tagsReverted"))
		u.libPatchDetail()
	})
}

// ── batch ──

func (u *UI) libBatchRun(kind string) {
	switch kind {
	case "transcode":
		u.libBatchTranscodeModal()
	case "writetags":
		u.toast(i18n.T("library.toast.writeTagsHint"))
	default:
		u.libBatchAnalyze(kind)
	}
}

func (u *UI) libBatchAnalyze(kind string) {
	if u.svc.Workers == nil {
		u.toast(i18n.T("library.toast.workerUnavailable"))
		return
	}
	s := u.lib()
	s.mu.Lock()
	var files []string
	for p := range s.batch {
		files = append(files, p)
	}
	s.mu.Unlock()
	sort.Strings(files)
	if len(files) == 0 {
		return
	}
	labels := map[string]string{"peaks": i18n.T("library.batch.waveforms"), "tags": i18n.T("library.batch.tags"), "fingerprint": i18n.T("library.batch.fingerprint")}
	methods := map[string]string{"peaks": "probe.peaks", "tags": "probe.tags", "fingerprint": "fingerprint.compute"}
	wtype := "probe"
	if kind == "fingerprint" {
		wtype = "fingerprint"
	}
	jid := fmt.Sprintf("libbatch-%d", time.Now().UnixNano())
	j := &libJob{id: jid, name: i18n.Tn("library.label.filesCount", len(files)), preset: labels[kind], status: "running"}
	s.jobsMu.Lock()
	s.jobs = append([]*libJob{j}, s.jobs...)
	s.jobsMu.Unlock()
	u.mu.Lock()
	u.libSection = "queue"
	u.mu.Unlock()
	u.patchMain()
	u.bg(func() {
		for i, p := range files {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			params := map[string]any{"path": p}
			if kind == "peaks" {
				params["buckets"] = 8192
			}
			if _, err := u.svc.Workers.RunBackground(ctx, wtype, methods[kind], params); err != nil {
				u.logErr("batch "+kind, err)
			}
			cancel()
			frac := float64(i+1) / float64(len(files)) * 100
			u.libJobUpd(j, func() { j.pct = frac })
		}
		u.libJobUpd(j, func() { j.status = "done"; j.pct = 100 })
		u.toast(i18n.T("library.toast.batchDone", i18n.A{"kind": labels[kind]}))
	})
}

func (u *UI) libBatchTranscodeModal() {
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	body := `<p class=page-sub>` + html.EscapeString(i18n.T("library.modal.batchTranscodeDesc")) + `</p><div class=seg>`
	for _, p := range transcode.AllPresets(custom) {
		body += btn(p.Label, "outline", "lib-batch-tc:"+p.ID, "")
	}
	body += `</div>`
	u.openModal(modal(i18n.T("library.modal.batchTranscode"), body, ""))
}

func (u *UI) libBatchTranscode(id string) {
	u.closeModal()
	p, ok := u.findPreset(id)
	if !ok {
		return
	}
	s := u.lib()
	s.mu.Lock()
	var files []string
	for f := range s.batch {
		files = append(files, f)
	}
	s.mu.Unlock()
	for _, f := range files {
		u.libStartTranscode(f, p, "", "")
	}
	u.mu.Lock()
	u.libSection = "queue"
	u.mu.Unlock()
	u.patchMain()
	u.toast(i18n.Tn("library.toast.queuedTranscodes", len(files)))
}

// ── file ops (context menu) ──

func (u *UI) libCtxModal(path string) {
	marked := false
	if u.svc.IDMarks != nil {
		marked = u.svc.IDMarks.IsMarked(path)
	}
	markBtn := btn(i18n.T("library.label.markAsID"), "outline", "lib-mark:"+path, "")
	if marked {
		markBtn = btn(i18n.T("library.label.unmarkID"), "outline", "lib-unmark:"+path, "")
	}
	body := btnRow(btn(i18n.T("library.label.renameEllipsis"), "outline", "lib-rename:"+path, ""), btn(i18n.T("library.label.moveEllipsis"), "outline", "lib-move:"+path, ""),
		btn(i18n.T("library.label.deleteEllipsis"), "destructive", "lib-del:"+path, "")) +
		btnRow(btn(i18n.T("library.copyPath"), "ghost", "copy", ""), btn(i18n.T("library.reveal"), "ghost", "lib-reveal:"+path, ""), markBtn)
	u.openModal(modal(filepath.Base(path), body, ""))
}

func (u *UI) libRenameModal(path string) {
	body := fmt.Sprintf(`<form data-act=lib-rename-do class=mform>%s%s<button class="rp-btn rp-btn--primary" type=submit>%s</button></form>`,
		hiddenField("path", path), labeledInput("name", i18n.T("library.label.newName"), filepath.Base(path)), html.EscapeString(i18n.T("library.pl.rename")))
	u.openModal(modal(i18n.T("library.pl.rename"), body, ""))
}

func (u *UI) libFileRename(f map[string]string) {
	u.closeModal()
	path, name := f["path"], strings.TrimSpace(f["name"])
	if path == "" || name == "" {
		return
	}
	dst := filepath.Join(filepath.Dir(path), name)
	if err := os.Rename(path, dst); err != nil {
		u.toast(i18n.T("library.toast.renameFailed") + err.Error())
		return
	}
	u.toast(i18n.T("library.toast.renamed"))
	u.libPatchBody()
}

func (u *UI) libMoveModal(path string) {
	s := u.lib()
	s.mu.Lock()
	if path != "" { // "" = re-render with current draft (after a pick)
		s.movePath, s.moveDir = path, filepath.Dir(path)
	}
	src, dir := s.movePath, s.moveDir
	s.mu.Unlock()
	body := `<p class=page-sub>` + html.EscapeString(i18n.T("library.confirm.moveTo", i18n.A{"name": filepath.Base(src)})) + `</p>` +
		`<div class=lib-toolbar>` + fieldRaw("lib-move-dir", dir, i18n.T("library.label.destFolder")) +
		btn(i18n.T("common.browse"), "ghost", "pick-dir:lib-move-dir", "") + `</div>` +
		btnRow(btn(i18n.T("library.label.move"), "primary", "lib-move-go", ""), btn(i18n.T("common.cancel"), "outline", "modal-close", ""))
	u.openModal(modal(i18n.T("library.modal.moveToFolder"), body, ""))
}

// libMoveDir stores a typed/picked destination and refreshes the modal.
func (u *UI) libMoveDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	u.libSetQuiet(func(s *libSt) { s.moveDir = strings.TrimSpace(dir) })
	u.libMoveModal("")
}

func (u *UI) libFileMove() {
	u.closeModal()
	s := u.lib()
	s.mu.Lock()
	path, dir := s.movePath, strings.TrimSpace(s.moveDir)
	s.mu.Unlock()
	if path == "" || dir == "" {
		return
	}
	if err := os.Rename(path, filepath.Join(dir, filepath.Base(path))); err != nil {
		u.toast(i18n.T("library.toast.moveFailed") + err.Error())
		return
	}
	u.toast(i18n.T("library.toast.moved"))
	u.libPatchBody()
}

func (u *UI) libDelModal(path string) {
	body := `<p class=page-sub>` + html.EscapeString(i18n.T("library.confirm.deleteFile", i18n.A{"name": filepath.Base(path)})) + `</p>` +
		btnRow(btn(i18n.T("common.delete"), "destructive", "lib-del-do", path), btn(i18n.T("common.cancel"), "outline", "modal-close", ""))
	u.openModal(modal(i18n.T("library.modal.deleteFile"), body, ""))
}

func (u *UI) libFileDelete(path string) {
	u.closeModal()
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil {
		u.toast(i18n.T("library.toast.deleteFailed") + err.Error())
		return
	}
	u.toast(i18n.T("library.toast.deleted"))
	u.libPatchBody()
}

// ── tiny helpers ──

func atoi(s string) int     { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
func atoi64(s string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }
func atof(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }

func hiddenField(name, val string) string {
	return `<input type=hidden name="` + html.EscapeString(name) + `" value="` + html.EscapeString(val) + `">`
}

func labeledInput(name, label, val string) string {
	return `<div class=pb-field><div class=pb-label>` + html.EscapeString(label) + `</div>` +
		`<input class=field-input name="` + html.EscapeString(name) + `" value="` + html.EscapeString(val) + `"></div>`
}

// selNamed renders a form <select name=…> (for modal preset forms).
func selNamed(name, current string, opts [][2]string) string {
	var o strings.Builder
	for _, op := range opts {
		sel := ""
		if op[0] == current {
			sel = " selected"
		}
		fmt.Fprintf(&o, `<option value="%s"%s>%s</option>`, html.EscapeString(op[0]), sel, html.EscapeString(op[1]))
	}
	return `<div class=pb-field><div class=pb-label>` + html.EscapeString(strings.ToUpper(name)) + `</div>` +
		`<select class="field-input select-input" name="` + html.EscapeString(name) + `">` + o.String() + `</select></div>`
}
