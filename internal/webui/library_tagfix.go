package webui

// Tag fixer (task #74 UI): Maintenance → "Fix tags" scans the collection's files with
// tagfix.Scan (ID3v1-only upgrade, mojibake repair, missing/mismatched basics vs the
// library) and shows a grouped, selectable problem list. Apply writes atomically per
// file via tagsync (revertible snapshot + change_log); stale files are skipped.

import (
	"fmt"
	"html"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagfix"
	"rave.page/mate/internal/tagsync"
	"rave.page/mate/internal/tagwrite"
)

type tfState struct {
	mu          sync.Mutex
	stage       string // "" | "scan" | "done"
	resView     bool   // results replace the collection list
	done, total int
	skipped     int
	probs       []tagfix.Problem
	sel         map[int]bool
	applying    bool
	lastErr     string
}

// tfKinds in render order, with their i18n suffixes.
var tfKinds = []tagfix.Kind{tagfix.KindV1Only, tagfix.KindMojibake, tagfix.KindMissing,
	tagfix.KindMismatch, tagfix.KindNoBasics}

// tfStart scans the on-disk collection in the background.
func (u *UI) tfStart() {
	t := &u.tf
	t.mu.Lock()
	if t.stage == "scan" || t.applying {
		t.resView = true // re-entry while busy: just re-open the view
		t.mu.Unlock()
		u.mu.Lock()
		u.libSection = "collection"
		u.mu.Unlock()
		u.patchMain()
		return
	}
	t.stage, t.resView = "scan", true
	t.done, t.total, t.skipped = 0, 0, 0
	t.probs, t.sel, t.lastErr = nil, map[int]bool{}, ""
	t.mu.Unlock()
	u.mu.Lock()
	u.libSection = "collection"
	u.mu.Unlock()
	u.patchMain() // open the scanning view immediately; hydrate + scan off-thread (never block the actWorker)
	// libTracksBlocking parked the actWorker up to 30s on a cold library (froze every tab); async instead.
	u.libTracksAsync(u.lib(), "tagfix", func(tracks []musiclib.Track) {
		u.bg(func() {
			var onDisk []musiclib.Track
			for _, tr := range tracks { // fs stat loop stays off the actWorker
				if tr.Path != "" && pathOnDisk(tr.Path) {
					onDisk = append(onDisk, tr)
				}
			}
			skipped := 0
			lastPatch := 0
			probs, err := tagfix.Scan(onDisk, tagfix.Options{Skipped: &skipped,
				Progress: func(done, total int) {
					t.mu.Lock()
					t.done, t.total = done, total
					t.mu.Unlock()
					if done-lastPatch >= 100 || done == total {
						lastPatch = done
						u.libPatchBody()
					}
				}})
			t.mu.Lock()
			t.stage, t.skipped = "done", skipped
			t.probs = probs
			t.sel = map[int]bool{}
			for i := range probs {
				t.sel[i] = true // everything proposed is safe + revertible - default all on
			}
			if err != nil {
				t.lastErr = err.Error()
			}
			t.mu.Unlock()
			u.libPatchBody()
		})
	})
}

// tfApply writes the selected repairs (atomic per file), then rescans for fresh state.
func (u *UI) tfApply() {
	t := &u.tf
	t.mu.Lock()
	if t.applying || t.stage != "done" {
		t.mu.Unlock()
		return
	}
	var picked []tagfix.Problem
	for i, p := range t.probs {
		if t.sel[i] {
			picked = append(picked, p)
		}
	}
	if len(picked) == 0 {
		t.mu.Unlock()
		u.toast(i18n.T("library.tf.nonePicked"))
		return
	}
	t.applying = true
	t.mu.Unlock()
	u.bg(func() {
		n, err := tagfix.Apply(u.svc.Lib, picked)
		t.mu.Lock()
		t.applying = false
		if err != nil {
			t.lastErr = err.Error()
		}
		t.mu.Unlock()
		u.toast(i18n.T("library.tf.appliedToast", i18n.A{"n": fmt.Sprint(n)}))
		u.tfStart() // rescan: applied problems disappear, stale ones resurface honestly
	})
	u.libPatchBody()
}

// tfSetKind bulk-toggles every problem of one kind.
func (u *UI) tfSetKind(kind string, on bool) {
	t := &u.tf
	t.mu.Lock()
	for i, p := range t.probs {
		if string(p.Kind) == kind {
			t.sel[i] = on
		}
	}
	t.mu.Unlock()
	u.libPatchBody()
}

// tfResultsHTML replaces the collection list while the fixer view is open.
func (u *UI) tfResultsHTML() string {
	t := &u.tf
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + html.EscapeString(i18n.T("library.tf.eyebrow")) + `</div><div class=insp-title>` +
		html.EscapeString(i18n.T("library.tf.title")) + `</div></div>`)
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.tf.desc")) + `</p>`)

	if t.stage == "scan" {
		frac := 0.0
		if t.total > 0 {
			frac = float64(t.done) / float64(t.total)
		}
		b.WriteString(progressBar(frac, i18n.T("library.tf.scanning", i18n.A{"done": fmt.Sprint(t.done), "total": fmt.Sprint(t.total)})))
		b.WriteString(btnRow(btn(i18n.T("common.close"), "ghost", "tf-close", "")))
		return b.String()
	}

	nsel := 0
	for _, on := range t.sel {
		if on {
			nsel++
		}
	}
	b.WriteString(`<div class=lib-toolbar>` +
		btn(i18n.T("library.tf.apply", i18n.A{"n": fmt.Sprint(nsel)}), "primary", "tf-apply", "") +
		btn(i18n.T("library.tf.rescan"), "outline", "lib-tagfix", "") +
		btn(i18n.T("common.close"), "ghost", "tf-close", "") + `</div>`)
	if t.applying {
		b.WriteString(hint("info", i18n.T("library.tf.applying")))
	}
	if t.lastErr != "" {
		b.WriteString(hint("bad", truncate(t.lastErr, 400)))
	}
	if t.skipped > 0 {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.tf.skipped", i18n.A{"n": fmt.Sprint(t.skipped)})) + `</p>`)
	}
	if len(t.probs) == 0 {
		b.WriteString(emptyState(i18n.T("library.tf.clean")))
		return b.String()
	}

	// grouped by kind, stable order
	byKind := map[tagfix.Kind][]int{}
	for i, p := range t.probs {
		byKind[p.Kind] = append(byKind[p.Kind], i)
	}
	const maxRows = 200
	for _, k := range tfKinds {
		idxs := byKind[k]
		if len(idxs) == 0 {
			continue
		}
		on := 0
		for _, i := range idxs {
			if t.sel[i] {
				on++
			}
		}
		b.WriteString(`<div class=tf-grp><div class=tf-grphead><span class=tf-grptitle>` +
			html.EscapeString(i18n.T("library.tf.kind."+string(k))) + `</span>` +
			badge(fmt.Sprintf("%d/%d", on, len(idxs)), "secondary") +
			btn(i18n.T("library.tf.all"), "ghost", "tf-kind:"+string(k)+":on", "") +
			btn(i18n.T("library.tf.none"), "ghost", "tf-kind:"+string(k)+":off", "") + `</div>`)
		b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.tf.kindDesc."+string(k))) + `</p>`)
		for n, i := range idxs {
			if n >= maxRows {
				b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.showingFirst",
					i18n.A{"shown": fmt.Sprint(maxRows), "total": fmt.Sprint(len(idxs))})) + `</p>`)
				break
			}
			p := t.probs[i]
			chk := ""
			if t.sel[i] {
				chk = " checked"
			}
			cur := p.Current
			if cur == "" {
				cur = "—"
			}
			b.WriteString(`<label class=tf-row><input type=checkbox data-act="tf-sel:` + fmt.Sprint(i) + `"` + chk + `>` +
				`<span class=tf-file title="` + html.EscapeString(p.Path) + `">` + html.EscapeString(filepath.Base(p.Path)) + `</span>` +
				`<span class=tf-field>` + html.EscapeString(p.Field) + `</span>` +
				`<span class=tf-diff><s>` + html.EscapeString(truncate(cur, 60)) + `</s> → <b>` +
				html.EscapeString(truncate(p.Proposed, 60)) + `</b></span></label>`)
		}
		b.WriteString(`</div>`)
	}
	return b.String()
}

// truncate shortens s to max runes with an ellipsis.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// ── per-track tag editor (detail rail) ──

// tfEditorHTML: editable file-tag fields for the selected track. Saving writes the
// file via tagsync (revertible + journaled); the library row itself is untouched.
func (u *UI) tfEditorHTML(s *libSt, sel *libSel) string {
	if !s.tagEdit {
		return btnRow(btn(i18n.T("library.tf.editTags"), "outline", "tf-edit-open", ""))
	}
	d := s.tagDraft
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.tf.editDesc")) + `</p>`)
	b.WriteString(`<div class=pbuilder>`)
	for _, f := range tfEditFields {
		b.WriteString(pbField(i18n.T("library.tf.f."+f), "tf-edit:"+f, d[f], "text", ""))
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div class=btn-row>` +
		btn(i18n.T("common.save"), "primary", "tf-edit-save", "") +
		btn(i18n.T("common.cancel"), "ghost", "tf-edit-close", "") + `</div>`)
	return b.String()
}

var tfEditFields = []string{"title", "artist", "album", "genre", "label", "year", "rating"}

// tfEditOpen seeds the draft from the file's current tags (rating shown as 0-5 stars).
func (u *UI) tfEditOpen() {
	s := u.lib()
	s.mu.Lock()
	sel := s.sel
	s.mu.Unlock()
	if sel == nil {
		return
	}
	path := sel.path
	u.bg(func() { // tag-file parse off the actWorker (mirrors tfEditSave)
		cur, err := tagwrite.Read(path)
		if err != nil {
			u.toast(i18n.T("library.tf.readFailed") + err.Error())
			return
		}
		s.mu.Lock()
		if s.sel == nil || s.sel.path != path { // selection moved while we read: don't seed the new track with the old one's tags
			s.mu.Unlock()
			return
		}
		s.tagEdit = true
		s.tagDraft = map[string]string{}
		for _, f := range tfEditFields {
			s.tagDraft[f] = cur[f]
		}
		if r, err := strconv.Atoi(cur[tagwrite.FieldRating]); err == nil && r > 5 {
			s.tagDraft["rating"] = fmt.Sprint(int(math.Round(float64(r) / 51))) // 0-255 → stars
		}
		s.mu.Unlock()
		if u.stopped() {
			return
		}
		u.libPatchDetail()
	})
}

// tfEditSave writes the draft through tagsync (one atomic file write, revertible).
func (u *UI) tfEditSave() {
	s := u.lib()
	s.mu.Lock()
	sel := s.sel
	draft := make(map[string]string, len(s.tagDraft))
	for k, v := range s.tagDraft {
		draft[k] = v
	}
	s.mu.Unlock()
	if sel == nil {
		return
	}
	tr := sel.track
	if tr.Path == "" {
		tr = musiclib.Track{Path: sel.path}
	}
	desired := tagwrite.Tags{}
	for f, v := range draft {
		desired[f] = strings.TrimSpace(v)
	}
	if st, err := strconv.Atoi(desired[tagwrite.FieldRating]); err == nil && st >= 0 && st <= 5 {
		desired[tagwrite.FieldRating] = fmt.Sprint(st * 51) // stars → canonical 0-255
	}
	u.bg(func() {
		if _, err := tagsync.ApplyTags(u.svc.Lib, tr, desired); err != nil {
			u.toast(i18n.T("library.tf.writeFailed") + err.Error())
			return
		}
		s.mu.Lock()
		s.tagEdit = false
		s.mu.Unlock()
		u.toast(i18n.T("library.tf.savedToast"))
		u.libPatchDetail()
	})
}

func init() {
	onExact("lib-tagfix", func(u *UI, _ actMsg) { u.tfStart() })
	onExact("tf-apply", func(u *UI, _ actMsg) { u.tfApply() })
	onExact("tf-close", func(u *UI, _ actMsg) {
		u.tf.mu.Lock()
		u.tf.resView = false
		u.tf.mu.Unlock()
		u.libPatchBody()
	})
	onPrefix("tf-sel:", func(u *UI, m actMsg) {
		i := atoi(m.arg("tf-sel:"))
		u.tf.mu.Lock()
		if u.tf.sel != nil {
			u.tf.sel[i] = !u.tf.sel[i]
		}
		u.tf.mu.Unlock()
		u.libPatchBody()
	})
	onPrefix("tf-kind:", func(u *UI, m actMsg) {
		rest := m.arg("tf-kind:") // "<kind>:<on|off>"
		if i := strings.LastIndex(rest, ":"); i > 0 {
			u.tfSetKind(rest[:i], rest[i+1:] == "on")
		}
	})
	onExact("tf-edit-open", func(u *UI, _ actMsg) { u.tfEditOpen() })
	onExact("tf-edit-close", func(u *UI, _ actMsg) {
		s := u.lib()
		s.mu.Lock()
		s.tagEdit = false
		s.mu.Unlock()
		u.libPatchDetail()
	})
	onExact("tf-edit-save", func(u *UI, _ actMsg) { u.tfEditSave() })
	onPrefix("tf-edit:", func(u *UI, m actMsg) {
		f := m.arg("tf-edit:")
		s := u.lib()
		s.mu.Lock()
		if s.tagDraft != nil {
			s.tagDraft[f] = m.Val
		}
		s.mu.Unlock()
	})
}
