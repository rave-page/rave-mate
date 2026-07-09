package webui

import (
	"fmt"
	"html"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libsync"
)

// Cross-DJ-software library sync UI (the libsync engine's front-end). rave-mate's merged music.db
// is the source of truth; a job picks WHICH app wins PER FIELD (the tier list), WHAT to sync
// (scope), and WHERE to write it (targets). Persisted in config.LibrarySync.Jobs and run via
// libsync.Run. Complements the Fyne view_studio_sync editor.

// djFields is the per-field tier order shown in the editor (canonical name → display label).
// Names match libsync.MergeFields so a pick drives MergeCanonical's fieldSource map directly.
var djFields = [][2]string{
	{"beatgrid", "Beatgrid"}, {"cues", "Hot cues"}, {"bpm", "BPM"}, {"key", "Key"},
	{"genre", "Genre"}, {"rating", "Rating"}, {"comment", "Comment"}, {"playCount", "Play count"},
	{"label", "Label"}, {"album", "Album"},
}

var djTargetApps = [][2]string{{"traktor", "Traktor"}, {"rekordbox", "Rekordbox"}, {"virtualdj", "VirtualDJ"}}

func djModeOpts() [][2]string {
	return [][2]string{
		{libsync.ModeFile, i18n.T("library.djsync.modeFile")},
		{libsync.ModeWriteback, i18n.T("library.djsync.modeWriteback")},
		{libsync.ModeTags, i18n.T("library.djsync.modeTags")},
	}
}

func init() {
	onExact("lib-djsync", func(u *UI, _ actMsg) { u.djOpen() })
	onExact("lib-dj-new", func(u *UI, _ actMsg) { u.djNew() })
	onPrefix("lib-dj-edit:", func(u *UI, m actMsg) { u.djEdit(m.arg("lib-dj-edit:")) })
	onPrefix("lib-dj-del:", func(u *UI, m actMsg) { u.djDelConfirm(m.arg("lib-dj-del:")) })
	onPrefix("lib-dj-del-do:", func(u *UI, m actMsg) { u.djDelete(m.arg("lib-dj-del-do:")) })
	onPrefix("lib-dj-run:", func(u *UI, m actMsg) { u.djRunByID(m.arg("lib-dj-run:"), false) })
	onPrefix("lib-dj-dry:", func(u *UI, m actMsg) { u.djRunByID(m.arg("lib-dj-dry:"), true) })
	// editor fields
	onExact("lib-dj-label", func(u *UI, m actMsg) { u.djSet(func(j *config.SyncJob) { j.Label = strings.TrimSpace(m.Val) }) })
	onExact("lib-dj-scope", func(u *UI, m actMsg) {
		u.djSetRender(func(j *config.SyncJob) { j.Scope.Kind = m.Val })
	})
	onExact("lib-dj-dirs", func(u *UI, m actMsg) {
		u.djSet(func(j *config.SyncJob) { j.Scope.Dirs = splitLines(m.Val) })
	})
	onPrefix("lib-dj-pl:", func(u *UI, m actMsg) { u.djTogglePlaylist(atoi64(m.arg("lib-dj-pl:")), m.Val == "true") })
	onPrefix("lib-dj-fs:", func(u *UI, m actMsg) { u.djSetField(m.arg("lib-dj-fs:"), m.Val) })
	onExact("lib-dj-tgt-add", func(u *UI, _ actMsg) {
		u.djSetRender(func(j *config.SyncJob) {
			j.Targets = append(j.Targets, config.SyncTarget{App: "traktor", Mode: libsync.ModeFile})
		})
	})
	onPrefix("lib-dj-tgt-app:", func(u *UI, m actMsg) {
		u.djSetTarget(atoi(m.arg("lib-dj-tgt-app:")), func(t *config.SyncTarget) { t.App = m.Val })
	})
	onPrefix("lib-dj-tgt-mode:", func(u *UI, m actMsg) {
		u.djSetTarget(atoi(m.arg("lib-dj-tgt-mode:")), func(t *config.SyncTarget) { t.Mode = m.Val })
	})
	onPrefix("lib-dj-tgt-path:", func(u *UI, m actMsg) {
		u.djSet(func(j *config.SyncJob) {
			u.djTargetSet(j, atoi(m.arg("lib-dj-tgt-path:")), func(t *config.SyncTarget) { t.OutputPath = strings.TrimSpace(m.Val) })
		})
	})
	onPrefix("lib-dj-tgt-del:", func(u *UI, m actMsg) { u.djDelTarget(atoi(m.arg("lib-dj-tgt-del:"))) })
	onExact("lib-dj-hotcues", func(u *UI, m actMsg) { u.djSet(func(j *config.SyncJob) { j.Rules.HotcuesToMemory = m.Val == "true" }) })
	onExact("lib-dj-writetags", func(u *UI, m actMsg) { u.djSet(func(j *config.SyncJob) { j.Rules.WriteFileTags = m.Val == "true" }) })
	onExact("lib-dj-save", func(u *UI, _ actMsg) { u.djSave(false) })
	onExact("lib-dj-saverun", func(u *UI, _ actMsg) { u.djSave(true) })
	onExact("lib-dj-list", func(u *UI, _ actMsg) { u.djOpen() }) // back to the list from the editor
}

// ── list ──

func (u *UI) djOpen() {
	if u.svc.Lib == nil {
		u.toast(i18n.T("library.dbUnavailable"))
		return
	}
	u.libSet(func(s *libSt) { s.djEditing = false })
	u.openModal(u.djListModal())
}

func (u *UI) djListModal() string {
	var jobs []config.SyncJob
	if u.svc.Cfg != nil {
		jobs = u.svc.Cfg.Features.LibrarySync.Jobs
	}
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.djsync.desc")) + `</p>`)
	b.WriteString(btnRow(btn(i18n.T("library.djsync.newJob"), "primary", "lib-dj-new", "")))
	if len(jobs) == 0 {
		b.WriteString(emptyState(i18n.T("library.djsync.noJobs")))
		return modal(i18n.T("library.djsync.title"), b.String(), "")
	}
	b.WriteString(`<div class="rp-card">`)
	for _, j := range jobs {
		sub := i18n.T("library.djsync.never")
		if j.LastRunAt != "" {
			when := j.LastRunAt
			if t, err := time.Parse(time.RFC3339, j.LastRunAt); err == nil {
				when = t.Local().Format("2006-01-02 15:04")
			}
			sub = i18n.T("library.djsync.lastRun", i18n.A{"when": when, "summary": or(j.LastSummary, "-")})
		}
		title := or(j.Label, j.ID)
		b.WriteString(itemRow(title, sub,
			btn(i18n.T("library.djsync.run"), "primary", "lib-dj-run:"+j.ID, ""),
			btn(i18n.T("library.djsync.dryRun"), "outline", "lib-dj-dry:"+j.ID, ""),
			btn(i18n.T("library.djsync.edit"), "ghost", "lib-dj-edit:"+j.ID, ""),
			btn(i18n.T("library.djsync.del"), "ghost", "lib-dj-del:"+j.ID, "")))
	}
	b.WriteString(`</div>`)
	return modal(i18n.T("library.djsync.title"), b.String(), "")
}

// ── editor ──

func (u *UI) djNew() {
	u.libSet(func(s *libSt) {
		s.djDraft = config.SyncJob{
			ID:      fmt.Sprintf("job-%d", time.Now().UnixNano()),
			Scope:   config.SyncScope{Kind: "all"},
			Targets: []config.SyncTarget{{App: "traktor", Mode: libsync.ModeFile}},
			Rules:   config.SyncRules{FieldSource: map[string]string{}},
		}
		s.djIdx, s.djEditing = -1, true
	})
	u.djRender()
}

func (u *UI) djEdit(id string) {
	if u.svc.Cfg == nil {
		return
	}
	jobs := u.svc.Cfg.Features.LibrarySync.Jobs
	for i, j := range jobs {
		if j.ID == id {
			job := j
			if job.Rules.FieldSource == nil {
				job.Rules.FieldSource = map[string]string{}
			}
			u.libSet(func(s *libSt) { s.djDraft, s.djIdx, s.djEditing = job, i, true })
			u.djRender()
			return
		}
	}
}

func (u *UI) djRender() { u.openModal(u.djEditorModal()) }

func (u *UI) djEditorModal() string {
	s := u.lib()
	s.mu.Lock()
	j := s.djDraft
	s.mu.Unlock()

	var b strings.Builder
	b.WriteString(field(i18n.T("library.djsync.label"), "lib-dj-label", j.Label, "text"))

	// scope
	b.WriteString(selectBox(i18n.T("library.djsync.scope"), "lib-dj-scope", [][2]string{
		{"all", i18n.T("library.djsync.scopeAll")},
		{"dirs", i18n.T("library.djsync.scopeDirs")},
		{"playlists", i18n.T("library.djsync.scopePlaylists")},
	}, or(j.Scope.Kind, "all")))
	switch j.Scope.Kind {
	case "dirs":
		b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(i18n.T("library.djsync.dirs")) + `</span>` +
			`<textarea class=field-input rows=3 data-act=lib-dj-dirs>` + html.EscapeString(strings.Join(j.Scope.Dirs, "\n")) + `</textarea></label>`)
	case "playlists":
		b.WriteString(u.djPlaylistPicker(j))
	}

	// per-field authoritative source (the tier list)
	b.WriteString(section(i18n.T("library.djsync.fields"), ""))
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.djsync.fieldsNote")) + `</p>`)
	appOpts := u.djAppOpts()
	b.WriteString(`<div class=dj-fields>`)
	for _, f := range djFields {
		cur := ""
		if j.Rules.FieldSource != nil {
			cur = j.Rules.FieldSource[f[0]]
		}
		b.WriteString(`<div class=dj-fieldrow>` + smartSelect("djfs-"+f[0], f[1], "lib-dj-fs:"+f[0], cur, func() []ssOpt { return appOpts }) + `</div>`)
	}
	b.WriteString(`</div>`)

	// targets
	b.WriteString(section(i18n.T("library.djsync.targets"), ""))
	for i, t := range j.Targets {
		b.WriteString(`<div class="rp-card dj-target">`)
		b.WriteString(selectBox(i18n.T("library.djsync.targetApp"), fmt.Sprintf("lib-dj-tgt-app:%d", i), djTargetApps, or(t.App, "traktor")))
		b.WriteString(selectBox(i18n.T("library.djsync.targetMode"), fmt.Sprintf("lib-dj-tgt-mode:%d", i), djModeOpts(), or(t.Mode, libsync.ModeFile)))
		b.WriteString(field(i18n.T("library.meta.path"), fmt.Sprintf("lib-dj-tgt-path:%d", i), t.OutputPath, "text"))
		b.WriteString(btnRow(btn(i18n.T("library.remove"), "ghost", fmt.Sprintf("lib-dj-tgt-del:%d", i), "")))
		b.WriteString(`</div>`)
	}
	b.WriteString(btnRow(btn(i18n.T("library.djsync.addTarget"), "outline", "lib-dj-tgt-add", "")))

	// rules
	b.WriteString(toggleRow(i18n.T("library.djsync.hotcues"), "lib-dj-hotcues", j.Rules.HotcuesToMemory))
	b.WriteString(toggleRow(i18n.T("library.djsync.writeTags"), "lib-dj-writetags", j.Rules.WriteFileTags))

	foot := btn(i18n.T("library.djsync.saveRun"), "primary", "lib-dj-saverun", "") +
		btn(i18n.T("library.djsync.save"), "outline", "lib-dj-save", "") +
		btn(i18n.T("common.cancel"), "ghost", "lib-dj-list", "")
	return modal(i18n.T("library.djsync.title"), b.String(), foot)
}

// djAppOpts builds the per-field source options: "(automatic)" + every source app present.
func (u *UI) djAppOpts() []ssOpt {
	out := []ssOpt{{Val: "", Label: i18n.T("library.djsync.auto")}}
	if u.svc.Lib == nil {
		return out
	}
	apps, err := u.svc.Lib.SourceApps()
	if err != nil {
		return out
	}
	for _, a := range apps {
		out = append(out, ssOpt{Val: a.App, Label: djAppLabel(a.App), Badge: fmt.Sprint(a.Count)})
	}
	return out
}

func djAppLabel(app string) string {
	switch app {
	case "traktor":
		return "Traktor"
	case "rekordbox":
		return "Rekordbox"
	case "virtualdj":
		return "VirtualDJ"
	case "serato":
		return "Serato"
	case "enginedj":
		return "Engine DJ"
	}
	if app == "" {
		return "?"
	}
	return strings.ToUpper(app[:1]) + app[1:]
}

func (u *UI) djPlaylistPicker(j config.SyncJob) string {
	if u.svc.Lib == nil {
		return ""
	}
	rows, _ := u.svc.Lib.ListPlaylists()
	if len(rows) == 0 {
		return emptyState(i18n.T("library.djsync.noPlaylists"))
	}
	sel := map[int64]bool{}
	for _, id := range j.Scope.Playlists {
		sel[id] = true
	}
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.djsync.playlistsPick")) + `</p><div class="rp-card">`)
	for _, p := range rows {
		b.WriteString(toggleRow(p.Name+" ("+i18n.Tn("track", p.TrackCount)+")", fmt.Sprintf("lib-dj-pl:%d", p.ID), sel[p.ID]))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── draft mutation helpers ──

func (u *UI) djSet(fn func(*config.SyncJob)) {
	u.libSetQuiet(func(s *libSt) { fn(&s.djDraft) })
}

func (u *UI) djSetRender(fn func(*config.SyncJob)) {
	u.djSet(fn)
	u.djRender()
}

func (u *UI) djSetField(field, app string) {
	u.djSet(func(j *config.SyncJob) {
		if j.Rules.FieldSource == nil {
			j.Rules.FieldSource = map[string]string{}
		}
		if app == "" {
			delete(j.Rules.FieldSource, field)
		} else {
			j.Rules.FieldSource[field] = app
		}
	})
}

func (u *UI) djTargetSet(j *config.SyncJob, i int, fn func(*config.SyncTarget)) {
	if i >= 0 && i < len(j.Targets) {
		fn(&j.Targets[i])
	}
}

func (u *UI) djSetTarget(i int, fn func(*config.SyncTarget)) {
	u.djSet(func(j *config.SyncJob) { u.djTargetSet(j, i, fn) })
	u.djRender()
}

func (u *UI) djDelTarget(i int) {
	u.djSet(func(j *config.SyncJob) {
		if i >= 0 && i < len(j.Targets) {
			j.Targets = append(j.Targets[:i], j.Targets[i+1:]...)
		}
	})
	u.djRender()
}

func (u *UI) djTogglePlaylist(id int64, on bool) {
	u.djSet(func(j *config.SyncJob) {
		out := j.Scope.Playlists[:0:0]
		for _, x := range j.Scope.Playlists {
			if x != id {
				out = append(out, x)
			}
		}
		if on {
			out = append(out, id)
		}
		j.Scope.Playlists = out
	})
}

// ── persist + run ──

func (u *UI) djSave(run bool) {
	if u.svc.Cfg == nil {
		return
	}
	s := u.lib()
	s.mu.Lock()
	job, idx := s.djDraft, s.djIdx
	s.mu.Unlock()
	if strings.TrimSpace(job.Label) == "" {
		u.toast(i18n.T("library.djsync.needName"))
		return
	}
	if len(job.Targets) == 0 {
		u.toast(i18n.T("library.djsync.noTargets"))
		return
	}
	jobs := u.svc.Cfg.Features.LibrarySync.Jobs
	if idx >= 0 && idx < len(jobs) {
		jobs[idx] = job
	} else {
		jobs = append(jobs, job)
		idx = len(jobs) - 1
	}
	u.svc.Cfg.Features.LibrarySync.Jobs = jobs
	u.saveCfg()
	u.libSet(func(st *libSt) { st.djIdx = idx })
	u.toast(i18n.T("library.djsync.saved"))
	if run {
		u.djRun(job, false)
		return
	}
	u.djOpen() // back to the list
}

func (u *UI) djRunByID(id string, dry bool) {
	if u.svc.Cfg == nil {
		return
	}
	for _, j := range u.svc.Cfg.Features.LibrarySync.Jobs {
		if j.ID == id {
			u.djRun(j, dry)
			return
		}
	}
}

// djRun executes a job through the libsync engine off the UI goroutine (guarded single-run).
func (u *UI) djRun(job config.SyncJob, dry bool) {
	if u.svc.Lib == nil {
		u.toast(i18n.T("library.dbUnavailable"))
		return
	}
	s := u.lib()
	s.mu.Lock()
	if s.djRunning {
		s.mu.Unlock()
		u.toast(i18n.T("library.djsync.running"))
		return
	}
	s.djRunning = true
	s.mu.Unlock()
	u.closeModal()
	u.toast(i18n.T("library.djsync.started"))
	u.bg(func() {
		res, err := libsync.Run(u.svc.Lib, job, dry)
		u.libSet(func(st *libSt) { st.djRunning = false })
		if err != nil {
			u.toast(i18n.T("library.djsync.failed", i18n.A{"err": err.Error()}))
			return
		}
		u.toast(i18n.T("library.djsync.done", i18n.A{"summary": res.Summary()}))
		if !dry && u.svc.Cfg != nil {
			for i := range u.svc.Cfg.Features.LibrarySync.Jobs {
				if u.svc.Cfg.Features.LibrarySync.Jobs[i].ID == job.ID {
					u.svc.Cfg.Features.LibrarySync.Jobs[i].LastRunAt = time.Now().UTC().Format(time.RFC3339)
					u.svc.Cfg.Features.LibrarySync.Jobs[i].LastSummary = res.Summary()
					u.saveCfg()
					break
				}
			}
		}
	})
}

// ── delete ──

func (u *UI) djDelConfirm(id string) {
	body := `<p class=page-sub>` + html.EscapeString(i18n.T("library.djsync.delConfirm")) + `</p>` +
		btnRow(btn(i18n.T("common.delete"), "destructive", "lib-dj-del-do:"+id, ""), btn(i18n.T("common.cancel"), "outline", "lib-dj-list", ""))
	u.openModal(modal(i18n.T("library.djsync.del"), body, ""))
}

func (u *UI) djDelete(id string) {
	if u.svc.Cfg == nil {
		return
	}
	jobs := u.svc.Cfg.Features.LibrarySync.Jobs
	out := jobs[:0]
	for _, j := range jobs {
		if j.ID != id {
			out = append(out, j)
		}
	}
	u.svc.Cfg.Features.LibrarySync.Jobs = out
	u.saveCfg()
	u.toast(i18n.T("library.djsync.deleted"))
	u.djOpen()
}

// splitLines splits a textarea value into trimmed, non-empty lines.
func splitLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}
