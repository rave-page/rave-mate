package webui

// BPM target ranges: per-playlist + per-genre bands (libdb rules) with an
// enforcement pass that folds octave-wrong BPMs (87 → 174) into the band -
// library, collection files (through grid locks) and file tags. Pure ×2/÷2
// folds never move a beat position, so grids and time-based cues survive.

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagsync"
)

type bprItem struct {
	old    musiclib.Track
	folded musiclib.Track
	factor float64
	r      musiclib.BPMRange
}

type bprState struct {
	mu      sync.Mutex
	items   []bprItem // out-of-range, foldable
	unfix   []string  // out-of-range, band unreachable by ×2 (title lines)
	ruled   int       // tracks covered by any rule
	scanned bool
	busy    bool
}

func init() {
	onExact("lib-bpmranges", func(u *UI, _ actMsg) { u.bprOpen() })
	onExact("lib-bpmrange-genre-do", func(u *UI, m actMsg) { u.bprGenreSave(parseForm(m.Form)) })
	onPrefix("lib-bpmrange-gdel:", func(u *UI, m actMsg) { u.bprGenreDel(m.arg("lib-bpmrange-gdel:")) })
	onExact("lib-bpmrange-scan", func(u *UI, _ actMsg) { u.bprScan() })
	onExact("lib-bpmrange-apply", func(u *UI, _ actMsg) { u.bprApply() })
	onPrefix("lib-pl-bpmrange:", func(u *UI, m actMsg) { u.bprPlaylistModal(int64(atoi(m.arg("lib-pl-bpmrange:")))) })
	onExact("lib-pl-bpmrange-do", func(u *UI, m actMsg) { u.bprPlaylistSave(parseForm(m.Form)) })
}

// bprOpen renders the genre-rules + enforcement modal.
func (u *UI) bprOpen() {
	u.bpr.mu.Lock()
	u.bpr.items, u.bpr.unfix, u.bpr.ruled, u.bpr.scanned = nil, nil, 0, false
	u.bpr.mu.Unlock()
	u.openModal(modal(i18n.T("library.bpr.title"), u.bprBodyHTML(), ""))
}

func (u *UI) bprRefresh() {
	u.openModal(modal(i18n.T("library.bpr.title"), u.bprBodyHTML(), ""))
}

func (u *UI) bprBodyHTML() string {
	if u.svc.Lib == nil {
		return `<div class=set-note>` + html.EscapeString(i18n.T("library.bpr.noLib")) + `</div>`
	}
	var b strings.Builder
	b.WriteString(`<div class=set-note>` + html.EscapeString(i18n.T("library.bpr.hint")) + `</div>`)

	// genre rules
	rules, err := u.svc.Lib.GenreBPMRules()
	if err == nil {
		for _, g := range rules {
			b.WriteString(`<div class=bpr-rule><b>` + html.EscapeString(g.Genre) + `</b> ` +
				html.EscapeString(fmt.Sprintf("%g–%g BPM", g.Range.Min, g.Range.Max)) +
				btn("✕", "ghost", "lib-bpmrange-gdel:"+g.Genre, "") + `</div>`)
		}
	}
	b.WriteString(`<form data-act=lib-bpmrange-genre-do class=mform>` +
		fpair(labeledInput("genre", i18n.T("library.bpr.genre"), ""),
			fpair(labeledInput("min", i18n.T("library.bpr.min"), ""), labeledInput("max", i18n.T("library.bpr.max"), ""))) +
		`<button class="rp-btn rp-btn--outline" type=submit>` + html.EscapeString(i18n.T("library.bpr.addRule")) + `</button></form>`)

	// playlist rules overview (set via each playlist's ⋯ menu)
	if pls, err := u.svc.Lib.ListPlaylists(); err == nil {
		var ruled []string
		for _, p := range pls {
			if r, ok := u.svc.Lib.PlaylistBPMRange(p.ID); ok {
				ruled = append(ruled, fmt.Sprintf("%s %g–%g", p.Name, r.Min, r.Max))
			}
		}
		if len(ruled) > 0 {
			sort.Strings(ruled)
			b.WriteString(`<div class=set-note>` + html.EscapeString(i18n.T("library.bpr.plRules")+" "+strings.Join(ruled, " · ")) + `</div>`)
		}
	}

	// enforcement
	u.bpr.mu.Lock()
	items, unfix, ruled, scanned, busy := u.bpr.items, u.bpr.unfix, u.bpr.ruled, u.bpr.scanned, u.bpr.busy
	u.bpr.mu.Unlock()
	b.WriteString(`<hr>`)
	switch {
	case busy:
		b.WriteString(`<div class=set-note>` + html.EscapeString(i18n.T("library.bpr.working")) + `</div>`)
	case !scanned:
		b.WriteString(btnRow(btn(i18n.T("library.bpr.scan"), "primary", "lib-bpmrange-scan", "")))
	default:
		b.WriteString(`<div>` + html.EscapeString(i18n.T("library.bpr.scanResult", i18n.A{
			"ruled": fmt.Sprint(ruled), "fix": fmt.Sprint(len(items)), "unfix": fmt.Sprint(len(unfix))})) + `</div>`)
		for i, it := range items {
			if i >= 8 {
				b.WriteString(`<div class=set-note>…</div>`)
				break
			}
			b.WriteString(`<div class=set-note>` + html.EscapeString(fmt.Sprintf("%s: %g → %g", trackTitle(it.old), it.old.BPM, it.folded.BPM)) + `</div>`)
		}
		for _, l := range unfix {
			b.WriteString(`<div class="set-note bpr-warn">` + html.EscapeString(l) + `</div>`)
		}
		if len(items) > 0 {
			b.WriteString(btnRow(btn(i18n.T("library.bpr.apply", i18n.A{"n": fmt.Sprint(len(items))}), "primary", "lib-bpmrange-apply", "")))
		}
	}
	return b.String()
}

func (u *UI) bprGenreSave(f map[string]string) {
	if u.svc.Lib == nil {
		return
	}
	mn, _ := strconv.ParseFloat(strings.TrimSpace(f["min"]), 64)
	mx, _ := strconv.ParseFloat(strings.TrimSpace(f["max"]), 64)
	if err := u.svc.Lib.SetGenreBPMRange(f["genre"], musiclib.BPMRange{Min: mn, Max: mx}); err != nil {
		u.toast(err.Error())
		return
	}
	u.bprRefresh()
}

func (u *UI) bprGenreDel(genre string) {
	if u.svc.Lib == nil {
		return
	}
	_ = u.svc.Lib.SetGenreBPMRange(genre, musiclib.BPMRange{})
	u.bprRefresh()
}

// bprScan finds out-of-range tracks under the current rules (dry).
func (u *UI) bprScan() {
	if u.svc.Lib == nil {
		return
	}
	u.bpr.mu.Lock()
	if u.bpr.busy {
		u.bpr.mu.Unlock()
		return
	}
	u.bpr.busy = true
	u.bpr.mu.Unlock()
	u.bprRefresh()
	u.bg(func() {
		items, unfix, ruled := u.bprCollect()
		u.bpr.mu.Lock()
		u.bpr.items, u.bpr.unfix, u.bpr.ruled = items, unfix, ruled
		u.bpr.scanned, u.bpr.busy = true, false
		u.bpr.mu.Unlock()
		u.bprRefresh()
	})
}

func (u *UI) bprCollect() (items []bprItem, unfix []string, ruled int) {
	rules, err := u.svc.Lib.LoadBPMRules()
	if err != nil || rules.Empty() {
		return nil, nil, 0
	}
	tracks, err := u.svc.Lib.LoadAllTracks()
	if err != nil {
		return nil, nil, 0
	}
	for _, t := range tracks {
		r, ok := rules.Resolve(t.Path, t.Genre)
		if !ok {
			continue
		}
		ruled++
		if t.BPM <= 0 || r.Contains(t.BPM) {
			continue
		}
		ft := t
		ft.Beatgrid = append([]musiclib.GridMarker(nil), t.Beatgrid...)
		factor, changed := musiclib.FoldTrack(&ft, r)
		if !changed {
			unfix = append(unfix, fmt.Sprintf("%s: %g BPM (%g–%g)", trackTitle(t), t.BPM, r.Min, r.Max))
			continue
		}
		items = append(items, bprItem{old: t, folded: ft, factor: factor, r: r})
	}
	return items, unfix, ruled
}

// bprApply folds the scanned tracks: library rows + tags always; collection files
// for single-marker grids (the writers emit one marker - multi-marker manual grids
// are library/tag-only to avoid flattening them).
func (u *UI) bprApply() {
	u.bpr.mu.Lock()
	if u.bpr.busy || len(u.bpr.items) == 0 {
		u.bpr.mu.Unlock()
		return
	}
	items := u.bpr.items
	u.bpr.busy = true
	u.bpr.mu.Unlock()
	u.bprRefresh()
	u.bg(func() {
		var folded, tagged int
		var errs []string
		lock := u.svc.Cfg.Features.GridFix.LockFixed
		var fixes []musiclib.GridFixUpdate
		var fixedPaths []string
		for _, it := range items {
			if err := u.svc.Lib.UpdateTrackBPMFold(it.old, it.folded); err != nil {
				errs = append(errs, err.Error())
				continue
			}
			folded++
			if len(it.folded.Beatgrid) == 1 {
				fixes = append(fixes, musiclib.GridFixUpdate{Path: it.old.Path,
					BPM: it.folded.BPM, StartMs: it.folded.Beatgrid[0].PositionMs, Lock: lock})
				fixedPaths = append(fixedPaths, it.old.Path)
			}
			if _, err := tagsync.Apply(u.svc.Lib, it.folded); err == nil {
				tagged++
			}
		}
		var targetNotes []string
		if len(fixes) > 0 {
			for _, t := range u.gfTargets() {
				res, err := u.gfApplyTo(t, fixes, fixedPaths)
				if err != nil {
					errs = append(errs, t.key+": "+err.Error())
					continue
				}
				targetNotes = append(targetNotes, fmt.Sprintf("%s %d", t.key, res.Updated))
			}
		}
		u.bpr.mu.Lock()
		u.bpr.items, u.bpr.unfix, u.bpr.scanned, u.bpr.busy = nil, nil, false, false
		u.bpr.mu.Unlock()
		msg := i18n.T("library.bpr.done", i18n.A{"n": fmt.Sprint(folded), "tags": fmt.Sprint(tagged)})
		if len(targetNotes) > 0 {
			msg += " · " + strings.Join(targetNotes, " · ")
		}
		if len(errs) > 0 {
			msg += " · " + errs[0]
		}
		u.toast(msg)
		u.bprRefresh()
		u.patchMain()
	})
}

// bprPlaylistModal edits one playlist's target range.
func (u *UI) bprPlaylistModal(id int64) {
	if u.svc.Lib == nil {
		return
	}
	cur, _ := u.svc.Lib.PlaylistBPMRange(id)
	mn, mx := "", ""
	if cur.Valid() {
		mn, mx = fmtG(cur.Min), fmtG(cur.Max)
	}
	body := `<div class=set-note>` + html.EscapeString(i18n.T("library.bpr.plHint")) + `</div>` +
		`<form data-act=lib-pl-bpmrange-do class=mform>` + hiddenField("id", strconv.FormatInt(id, 10)) +
		fpair(labeledInput("min", i18n.T("library.bpr.min"), mn), labeledInput("max", i18n.T("library.bpr.max"), mx)) +
		`<button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(i18n.T("common.save")) + `</button></form>`
	u.openModal(modal(i18n.T("library.bpr.plTitle"), body, ""))
}

func (u *UI) bprPlaylistSave(f map[string]string) {
	u.closeModal()
	if u.svc.Lib == nil {
		return
	}
	id, _ := strconv.ParseInt(f["id"], 10, 64)
	mn, _ := strconv.ParseFloat(strings.TrimSpace(f["min"]), 64)
	mx, _ := strconv.ParseFloat(strings.TrimSpace(f["max"]), 64)
	if err := u.svc.Lib.SetPlaylistBPMRange(id, musiclib.BPMRange{Min: mn, Max: mx}); err != nil {
		u.toast(err.Error())
		return
	}
	u.toast(i18n.T("library.bpr.plSaved"))
	u.patchMain()
}

func fmtG(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

var _ = libdb.PlaylistManual // keep libdb import stable for future rule kinds
