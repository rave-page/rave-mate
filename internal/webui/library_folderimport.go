package webui

// Folder import: add a loose directory to the collection without any DJ software - scan
// the folder for audio files, read tags via the out-of-process probe worker (filename
// fallback), persist them as a synthetic "folder" source + a folder-bound playlist, and
// optionally beatgrid the new tracks right away (results saved straight into libdb, so
// waveform/cue editor work without Traktor). "Send to Traktor" exports any manual playlist
// back into collection.nml (new ENTRYs incl. TEMPO/grid) when the user wants it there.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/cuewriteback"
	"rave.page/mate/internal/gridfix"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// fiMaxFiles bounds one scan/import batch (paths only, ~100 B each → ~2.5 MB worst case;
// drop policy: scan stops at the cap and the modal says so).
const fiMaxFiles = 25000

type fiState struct {
	mu        sync.Mutex
	dir       string
	recursive bool
	scanning  bool
	files     []string // last scan result (≤ fiMaxFiles)
	capped    bool     // scan hit fiMaxFiles
	scanGen   int      // drops stale async scan results
	running   bool     // an import is in flight (serialize)
}

// ── modal ──

func (u *UI) fiOpen(dir string) {
	f := &u.fi
	f.mu.Lock()
	if dir != "" {
		f.dir = dir
	}
	f.mu.Unlock()
	u.openModal(u.fiModalHTML())
	u.fiScan()
}

func (u *UI) fiModalHTML() string {
	f := &u.fi
	f.mu.Lock()
	dir, rec := f.dir, f.recursive
	f.mu.Unlock()
	body := `<p class=page-sub>` + esc(i18n.T("library.fi.desc")) + `</p>` +
		`<div class=lib-toolbar>` + fieldRaw("fi-dir", dir, i18n.T("library.fi.folder")) +
		btn(i18n.T("common.browse"), "ghost", "pick-dir:fi-dir", "") + `</div>` +
		toggleRow(i18n.T("library.fi.recursive"), "fi-recursive", rec) +
		`<div id=fi-scan>` + u.fiScanHTML() + `</div>`
	return modal(i18n.T("library.fi.title"), body, "")
}

// fiScanHTML is the patched fragment below the folder field: scan status + the
// action-bound import buttons (each states exactly what will happen).
func (u *UI) fiScanHTML() string {
	f := &u.fi
	f.mu.Lock()
	dir, scanning, n, capped := f.dir, f.scanning, len(f.files), f.capped
	f.mu.Unlock()
	var b strings.Builder
	switch {
	case dir == "":
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.fi.pickHint")) + `</div>`)
	case scanning:
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.fi.scanning")) + `</div>`)
	case n == 0:
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.re.noFiles")) + `</div>`)
	default:
		b.WriteString(`<div class=set-note>` + esc(i18n.Tn("library.fi.found", n)) + `</div>`)
		if capped {
			b.WriteString(`<div class=set-note>` + esc(i18n.T("library.fi.cappedNote", i18n.A{"cap": fmt.Sprint(fiMaxFiles)})) + `</div>`)
		}
		row := btn(i18n.Tn("library.fi.importBtn", n), "primary", "fi-import", "")
		if u.svc.Cfg != nil && u.svc.Cfg.Features.GridFix.Enabled {
			row += btn(i18n.Tn("library.fi.importGridBtn", n), "outline", "fi-import-grid", "")
		}
		b.WriteString(btnRow(row))
	}
	b.WriteString(btnRow(btn(i18n.T("common.cancel"), "ghost", "modal-close", "")))
	return b.String()
}

// fiScan re-lists the chosen folder off the actWorker and patches the modal fragment.
func (u *UI) fiScan() {
	f := &u.fi
	f.mu.Lock()
	dir, rec := f.dir, f.recursive
	f.scanGen++
	gen := f.scanGen
	f.scanning, f.files, f.capped = dir != "", nil, false
	f.mu.Unlock()
	if dir == "" {
		return
	}
	u.bg(func() {
		files, capped := fiScanDir(dir, rec)
		f.mu.Lock()
		if f.scanGen != gen { // a newer scan superseded this one
			f.mu.Unlock()
			return
		}
		f.scanning, f.files, f.capped = false, files, capped
		f.mu.Unlock()
		if u.stopped() {
			return
		}
		u.eval("window.__patch('fi-scan'," + jsQuote(u.fiScanHTML()) + ")")
	})
}

// fiScanDir lists the folder's audio files, sorted. Non-recursive mirrors
// libMarkDirPlaylist; recursive walks subfolders (dot-dirs skipped). capped=true when
// the fiMaxFiles bound stopped the scan.
func fiScanDir(dir string, recursive bool) (files []string, capped bool) {
	if !recursive {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil, false
		}
		for _, e := range ents {
			if !e.IsDir() {
				if p := filepath.Join(dir, e.Name()); pubIsAudio(p) {
					if len(files) >= fiMaxFiles {
						return files, true
					}
					files = append(files, p)
				}
			}
		}
		sort.Strings(files)
		return files, false
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, keep walking
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if pubIsAudio(p) {
			if len(files) >= fiMaxFiles {
				capped = true
				return filepath.SkipAll
			}
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files, capped
}

// ── import run ──

// fiRun probes + persists the scanned files; beatgrid=true additionally runs the gridfix
// batch on them and saves the created grids into libdb (the button label announced both).
func (u *UI) fiRun(beatgrid bool) {
	u.closeModal()
	f := &u.fi
	f.mu.Lock()
	dir := f.dir
	files := append([]string(nil), f.files...)
	if f.running {
		f.mu.Unlock()
		u.toast(i18n.T("library.fi.alreadyRunning"))
		return
	}
	f.running = true
	f.mu.Unlock()
	release := func() { f.mu.Lock(); f.running = false; f.mu.Unlock() }
	if dir == "" || len(files) == 0 || u.svc.Lib == nil {
		release()
		if u.svc.Lib == nil {
			u.toast(i18n.T("library.dbUnavailable"))
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &libJob{id: fmt.Sprintf("folderimp-%d", time.Now().UnixNano()),
		name: filepath.Base(dir), preset: i18n.T("library.fi.jobLabel"), status: "running", cancel: cancel}
	s := u.lib()
	s.jobsMu.Lock()
	s.jobs = append([]*libJob{j}, s.jobs...)
	s.jobsMu.Unlock()
	u.mu.Lock()
	u.libSection = "queue"
	u.mu.Unlock()
	u.patchMain()
	u.bg(func() {
		defer release()
		defer cancel()
		tracks := u.fiProbe(ctx, files, j)
		if ctx.Err() != nil {
			u.libJobUpd(j, func() { j.status = "cancelled" })
			return
		}
		if err := u.fiPersist(dir, tracks); err != nil {
			u.libJobUpd(j, func() { j.status = "error"; j.msg = err.Error() })
			u.toast(i18n.T("library.fi.failedToast") + err.Error())
			return
		}
		u.libJobUpd(j, func() { j.status = "done"; j.pct = 100 })
		u.libReload()
		u.toast(i18n.Tn("library.fi.importedToast", len(tracks), i18n.A{"name": filepath.Base(dir)}))
		if beatgrid {
			byPath := make(map[string]musiclib.Track, len(tracks))
			for _, t := range tracks {
				byPath[t.Path] = t
			}
			u.gfRunTracksHook(tracks, "selected", false, func(res []gridfix.TrackResult) {
				u.fiSaveGrids(byPath, res)
			})
		}
	})
}

// fiProbe tag-reads every file via the probe worker (serial; the worker pool caps
// concurrency anyway), with filename fallback. Bounded by len(files) ≤ fiMaxFiles.
func (u *UI) fiProbe(ctx context.Context, files []string, j *libJob) []musiclib.Track {
	out := make([]musiclib.Track, 0, len(files))
	for i, p := range files {
		if ctx.Err() != nil {
			break
		}
		out = append(out, u.fiProbeOne(ctx, p))
		frac := float64(i+1) / float64(len(files)) * 100
		u.libJobUpd(j, func() { j.pct = frac })
	}
	return out
}

// fiProbeOne builds one Track: probe.tags (ffprobe + embedded tags, out-of-process) when
// the worker pool is up, else/additionally an "Artist - Title" filename split.
func (u *UI) fiProbeOne(ctx context.Context, path string) musiclib.Track {
	var t musiclib.Track
	if u.svc.Workers != nil {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		raw, err := u.svc.Workers.RunBackground(c, "probe", "probe.tags", map[string]any{"path": path})
		cancel()
		if err == nil {
			_ = json.Unmarshal(raw, &t)
		}
	}
	t.Path = path
	if strings.TrimSpace(t.Title) == "" {
		ar, ti := fiSplitName(filepath.Base(path))
		if t.Artist == "" {
			t.Artist = ar
		}
		t.Title = ti
	}
	if t.FileSizeKB == 0 {
		if fi, err := os.Stat(path); err == nil {
			t.FileSizeKB = int(fi.Size() / 1024)
		}
	}
	if t.ImportDate == "" {
		t.ImportDate = time.Now().Format("2006/1/2")
	}
	return t
}

// fiSplitName derives (artist, title) from a file name: extension stripped, a leading
// track number removed only when a ./-/_ separator follows ("01. ", "03 - ", "12_") so
// "2 Unlimited - …" keeps its artist, then split on the first " - ". No separator →
// title only.
func fiSplitName(base string) (artist, title string) {
	name := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i > 0 && i <= 3 && i < len(name) {
		sep := strings.IndexFunc(name[i:], func(r rune) bool { return !strings.ContainsRune(" .-_", r) })
		if sep > 0 && strings.ContainsAny(name[i:i+sep], ".-_") {
			name = name[i+sep:]
		}
	}
	if a, t, ok := strings.Cut(name, " - "); ok {
		a, t = strings.TrimSpace(a), strings.TrimSpace(t)
		if a != "" && t != "" {
			return a, t
		}
	}
	return "", name
}

// fiPersist upserts the probed tracks under the synthetic "folder" source (EnsureSource:
// no imported_at stamp, so a folder import never becomes FirstSource for remotectl/Fyne)
// and binds/refreshes the folder playlist. Re-importing the same folder refresh-syncs its
// source: files gone from disk drop out.
func (u *UI) fiPersist(dir string, tracks []musiclib.Track) error {
	db := u.svc.Lib
	srcID, err := db.EnsureSource("folder", dir)
	if err != nil {
		return err
	}
	sy, err := db.BeginTrackSync(srcID)
	if err != nil {
		return err
	}
	for _, t := range tracks {
		if e := sy.Add(t); e != nil {
			sy.Rollback()
			return e
		}
	}
	if _, err := sy.Commit(); err != nil {
		return err
	}
	paths := make([]string, 0, len(tracks))
	for _, t := range tracks {
		paths = append(paths, t.Path)
	}
	// bind the folder playlist (top-up when it already exists - the mark gesture ran before)
	d := filepath.Clean(dir)
	if pls, err := db.ListPlaylists(); err == nil {
		for _, p := range pls {
			if p.Folder != "" && filepath.Clean(p.Folder) == d {
				_, err := db.AddToPlaylist(p.ID, paths...)
				return err
			}
		}
	}
	_, err = db.CreateFolderPlaylist(filepath.Base(dir), dir, paths)
	return err
}

// fiSaveGrids writes the batch's FIX plans (created or corrected grids) straight into the
// libdb track rows - folder tracks have no DJ-software file for the gridfix Apply router.
func (u *UI) fiSaveGrids(byPath map[string]musiclib.Track, results []gridfix.TrackResult) {
	if u.svc.Lib == nil {
		return
	}
	n := 0
	for _, r := range results {
		if r.Err != "" || r.Plan.Status != gridfix.StatusFix {
			continue
		}
		t, ok := byPath[r.Path]
		if !ok {
			t = musiclib.Track{Path: r.Path}
		}
		grid := []musiclib.GridMarker{{PositionMs: r.Plan.NewStartS * 1000, BPM: r.Plan.NewBPM}}
		if err := u.svc.Lib.UpdateTrackGrid(t, r.Plan.NewBPM, grid); err != nil {
			u.logErr("save grid", err)
			continue
		}
		n++
	}
	if n > 0 {
		u.toast(i18n.Tn("library.fi.gridSavedToast", n))
		u.libReload()
	}
}

// fiPersistLoose top-ups the collection with files a folder-playlist refresh just picked
// up: only paths not yet in ANY source are probed + added (additive - never deletes),
// under the folder source of their dominant dir. Returns how many were added.
func (u *UI) fiPersistLoose(dir string, paths []string) int {
	db := u.svc.Lib
	if db == nil || dir == "" {
		return 0
	}
	var fresh []string
	for _, p := range paths {
		if ok, err := db.HasTrackPath(p); err == nil && !ok {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) == 0 {
		return 0
	}
	srcID, err := db.EnsureSource("folder", dir)
	if err != nil {
		u.logErr("folder source", err)
		return 0
	}
	// Probe BEFORE opening the write txn: probing N files takes seconds-to-minutes, and a
	// txn held open that long starves every other DB writer (busy_timeout 5s).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracks := make([]musiclib.Track, 0, len(fresh))
	for _, p := range fresh {
		tracks = append(tracks, u.fiProbeOne(ctx, p))
	}
	sy, err := db.BeginTrackUpsert(srcID)
	if err != nil {
		u.logErr("begin upsert", err)
		return 0
	}
	n := 0
	for _, t := range tracks {
		if e := sy.Add(t); e != nil {
			sy.Rollback()
			u.logErr("track add", e)
			return 0
		}
		n++
	}
	if _, err := sy.Commit(); err != nil {
		u.logErr("commit", err)
		return 0
	}
	return n
}

// ── send a playlist to Traktor ──

// libPlSendTraktor exports a playlist into collection.nml: backup → merge tracks as new
// ENTRYs (incl. TEMPO + grid/cues from libdb) → upsert the NML playlist. Guarded against
// a running Traktor (it rewrites the NML from memory on exit).
func (u *UI) libPlSendTraktor(id int64) {
	u.closeModal()
	if u.svc.Lib == nil {
		u.toast(i18n.T("library.dbUnavailable"))
		return
	}
	p, ok, err := u.svc.Lib.PlaylistByID(id)
	if err != nil || !ok {
		return
	}
	nml := u.gfNMLPath()
	if nml == "" {
		u.toast(i18n.T("library.gf.noNml"))
		return
	}
	u.toast(i18n.T("library.pl.sendingTraktor"))
	u.bg(func() {
		if cuewriteback.TraktorRunning() {
			u.toast(i18n.T("library.gf.traktorRunning"))
			return
		}
		paths, _ := u.svc.Lib.PlaylistTracks(id)
		div, _ := u.svc.Lib.DividerPaths()
		clean := make([]string, 0, len(paths))
		for _, x := range paths {
			if !div[x] {
				clean = append(clean, x)
			}
		}
		if len(clean) == 0 {
			u.toast(i18n.T("library.re.noFiles"))
			return
		}
		// safety: full collection backup before the write (mirrors gfApplyTo)
		if installs, derr := musiclib.DiscoverTraktor(); derr == nil && len(installs) > 0 && installs[0].Collection != "" {
			if _, berr := musiclib.BackupCollection(installs[0], libBackupRoot()); berr != nil {
				u.toast(i18n.T("library.gf.backupFailed") + berr.Error())
				return
			}
		} else if berr := gfBackupFile("traktor", nml); berr != nil {
			u.toast(i18n.T("library.gf.backupFailed") + berr.Error())
			return
		}
		all, _ := u.svc.Lib.LoadAllTracks()
		byPath := make(map[string]musiclib.Track, len(all))
		for _, t := range all {
			byPath[t.Path] = t
		}
		updates := make([]musiclib.Track, 0, len(clean))
		for _, x := range clean {
			if t, ok := byPath[x]; ok {
				updates = append(updates, t)
			} else {
				ar, ti := fiSplitName(filepath.Base(x))
				updates = append(updates, musiclib.Track{Path: x, Artist: ar, Title: ti})
			}
		}
		res, merr := musiclib.MergeIntoCollectionFile(nml, updates)
		if merr != nil {
			u.toast(i18n.T("library.pl.sendTraktorFailed") + gfWriteErr(merr).Error())
			return
		}
		// playlist AFTER the merge - UpsertNMLPlaylist skips paths without a COLLECTION entry
		plAdded, perr := musiclib.UpsertNMLPlaylist(nml, p.Name, clean)
		if perr != nil {
			u.toast(i18n.T("library.pl.sendTraktorFailed") + gfWriteErr(perr).Error())
			return
		}
		u.toast(i18n.T("library.pl.sentTraktorToast", i18n.A{
			"added": fmt.Sprint(res.Added), "updated": fmt.Sprint(res.Updated),
			"name": p.Name, "pl": fmt.Sprint(plAdded)}))
	})
}

// libPlCanSendTraktor: any manual playlist - folder-bound (the folder-import / mark gesture)
// or hand-curated. Imported ones already live in their source app; smart ones have no
// playlist_tracks rows to send.
func libPlCanSendTraktor(p libdb.PlaylistRow) bool {
	return p.Kind == libdb.PlaylistManual
}

// ── actions ──

func init() {
	onExact("lib-folderimp", func(u *UI, _ actMsg) { u.fiOpen(u.libDirOr()) })
	onPrefix("fi-open:", func(u *UI, m actMsg) { u.fiOpen(m.arg("fi-open:")) })
	onExact("fi-dir", func(u *UI, m actMsg) { // typed or pick-dir'd folder
		if strings.TrimSpace(m.Val) != "" {
			u.fiOpen(strings.TrimSpace(m.Val))
		}
	})
	onExact("fi-recursive", func(u *UI, m actMsg) {
		f := &u.fi
		f.mu.Lock()
		f.recursive = m.Val == "true"
		f.mu.Unlock()
		u.fiScan()
	})
	onExact("fi-import", func(u *UI, _ actMsg) { u.fiRun(false) })
	onExact("fi-import-grid", func(u *UI, _ actMsg) { u.fiRun(true) })
	onPrefix("lib-pl-traktor:", func(u *UI, m actMsg) { u.libPlSendTraktor(atoi64(m.arg("lib-pl-traktor:"))) })
}
