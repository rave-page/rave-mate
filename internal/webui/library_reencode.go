package webui

// Batch re-encode (task #76): duplicate a whole folder or playlist into a NEW directory
// in another format (the FLAC → CDJ-safe workflow) - filenames kept, extension swapped,
// subfolder structure mirrored, existing outputs skipped. The per-file encoder panel
// stays available but folds away in collection/playlist contexts (render_library.go).

import (
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/transcode"
)

type reencSt struct {
	mu     sync.Mutex
	kind   string // "dir" | "pl"
	src    string // kind=dir: source folder
	plID   int64  // kind=pl
	plName string
	preset string // transcode preset id
	dest   string // destination folder
}

// reencOpenDir opens the modal for a whole folder.
func (u *UI) reencOpenDir(dir string) {
	r := &u.re
	r.mu.Lock()
	r.kind, r.src, r.plID, r.plName = "dir", dir, 0, ""
	if r.preset == "" {
		r.preset = u.reencDefaultPreset()
	}
	r.dest = dir + "-" + r.preset
	r.mu.Unlock()
	u.openModal(u.reencModalHTML())
}

// reencOpenPl opens the modal for a playlist.
func (u *UI) reencOpenPl(id int64) {
	if u.svc.Lib == nil {
		return
	}
	pl, ok, err := u.svc.Lib.PlaylistByID(id)
	if err != nil || !ok {
		return
	}
	r := &u.re
	r.mu.Lock()
	r.kind, r.src, r.plID, r.plName = "pl", "", id, pl.Name
	if r.preset == "" {
		r.preset = u.reencDefaultPreset()
	}
	r.dest = filepath.Join(u.libDirOr(), reencSafeName(pl.Name)+"-"+r.preset)
	r.mu.Unlock()
	u.openModal(u.reencModalHTML())
}

// reencDefaultPreset picks the first audio-only preset.
func (u *UI) reencDefaultPreset() string {
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	for _, p := range transcode.AllPresets(custom) {
		if p.IsAudioOnly() {
			return p.ID
		}
	}
	return "remux"
}

func (u *UI) reencModalHTML() string {
	r := &u.re
	r.mu.Lock()
	kind, src, plName, preset, dest := r.kind, r.src, r.plName, r.preset, r.dest
	r.mu.Unlock()
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	srcLabel := src
	if kind == "pl" {
		srcLabel = plName
	}
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.re.desc")) + `</p>`)
	b.WriteString(kv(i18n.T("library.re.source"), srcLabel))
	b.WriteString(smartSelect("re-preset", i18n.T("library.enc.preset"), "re-preset", preset, func() []ssOpt {
		var out []ssOpt
		for _, p := range transcode.AllPresets(custom) {
			if !p.IsAudioOnly() {
				continue
			}
			out = append(out, ssOpt{Val: p.ID, Label: p.Label, Sub: p.Desc, Badge: strings.ToUpper(p.Container)})
		}
		return out
	}))
	b.WriteString(`<div class=lib-toolbar>` +
		field(i18n.T("library.re.dest"), "re-dest", dest, "text") +
		btn(i18n.T("common.browse"), "ghost", "pick-dir:re-dest", "") + `</div>`)
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.re.note")) + `</p>`)
	footer := btnRow(btn(i18n.T("library.re.start"), "primary", "re-start", ""),
		btn(i18n.T("common.cancel"), "ghost", "modal-close", ""))
	return modal(i18n.T("library.re.title"), b.String(), footer)
}

// reencFiles resolves the source file list (dir = recursive audio walk).
func (u *UI) reencFiles() []string {
	r := &u.re
	r.mu.Lock()
	kind, src, plID := r.kind, r.src, r.plID
	r.mu.Unlock()
	var files []string
	switch kind {
	case "dir":
		_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: skip, keep walking
			}
			if !d.IsDir() && pubIsAudio(p) {
				files = append(files, p)
			}
			return nil
		})
	case "pl":
		if u.svc.Lib != nil {
			paths, _ := u.svc.Lib.PlaylistTracks(plID)
			for _, p := range paths {
				if pubIsAudio(p) && pathOnDisk(p) {
					files = append(files, p)
				}
			}
		}
	}
	sort.Strings(files)
	return files
}

// reencStart enqueues one transcode per source file into the destination folder.
func (u *UI) reencStart() {
	r := &u.re
	r.mu.Lock()
	kind, src, dest, presetID := r.kind, r.src, r.dest, r.preset
	r.mu.Unlock()
	pre, ok := u.findPreset(presetID)
	if !ok || dest == "" {
		return
	}
	u.closeModal()
	u.bg(func() { // WalkDir source resolution + enqueue loop off the actWorker
		files := u.reencFiles()
		if len(files) == 0 {
			u.toast(i18n.T("library.re.noFiles"))
			return
		}
		queued, skipped := 0, 0
		for _, f := range files {
			base := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
			out := filepath.Join(dest, base+pre.Ext())
			if kind == "dir" { // mirror the subfolder structure
				if rel, err := filepath.Rel(src, f); err == nil {
					out = filepath.Join(dest, strings.TrimSuffix(rel, filepath.Ext(rel))+pre.Ext())
				}
			}
			if pathOnDisk(out) {
				skipped++ // already re-encoded on a previous run
				continue
			}
			u.libStartTranscodeTo(f, out, pre, "", "")
			queued++
		}
		if u.stopped() {
			return
		}
		u.mu.Lock()
		u.libSection = "queue"
		u.mu.Unlock()
		u.patchMain()
		u.toast(i18n.T("library.re.queuedToast", i18n.A{"queued": fmt.Sprint(queued), "skipped": fmt.Sprint(skipped)}))
	})
}

// ── playlist-context detection (per-file encoder demotion) ──

// libEncDemoted: the per-file encoder folds away when the file sits in a playlist
// context - the collection/playlists sections, or (in Browse) a folder that is marked
// as a playlist (imported playlist folder or manual mark).
func (u *UI) libEncDemoted(path string) bool {
	switch u.libSectionOr() {
	case "collection", "playlists", "history":
		return true
	case "browse":
		if u.svc.Lib == nil {
			return false
		}
		dir := filepath.Clean(filepath.Dir(path))
		pls, _ := u.svc.Lib.ListPlaylists()
		for _, p := range pls {
			if p.Folder != "" && filepath.Clean(p.Folder) == dir {
				return true
			}
		}
	}
	return false
}

// libMarkDirPlaylist creates a manual playlist bound to the folder (its audio files,
// non-recursive) - the "mark directory as playlist" gesture.
func (u *UI) libMarkDirPlaylist(dir string) {
	if u.svc.Lib == nil {
		return
	}
	var files []string
	ents, err := os.ReadDir(dir)
	if err != nil {
		u.toast(i18n.T("library.browse.cannotRead", i18n.A{"path": dir}))
		return
	}
	for _, e := range ents {
		if !e.IsDir() {
			if p := filepath.Join(dir, e.Name()); pubIsAudio(p) {
				files = append(files, p)
			}
		}
	}
	if len(files) == 0 {
		u.toast(i18n.T("library.re.noFiles"))
		return
	}
	sort.Strings(files)
	id, err := u.svc.Lib.CreateFolderPlaylist(filepath.Base(dir), dir, files)
	if err != nil {
		u.toast(err.Error())
		return
	}
	_ = id
	if u.stopped() {
		return
	}
	u.toast(i18n.T("library.re.markedToast", i18n.A{"name": filepath.Base(dir), "n": fmt.Sprint(len(files))}))
	u.libPatchBody()
}

func init() {
	onExact("lib-enc-open", func(u *UI, _ actMsg) {
		s := u.lib()
		s.mu.Lock()
		s.encOpen = true
		s.mu.Unlock()
		u.libPatchDetail()
	})
	onExact("lib-reenc-dir", func(u *UI, _ actMsg) { u.reencOpenDir(u.libDirOr()) })
	onPrefix("lib-reenc-pl:", func(u *UI, m actMsg) { u.reencOpenPl(int64(atoi(m.arg("lib-reenc-pl:")))) })
	onExact("lib-markpl", func(u *UI, _ actMsg) {
		dir := u.libDirOr()
		u.bg(func() { u.libMarkDirPlaylist(dir) }) // os.ReadDir + DB write off the actWorker
	})
	onExact("re-preset", func(u *UI, m actMsg) {
		u.re.mu.Lock()
		old := u.re.preset
		u.re.preset = m.Val
		// keep the default "<src>-<preset>" dest in step; a hand-edited dest is untouched
		if strings.HasSuffix(u.re.dest, "-"+old) {
			u.re.dest = strings.TrimSuffix(u.re.dest, "-"+old) + "-" + m.Val
		}
		u.re.mu.Unlock()
		u.openModal(u.reencModalHTML())
	})
	onExact("re-dest", func(u *UI, m actMsg) {
		u.re.mu.Lock()
		u.re.dest = strings.TrimSpace(m.Val)
		u.re.mu.Unlock()
		u.openModal(u.reencModalHTML()) // re-open so a pick-dir'd path shows in the field
	})
	onExact("re-start", func(u *UI, _ actMsg) { u.reencStart() })
}

// reencSafeName strips path-hostile characters from a playlist name.
func reencSafeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
}
