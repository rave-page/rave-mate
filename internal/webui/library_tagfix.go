package webui

// Tag fixer (task #74 UI): Maintenance → "Fix tags" scans the collection's files with
// tagfix.Scan (ID3v1-only upgrade, mojibake repair, missing/mismatched basics vs the
// library) and shows a grouped, selectable problem list. Apply writes atomically per
// file via tagsync (revertible snapshot + change_log); stale files are skipped.

import (
	"fmt"
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

// tfResultsState resolves the fixer view that replaces the collection list while it is open.
// Pure renderer: libTFResHTML (render_library_fixers.go) / native/zigui/src/libfixers.zig.
func (u *UI) tfResultsState() libTFResSt {
	t := &u.tf
	t.mu.Lock()
	defer t.mu.Unlock()
	st := libTFResSt{Eyebrow: i18n.T("library.tf.eyebrow"), Title: i18n.T("library.tf.title"),
		Desc: i18n.T("library.tf.desc"), CloseLbl: i18n.T("common.close")}

	if t.stage == "scan" {
		frac := 0.0
		if t.total > 0 {
			frac = float64(t.done) / float64(t.total)
		}
		st.Scanning, st.Pct = true, progressPct(frac)
		st.ScanCap = i18n.T("library.tf.scanning", i18n.A{"done": fmt.Sprint(t.done), "total": fmt.Sprint(t.total)})
		return st
	}

	nsel := 0
	for _, on := range t.sel {
		if on {
			nsel++
		}
	}
	st.ApplyLbl = i18n.T("library.tf.apply", i18n.A{"n": fmt.Sprint(nsel)})
	st.RescanLbl = i18n.T("library.tf.rescan")
	if t.applying {
		st.Hints = append(st.Hints, libHintSt{Tone: "info", Text: i18n.T("library.tf.applying")})
	}
	if t.lastErr != "" {
		st.Hints = append(st.Hints, libHintSt{Tone: "bad", Text: truncate(t.lastErr, 400)})
	}
	if t.skipped > 0 {
		st.Skipped = i18n.T("library.tf.skipped", i18n.A{"n": fmt.Sprint(t.skipped)})
	}
	if len(t.probs) == 0 {
		st.IsEmpty, st.Empty = true, i18n.T("library.tf.clean")
		return st
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
		g := libTFGrpSt{
			Title:   i18n.T("library.tf.kind." + string(k)),
			Badge:   fmt.Sprintf("%d/%d", on, len(idxs)),
			AllLbl:  i18n.T("library.tf.all"),
			AllAct:  "tf-kind:" + string(k) + ":on",
			NoneLbl: i18n.T("library.tf.none"),
			NoneAct: "tf-kind:" + string(k) + ":off",
			Desc:    i18n.T("library.tf.kindDesc." + string(k)),
		}
		for n, i := range idxs {
			if n >= maxRows {
				g.More = i18n.T("library.showingFirst",
					i18n.A{"shown": fmt.Sprint(maxRows), "total": fmt.Sprint(len(idxs))})
				break
			}
			p := t.probs[i]
			cur := p.Current
			if cur == "" {
				cur = "—"
			}
			g.Rows = append(g.Rows, libTFRowSt{Idx: fmt.Sprint(i), Checked: t.sel[i], Path: p.Path,
				Base: filepath.Base(p.Path), Field: p.Field,
				Cur: truncate(cur, 60), Proposed: truncate(p.Proposed, 60)})
		}
		st.Groups = append(st.Groups, g)
	}
	return st
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

// tfEditorState: editable file-tag fields for the selected track. Saving writes the
// file via tagsync (revertible + journaled); the library row itself is untouched.
// Caller holds s.mu. Pure renderer: libTagEdHTML (render_library_fixers.go).
func (u *UI) tfEditorState(s *libSt) libTagEdSt {
	if !s.tagEdit {
		return libTagEdSt{OpenLbl: i18n.T("library.tf.editTags")}
	}
	d := s.tagDraft
	st := libTagEdSt{Open: true, Desc: i18n.T("library.tf.editDesc"),
		SaveLbl: i18n.T("common.save"), CancelLbl: i18n.T("common.cancel")}
	for _, f := range tfEditFields {
		st.Fields = append(st.Fields, newPBField(i18n.T("library.tf.f."+f), "tf-edit:"+f, d[f], "text", ""))
	}
	return st
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
