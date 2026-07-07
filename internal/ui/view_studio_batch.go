package ui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/tagsync"
	"rave.page/mate/internal/transcode"
)

// Batch operations: tick files in the browser, then run waveform / tags / fingerprint
// analysis (cached to the store) or transcode across the whole selection at once. Each
// batch is one cancellable Queue job; per-file work runs on the worker subprocesses.

func (sv *studioView) buildBatchBar() *fyne.Container {
	sv.batchLbl = mutedInline("")
	mk := func(label string, icon fyne.Resource, fn func()) *widget.Button {
		b := widget.NewButtonWithIcon(label, icon, fn)
		b.Importance = widget.LowImportance
		return b
	}
	// Wrap the 6 action buttons so the bar never overflows the window - each
	// stays clickable on narrow widths by wrapping to the next row.
	actions := WrapActions(
		mk("Waveforms", theme.MediaMusicIcon(), func() { sv.batchAnalyze("Waveforms", store.KindPeaks) }),
		mk("Tags", theme.InfoIcon(), func() { sv.batchAnalyze("Tags", store.KindTags) }),
		mk("Fingerprint", theme.SearchIcon(), func() { sv.batchAnalyze("Fingerprint", store.KindFingerprint) }),
		mk("Write tags", theme.DocumentSaveIcon(), sv.batchWriteTags),
		mk("Transcode…", theme.MediaPlayIcon(), sv.batchTranscode),
		mk("Clear", theme.CancelIcon(), sv.clearBatch),
	)
	bar := container.NewBorder(nil, nil, sv.batchLbl, nil, actions)
	bar.Hide()
	return bar
}

func (sv *studioView) toggleBatch(e fileEntry, on bool) {
	if on {
		sv.batchSel[e.path] = e
	} else {
		delete(sv.batchSel, e.path)
	}
	sv.refreshBatchBar()
}

func (sv *studioView) refreshBatchBar() {
	if sv.batchBar == nil {
		return
	}
	n := len(sv.batchSel)
	if n == 0 {
		sv.batchBar.Hide()
	} else {
		sv.batchLbl.SetText(fmt.Sprintf("%d selected", n))
		sv.batchBar.Show()
	}
	sv.batchBar.Refresh()
}

func (sv *studioView) clearBatch() {
	sv.batchSel = map[string]fileEntry{}
	sv.refreshBatchBar()
	if sv.browseList != nil {
		sv.browseList.Refresh() // uncheck visible rows
	}
}

// batchFiles snapshots the current selection as a stable, name-sorted slice.
func (sv *studioView) batchFiles() []fileEntry {
	out := make([]fileEntry, 0, len(sv.batchSel))
	for _, e := range sv.batchSel {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// batchAnalyze runs one analysis kind across the selection as a single Queue job.
func (sv *studioView) batchAnalyze(label, kind string) {
	if sv.u.svc.Workers == nil {
		sv.u.Notify("rave-mate", "Worker unavailable.")
		return
	}
	files := sv.batchFiles()
	if len(files) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	sv.jobsMu.Lock()
	sv.nextJob++
	j := &tcJob{name: fmt.Sprintf("%s · %d files", label, len(files)), presetLabel: "batch", status: "running", cancel: cancel}
	sv.jobs = append([]*tcJob{j}, sv.jobs...)
	sv.jobsMu.Unlock()
	sv.refreshQueue()
	sv.showSection("Queue")

	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "batch-analyze", false)
		defer cancel()
		done, failed := 0, 0
		for _, e := range files {
			if ctx.Err() != nil {
				break
			}
			if err := sv.analyzeOne(ctx, kind, e); err != nil {
				failed++
			}
			done++
			pct := float64(done) / float64(len(files)) * 100
			sv.updateJob(j, func() { j.percent = pct })
		}
		sv.updateJob(j, func() {
			switch {
			case ctx.Err() != nil:
				j.status = "canceled"
			case failed > 0:
				j.status = "done"
				j.msg = fmt.Sprintf("%d failed", failed)
				j.percent = 100
			default:
				j.status = "done"
				j.percent = 100
			}
		})
		sv.u.Notify("rave-mate", fmt.Sprintf("%s: %d of %d done", label, done-failed, len(files)))
	}()
}

// analyzeOne runs (and persists) one analysis kind for one file; skips if already cached.
func (sv *studioView) analyzeOne(ctx context.Context, kind string, e fileEntry) error {
	mtime := fileMtime(e.path)
	if _, ok := sv.u.svc.Store.GetAnalysis(kind, e.path, mtime); ok {
		return nil
	}
	switch kind {
	case store.KindPeaks:
		raw, err := sv.u.svc.Workers.Run(ctx, "probe", "probe.peaks",
			map[string]any{"path": e.path, "buckets": 8192})
		if err != nil {
			return err
		}
		var r struct {
			Peaks  string  `json:"peaks"`
			DurSec float64 `json:"durationSeconds"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Peaks == "" || r.DurSec <= 0 {
			return fmt.Errorf("no waveform")
		}
		peaks, derr := base64.StdEncoding.DecodeString(r.Peaks)
		if derr != nil {
			return derr
		}
		tp := trackPeaks{DurSec: r.DurSec, Peaks: peaks}
		data, merr := json.Marshal(tp)
		if merr != nil {
			return merr
		}
		sv.u.svc.Store.PutAnalysis(kind, e.path, mtime, data)
		sv.cachePeaks(e.path, tp)
	case store.KindTags:
		raw, err := sv.u.svc.Workers.Run(ctx, "probe", "probe.tags", map[string]any{"path": e.path})
		if err != nil {
			return err
		}
		sv.u.svc.Store.PutAnalysis(kind, e.path, mtime, raw)
	case store.KindFingerprint:
		raw, err := sv.u.svc.Workers.Run(ctx, "fingerprint", "fingerprint.compute", map[string]any{"path": e.path})
		if err != nil {
			return err
		}
		sv.u.svc.Store.PutAnalysis(kind, e.path, mtime, raw)
	}
	return nil
}

// batchWriteTags resolves each selected file to its collection track and writes the DJ
// analysis into the file (MP3/FLAC). Confirmed first - it modifies real library files
// (each write is revertible). Files not in the collection are skipped (no analysis to write).
func (sv *studioView) batchWriteTags() {
	db := sv.u.svc.Lib
	if db == nil {
		sv.u.Notify("rave-mate", "Library DB unavailable.")
		return
	}
	files := sv.batchFiles()
	if len(files) == 0 {
		return
	}
	win := currentWindow()
	if win == nil {
		return
	}
	dialog.NewConfirm("Write tags to files",
		fmt.Sprintf("Write DJ analysis (BPM / key / genre / comment) into %d file(s)?\nMP3 + FLAC only; each write is revertible.", len(files)),
		func(ok bool) {
			if ok {
				sv.runBatchWriteTags(files)
			}
		}, win).Show()
}

func (sv *studioView) runBatchWriteTags(files []fileEntry) {
	ctx, cancel := context.WithCancel(context.Background())
	sv.jobsMu.Lock()
	sv.nextJob++
	j := &tcJob{name: fmt.Sprintf("Write tags · %d files", len(files)), presetLabel: "tags", status: "running", cancel: cancel}
	sv.jobs = append([]*tcJob{j}, sv.jobs...)
	sv.jobsMu.Unlock()
	sv.refreshQueue()
	sv.showSection("Queue")

	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "batch-tagwrite", false)
		defer cancel()
		db := sv.u.svc.Lib
		wrote, skipped, failed, done := 0, 0, 0, 0
		for _, e := range files {
			if ctx.Err() != nil {
				break
			}
			tr, ok, err := db.TrackByPath(e.path)
			switch {
			case err != nil || !ok:
				skipped++ // not in the collection → no analysis to write
			default:
				if _, aerr := tagsync.Apply(db, tr); aerr != nil {
					if aerr == tagsync.ErrUnsupported {
						skipped++
					} else {
						failed++
					}
				} else {
					wrote++
				}
			}
			done++
			pct := float64(done) / float64(len(files)) * 100
			sv.updateJob(j, func() { j.percent = pct })
		}
		sv.updateJob(j, func() {
			j.status = "done"
			j.percent = 100
			j.msg = fmt.Sprintf("%d written · %d skipped · %d failed", wrote, skipped, failed)
		})
		sv.u.svc.Log.Info("library", "batch tag write", map[string]any{"wrote": wrote, "skipped": skipped, "failed": failed})
		sv.u.Notify("rave-mate", fmt.Sprintf("Tags written to %d file(s) (%d skipped).", wrote, skipped))
	}()
}

// batchTranscode picks a preset, then enqueues every selected file as a Queue job.
func (sv *studioView) batchTranscode() {
	files := sv.batchFiles()
	if len(files) == 0 {
		return
	}
	win := currentWindow()
	if win == nil {
		return
	}
	var custom []transcode.Preset
	if sv.u.svc.Cfg != nil {
		custom = sv.u.svc.Cfg.Features.Transcode.Presets
	}
	presets := transcode.AllPresets(custom)

	var d dialog.Dialog
	rows := make([]fyne.CanvasObject, 0, len(presets))
	for _, p := range presets {
		b := widget.NewButton(p.Label, func() {
			d.Hide()
			for _, e := range files {
				sv.startTranscode(e, p, "", "")
			}
			sv.u.Notify("rave-mate", fmt.Sprintf("Queued %d transcodes (%s)", len(files), p.Label))
		})
		rows = append(rows, b)
	}
	body := container.NewVScroll(container.NewVBox(rows...))
	body.SetMinSize(fyne.NewSize(360, 340))
	d = dialog.NewCustom(fmt.Sprintf("Batch transcode · %d files", len(files)), "Cancel", body, win)
	d.Show()
}
