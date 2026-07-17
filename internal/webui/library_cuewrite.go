package webui

// Cue write-back router: pushes the cue editor's result into the user's DJ software,
// reusing the gridfix apply pattern - auto-detected targets (gfTargets), one action-bound
// "Write cues to {app}" button per detected library, backup-first, per-target applied
// state, busy serialization, refuse-while-running guards. Scope = the checked collection
// rows (mass cue-prep set), else the open track. Only tracks with ≥1 musical cue are
// written; drops are NOT exported (they only become cues via an applied pattern).

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/cuewriteback"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/musiclib"
)

// ceTargets returns the detected DJ-software write targets, cached ~30s in ceSt: rail
// repaints run per cursor move on the serial act worker, and gfTargets stats 26 drive
// roots (Serato) + probes installs - never per repaint. force = refresh now (write click).
func (u *UI) ceTargets(force bool) []gfTarget {
	c := u.ce()
	c.mu.Lock()
	if !force && !c.wbTargetsAt.IsZero() && time.Since(c.wbTargetsAt) < 30*time.Second {
		t := c.wbTargets
		c.mu.Unlock()
		return t
	}
	c.mu.Unlock()
	t := u.gfTargets()
	c.mu.Lock()
	c.wbTargets, c.wbTargetsAt = t, time.Now()
	c.mu.Unlock()
	return t
}

// ceWritePathsLocked resolves the write scope: checked rows, else openPath. Caller holds s.mu.
func ceWritePathsLocked(s *libSt, openPath string) []string {
	if len(s.collSel) > 0 {
		paths := make([]string, 0, len(s.collSel))
		for p := range s.collSel {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		return paths
	}
	return []string{openPath}
}

// ceWriteCountLocked counts the tracks a write to software sw would touch (≥1 musical
// cue in sw's scope). Render-cheap - the full transform only runs on the write click.
// Caller holds s.mu.
func ceWriteCountLocked(s *libSt, openPath, sw string) int {
	n := 0
	for _, p := range ceWritePathsLocked(s, openPath) {
		tr, ok := s.byPath[p]
		if !ok {
			continue
		}
		if musiclib.MusicalCues(cuepattern.FilterForSoftware(tr.Cues, sw)) > 0 {
			n++
		}
	}
	return n
}

// ceWriteJobs builds the transformed CueUpdate set for target sw: only sw's scope
// exports; the software's defaults then optionally promote memory cues to pads and
// always enforce its pad budget (closest-to-drop, split across drops per pref).
func (u *UI) ceWriteJobs(sw string) []musiclib.CueUpdate {
	c := u.ce()
	c.mu.Lock()
	path, active := c.path, c.active
	c.mu.Unlock()
	if !active {
		return nil
	}
	pref := u.cePrefFor(sw)
	pads := pref.MaxPadsOr()
	s := u.lib()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []musiclib.CueUpdate
	for _, p := range ceWritePathsLocked(s, path) {
		tr, ok := s.byPath[p]
		if !ok {
			continue
		}
		cues := cuepattern.FilterForSoftware(tr.Cues, sw)
		if pref.AutoPromote { // "always promote memory cues to pads for {app}"
			cues, _ = cuepattern.PromoteMemoryToHotcues(cues, "", pads)
		}
		cues, _ = cuepattern.CapPads(cues, s.dropsIdx[p], "", pads, !pref.NoSplitEven)
		if musiclib.MusicalCues(cues) == 0 {
			continue
		}
		out = append(out, musiclib.CueUpdate{Path: p, BPM: tr.BPM, Cues: cues})
	}
	return out
}

// ceWriteHTML renders the write-back section of the cue-editor rail. s is LOCKED by the
// caller (render path); ceSt is locked briefly here - call BEFORE locking it (ceRailHTML).
func (u *UI) ceWriteHTML(s *libSt) string {
	c := u.ce()
	c.mu.Lock()
	active, busy, errStr, openPath := c.active, c.wbBusy, c.wbErr, c.path
	remote := c.remote()
	applied := make(map[string]int, len(c.wbApplied))
	for k, v := range c.wbApplied {
		applied[k] = v
	}
	c.mu.Unlock()
	if !active || remote { // rce mode: the remote save rail replaces the local router
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=pb-label>` + esc(i18n.T("library.ce.writeHeader")) + `</div>`)
	targets := u.ceTargets(false) // cached: discovery fs-probes must not run per repaint
	if len(targets) == 0 {
		b.WriteString(hint("bad", i18n.T("library.gf.noTargets")))
		return b.String()
	}
	var acts []string
	var notes []string
	variant := "primary"
	writable := 0
	for _, t := range targets {
		if n, ok := applied[t.key]; ok {
			writable++
			b.WriteString(hint("ok", i18n.T("library.ce.wroteHint", i18n.A{"app": t.label, "n": fmt.Sprint(n)})))
			continue
		}
		n := ceWriteCountLocked(s, openPath, t.key) // per-target: only its scope's cues count
		if n == 0 {
			continue
		}
		writable++
		if busy {
			continue // one write at a time; rail re-renders when it lands
		}
		acts = append(acts, btn(i18n.T("library.ce.writeTo", i18n.A{"app": t.label, "n": fmt.Sprint(n)}), variant, "ce-write:"+t.key, ""))
		variant = "outline"
		if u.cePrefFor(t.key).AutoPromote {
			notes = append(notes, i18n.T("library.ce.writePromoteNote", i18n.A{"app": t.label}))
		}
		switch t.key {
		case "rekordbox":
			notes = append(notes, i18n.T("library.ce.writeRbNote"))
		case "virtualdj":
			notes = append(notes, i18n.T("library.ce.writeVdjNote"))
		case "serato":
			notes = append(notes, i18n.T("library.ce.writeSeratoNote"))
		}
	}
	if writable == 0 {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.ce.writeNone")) + `</div>`)
		return b.String()
	}
	if errStr != "" {
		b.WriteString(hint("bad", errStr))
	}
	if len(acts) > 0 {
		b.WriteString(`<div class=btn-col>` + strings.Join(acts, "") + `</div>`)
	}
	for _, n := range notes {
		b.WriteString(`<div class=set-note>` + esc(n) + `</div>`)
	}
	b.WriteString(`<div class=set-note>` + esc(i18n.T("library.ce.writeNote")) + `</div>`)
	return b.String()
}

// ceWriteTo routes the cue set into the chosen software's library (backup first).
func (u *UI) ceWriteTo(sw string) {
	c := u.ce()
	c.mu.Lock()
	if !c.active || c.wbBusy || c.remote() { // rce: peer write-back goes through rce-write:
		c.mu.Unlock()
		return
	}
	if _, done := c.wbApplied[sw]; done {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	updates := u.ceWriteJobs(sw)
	if len(updates) == 0 {
		u.toast(i18n.T("library.ce.writeNone"))
		return
	}
	var target *gfTarget
	for _, t := range u.ceTargets(true) { // write click: re-verify targets now
		if t.key == sw {
			tc := t
			target = &tc
			break
		}
	}
	if target == nil {
		u.toast(i18n.T("library.gf.noTargets"))
		return
	}
	c.mu.Lock()
	c.wbBusy, c.wbErr = true, ""
	c.mu.Unlock()
	u.patchMain()
	u.bg(func() {
		res, err := u.ceWriteApply(*target, updates)
		c.mu.Lock()
		c.wbBusy = false
		if err != nil {
			c.wbErr = err.Error()
		} else {
			if c.wbApplied == nil {
				c.wbApplied = map[string]int{}
			}
			c.wbApplied[sw] = res.Updated
		}
		c.mu.Unlock()
		if err != nil {
			u.toast(i18n.T("library.ce.writeFailed") + err.Error())
		} else {
			u.toast(i18n.T("library.ce.wroteHint", i18n.A{"app": target.label, "n": fmt.Sprint(res.Updated)}))
		}
		u.patchMain()
	})
}

// ceWriteApply performs the per-software cue write (off the action goroutine). Backup +
// refuse-while-running discipline lives in cuewriteback (shared with the remotectl peer RPC).
func (u *UI) ceWriteApply(t gfTarget, updates []musiclib.CueUpdate) (musiclib.WritebackResult, error) {
	return cuewriteback.ApplyCues(cuewriteback.Target{Key: t.key, Label: t.label, Path: t.path}, updates, libBackupRoot())
}

func init() {
	onPrefix("ce-write:", func(u *UI, m actMsg) { u.ceWriteTo(m.arg("ce-write:")) })
}
