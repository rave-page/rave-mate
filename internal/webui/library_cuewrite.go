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

// ceWriteJobsLocked builds the CueUpdate set the router would write (checked rows, else
// openPath; tracks without musical cues are dropped). Caller holds s.mu.
func ceWriteJobsLocked(s *libSt, openPath string) []musiclib.CueUpdate {
	var paths []string
	if len(s.collSel) > 0 {
		for p := range s.collSel {
			paths = append(paths, p)
		}
		sort.Strings(paths)
	} else {
		paths = []string{openPath}
	}
	var out []musiclib.CueUpdate
	for _, p := range paths {
		tr, ok := s.byPath[p]
		if !ok || ceCueCount(tr.Cues) == 0 {
			continue
		}
		out = append(out, musiclib.CueUpdate{Path: p, BPM: tr.BPM, Cues: tr.Cues})
	}
	return out
}

// ceWriteJobs is the self-locking variant for action handlers (no locks held).
func (u *UI) ceWriteJobs() []musiclib.CueUpdate {
	c := u.ce()
	c.mu.Lock()
	path, active := c.path, c.active
	c.mu.Unlock()
	if !active {
		return nil
	}
	s := u.lib()
	s.mu.Lock()
	defer s.mu.Unlock()
	return ceWriteJobsLocked(s, path)
}

// ceWriteHTML renders the write-back section of the cue-editor rail. s is LOCKED by the
// caller (render path); ceSt is locked briefly here - call BEFORE locking it (ceRailHTML).
func (u *UI) ceWriteHTML(s *libSt) string {
	c := u.ce()
	c.mu.Lock()
	active, busy, errStr, openPath := c.active, c.wbBusy, c.wbErr, c.path
	applied := make(map[string]int, len(c.wbApplied))
	for k, v := range c.wbApplied {
		applied[k] = v
	}
	c.mu.Unlock()
	if !active {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=pb-label>` + esc(i18n.T("library.ce.writeHeader")) + `</div>`)
	updates := ceWriteJobsLocked(s, openPath)
	if len(updates) == 0 {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.ce.writeNone")) + `</div>`)
		return b.String()
	}
	targets := u.ceTargets(false) // cached: discovery fs-probes must not run per repaint
	if len(targets) == 0 {
		b.WriteString(hint("bad", i18n.T("library.gf.noTargets")))
		return b.String()
	}
	var acts []string
	var notes []string
	variant := "primary"
	for _, t := range targets {
		if n, ok := applied[t.key]; ok {
			b.WriteString(hint("ok", i18n.T("library.ce.wroteHint", i18n.A{"app": t.label, "n": fmt.Sprint(n)})))
			continue
		}
		if busy {
			continue // one write at a time; rail re-renders when it lands
		}
		acts = append(acts, btn(i18n.T("library.ce.writeTo", i18n.A{"app": t.label, "n": fmt.Sprint(len(updates))}), variant, "ce-write:"+t.key, ""))
		variant = "outline"
		switch t.key {
		case "rekordbox":
			notes = append(notes, i18n.T("library.ce.writeRbNote"))
		case "virtualdj":
			notes = append(notes, i18n.T("library.ce.writeVdjNote"))
		case "serato":
			notes = append(notes, i18n.T("library.ce.writeSeratoNote"))
		}
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
	if !c.active || c.wbBusy {
		c.mu.Unlock()
		return
	}
	if _, done := c.wbApplied[sw]; done {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	updates := u.ceWriteJobs()
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
