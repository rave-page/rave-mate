package webui

// Publish tracklist: text-export style options, inline start-offset edits, and the
// capture-aligned "Fix start times" flow. All actions are `pub-`-namespaced; the export
// style persists in config (Features.Recorder.Export*).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/session/sinks/recorder"
)

func init() {
	// start-offset edit + capture-aligned fix (local sets only; the remote tracklist is read-only)
	onPrefix("pub-toff:", func(u *UI, m actMsg) {
		if u.libRemoteTarget() == "" {
			u.pubOffsetEdit(m.arg("pub-toff:"), m.Val)
		}
	})
	onPrefix("pub-fixtimes:", func(u *UI, m actMsg) {
		if u.libRemoteTarget() == "" {
			u.pubFixTimesOpen(m.arg("pub-fixtimes:"))
		}
	})
	onPrefix("pub-fixtimes-do:", func(u *UI, m actMsg) { u.pubFixTimesApply(m.arg("pub-fixtimes-do:")) })
	onPrefix("pub-fixopener:", func(u *UI, m actMsg) { u.pubFixReplan(m.arg("pub-fixopener:"), atoi(m.Val)) })
	onPrefix("pub-trm:", func(u *UI, m actMsg) {
		if u.libRemoteTarget() == "" {
			u.pubRemoveTrack(m.arg("pub-trm:"))
		}
	})
	onPrefix("pub-tctx2:", func(u *UI, m actMsg) {
		if u.libRemoteTarget() == "" {
			u.pubTrackCtx2(m.arg("pub-tctx2:"))
		}
	})

	// text-export style controls (modal re-renders with a fresh preview on every change)
	onPrefix("pub-txt-preset:", func(u *UI, m actMsg) {
		u.pubTxtMut(func(rc *pubTxtCfg) { rc.preset = m.Val })
		u.pubTxtOpen(m.arg("pub-txt-preset:"))
	})
	onPrefix("pub-txt-line:", func(u *UI, m actMsg) {
		u.pubTxtMut(func(rc *pubTxtCfg) { rc.preset, rc.line = "custom", m.Val })
		u.pubTxtOpen(m.arg("pub-txt-line:"))
	})
	onPrefix("pub-txt-header:", func(u *UI, m actMsg) {
		u.pubTxtMut(func(rc *pubTxtCfg) { rc.noHeader = m.Val != "true" })
		u.pubTxtOpen(m.arg("pub-txt-header:"))
	})
}

// ── text-export style (presets + persisted custom template) ────────────────────────

// pubTxtPresets are the offered line styles; "custom" uses the persisted template.
var pubTxtPresets = [][2]string{
	{"classic", "{n}. [{offset}] {track}"},
	{"youtube", "{offset} {track}"},
	{"numbered", "{nn}. {track}"},
	{"plain", "{track}"},
	{"detail", "{n}. [{offset}] {track} · {key} · {bpm} BPM"},
	{"custom", ""},
}

// pubTxtCfg mirrors the persisted export style while a mutation folds through saveCfgBG.
type pubTxtCfg struct {
	preset, line string
	noHeader     bool
}

func (u *UI) pubTxtCfg() pubTxtCfg {
	c := pubTxtCfg{preset: "classic"}
	if u.svc.Cfg != nil {
		rc := u.svc.Cfg.Features.Recorder
		if rc.ExportPreset != "" {
			c.preset = rc.ExportPreset
		}
		c.line, c.noHeader = rc.ExportLine, rc.ExportNoHeader
	}
	return c
}

// pubTxtMut applies a style change and persists it off the act lane.
func (u *UI) pubTxtMut(fn func(*pubTxtCfg)) {
	if u.svc.Cfg == nil {
		return
	}
	c := u.pubTxtCfg()
	fn(&c)
	rc := &u.svc.Cfg.Features.Recorder
	rc.ExportPreset, rc.ExportLine, rc.ExportNoHeader = c.preset, c.line, c.noHeader
	u.saveCfgBG("pub-export-style", nil, nil)
}

// pubTxtOpts resolves the persisted style into recorder options.
func (u *UI) pubTxtOpts() recorder.TextOptions {
	c := u.pubTxtCfg()
	line := c.line
	for _, p := range pubTxtPresets {
		if p[0] == c.preset && p[1] != "" {
			line = p[1]
			break
		}
	}
	opts := recorder.TextOptions{Line: line} // "" falls back to the recorder default
	if !c.noHeader {
		opts.Header = recorder.DefaultTextOptions().Header
	}
	return opts
}

// pubTxtOpen renders the text-export dialog: style preset, editable line template, header
// toggle and a live preview. Every control change re-opens it with fresh content.
func (u *UI) pubTxtOpen(id string) {
	if u.svc.Recorder == nil {
		return
	}
	c := u.pubTxtCfg()
	opts := u.pubTxtOpts()
	content, err := u.svc.Recorder.ExportText(id, opts)
	if err != nil {
		u.toast(i18n.T("publish.toast.exportFailed") + err.Error())
		return
	}
	line := opts.Line
	if line == "" {
		line = recorder.DefaultTextOptions().Line
	}
	opts01 := make([]ssOpt, 0, len(pubTxtPresets))
	for _, p := range pubTxtPresets {
		opts01 = append(opts01, ssOpt{Val: p[0], Label: i18n.T("publish.textExport.preset." + p[0])})
	}
	// smartSelect with an explicit id: register + resolve, then hand selHTML the plain label
	// (byte-identical to smartSelect's own label handling).
	sel := resolveSmartSelect("pub-txt-preset", "pub-txt-preset:"+id, c.preset, func() []ssOpt { return opts01 })
	sel.Label = i18n.T("publish.textExport.style")
	u.openModal(pubTxtDlgHTML(pubTxtDlgSt{
		Title:   i18n.T("publish.textExport.title"),
		Sel:     sel,
		Tmpl:    newField(i18n.T("publish.textExport.template"), "pub-txt-line:"+id, line, "text"),
		Header:  newToggle(i18n.T("publish.textExport.header"), "pub-txt-header:"+id, !c.noHeader),
		Place:   i18n.T("publish.textExport.placeholders") + " {n} {nn} {offset} {artist} {title} {track} {album} {key} {bpm} {deck}",
		Content: content, CopyLbl: i18n.T("common.copy"), CloseLbl: i18n.T("common.close"),
	}))
}

// ── inline start-offset edit ───────────────────────────────────────────────────────

// pubOffsetEdit parses an edited "[h:]m:ss" offset and moves the track's start. The
// mutation drains the persist queue (fsync) - always off the act lane.
func (u *UI) pubOffsetEdit(arg, val string) {
	recID, idxStr, _ := strings.Cut(arg, "\x1f")
	idx := atoi(idxStr)
	if u.svc.Recorder == nil || recID == "" || idx < 0 {
		return
	}
	d, err := recorder.ParseClock(val)
	if err != nil {
		u.toast(i18n.T("publish.toast.badOffset", i18n.A{"v": val}))
		u.patchMain() // restore the displayed value
		return
	}
	u.bg(func() {
		rec, ok := u.svc.Recorder.Get(recID)
		if !ok {
			return
		}
		if _, err := u.svc.Recorder.SetTrackStart(recID, idx, rec.StartedAt.Add(d)); err != nil {
			u.logErr("edit track start", err)
			if !u.stopped() {
				u.toast(i18n.T("publish.toast.offsetFailed") + err.Error())
			}
			return
		}
		if !u.stopped() {
			u.patchMain()
		}
	})
}

// ── capture-aligned time fix ───────────────────────────────────────────────────────

// pubFixCtx is one previewed fix + the probe inputs it came from, kept between the
// preview modal, opener re-plans and Apply. Bounded: one entry per set, overwritten per
// open, deleted on apply.
type pubFixCtx struct {
	fix   recorder.TimeFix
	capr  libdb.SetRecording
	lead  float64
	fader bool // reconstructed from fader history (authoritative - no opener select)
}

var pubFixPlans = struct {
	sync.Mutex
	m map[string]pubFixCtx
}{m: map[string]pubFixCtx{}}

// pubFixTimesOpen probes the set's capture for leading silence, plans the correction and
// opens a preview modal. Probe + plan run off the act lane.
func (u *UI) pubFixTimesOpen(id string) {
	if u.svc.Recorder == nil {
		return
	}
	caps, _ := u.pubCaptures()
	sets := caps[id]
	if len(sets) == 0 {
		u.toast(i18n.T("publish.fix.noCapture"))
		return
	}
	capr := sets[0]
	for _, s := range sets {
		if pubIsAudio(s.Path) { // prefer the broadcast audio over an OBS video
			capr = s
			break
		}
	}
	u.toast(i18n.T("publish.fix.analyzing"))
	u.bg(func() {
		rec, ok := u.svc.Recorder.Get(id)
		if !ok || rec.EndedAt.IsZero() {
			u.toast(i18n.T("publish.fix.notReady")) // silent return here read as "silence detect does nothing"
			return
		}
		lead := 0.0
		if am := u.svc.Automations; am != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if res, err := am.ProbeSilence(ctx, capr.Path, 0, 0); err == nil {
				lead = res.LeadingSilenceSeconds
			} else {
				u.logErr("fix-times silence probe", err) // capture-start alignment still applies
			}
		}
		// Fader history is the exact mechanism (audio anchors 0:00, each track starts at
		// its deck's first fader-up); the silence+opener heuristic is the fallback.
		if evs := u.pubFaderEvents(rec); len(evs) > 0 {
			if fix, ok := recorder.PlanFaderFix(rec, capr.StartedAt, capr.EndedAt, time.Duration(lead*float64(time.Second)), evs); ok {
				pubFixPlans.Lock()
				pubFixPlans.m[id] = pubFixCtx{fix: fix, capr: capr, lead: lead, fader: true}
				pubFixPlans.Unlock()
				if !u.stopped() {
					u.openModal(pubFixModal(rec, capr, lead, fix, true))
				}
				return
			}
		}
		u.pubFixPlan(id, rec, capr, lead, -1)
	})
}

// pubFaderEvents loads a set's fader history: its own OnAirLog (always-on going forward),
// else the raw Traktor payload log windowed to the set (LogPayloads, default on).
func (u *UI) pubFaderEvents(rec recorder.Recording) []recorder.DeckEvent {
	if len(rec.OnAirLog) > 0 {
		return recorder.FaderEventsFromOnAirLog(rec.OnAirLog)
	}
	p, err := config.DataPath("traktor-payloads.jsonl")
	if err != nil {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	return recorder.ParseTraktorPayloadLog(f, rec.StartedAt.Add(-15*time.Minute), rec.EndedAt.Add(time.Minute))
}

// pubFixPlan (re)plans heuristically with the given opener, stashes the context and shows
// the preview.
func (u *UI) pubFixPlan(id string, rec recorder.Recording, capr libdb.SetRecording, lead float64, opener int) {
	fix, planned := recorder.PlanTimeFix(rec, capr.StartedAt, capr.EndedAt, time.Duration(lead*float64(time.Second)), opener)
	if !planned {
		if !u.stopped() {
			u.toast(i18n.T("publish.fix.nothing"))
		}
		return
	}
	pubFixPlans.Lock()
	pubFixPlans.m[id] = pubFixCtx{fix: fix, capr: capr, lead: lead}
	pubFixPlans.Unlock()
	if !u.stopped() {
		u.openModal(pubFixModal(rec, capr, lead, fix, false))
	}
}

// pubFixReplan re-plans a stashed fix with a hand-picked opener (no re-probe).
func (u *UI) pubFixReplan(id string, opener int) {
	pubFixPlans.Lock()
	ctx, ok := pubFixPlans.m[id]
	pubFixPlans.Unlock()
	if !ok || u.svc.Recorder == nil {
		return
	}
	u.bg(func() {
		if rec, found := u.svc.Recorder.Get(id); found {
			u.pubFixPlan(id, rec, ctx.capr, ctx.lead, opener)
		}
	})
}

// pubFixModal previews a planned fix: what the set start becomes, which pre-audio
// phantom opens the recording (selectable when the heuristic guessed - fader-history
// plans are exact), which tracks get removed, and every offset that changes.
func pubFixModal(rec recorder.Recording, capr libdb.SetRecording, lead float64, fix recorder.TimeFix, fader bool) string {
	return pubFixDlgHTML(pubFixModalState(rec, capr, lead, fix, fader))
}

// pubFixModalState resolves the time-fix preview: description, opener picker (registering its
// smart select, exactly where the old renderer did), the set-start line and every row whose
// displayed offset changes.
func pubFixModalState(rec recorder.Recording, capr libdb.SetRecording, lead float64, fix recorder.TimeFix, fader bool) pubFixDlgSt {
	descKey := "publish.fix.desc"
	if fader {
		descKey = "publish.fix.descFader"
	}
	st := pubFixDlgSt{
		Title:       i18n.T("publish.fix.title"),
		Desc:        i18n.T(descKey, i18n.A{"file": filepath.Base(capr.Path), "lead": pubClock(lead)}),
		Opener:      emptySel(),
		SetStartLbl: i18n.T("publish.fix.setStart"),
		StartT:      rec.StartedAt.Local().Format("15:04:05"),
		NewT:        fix.NewStart.Local().Format("15:04:05"),
		RemovedTx:   i18n.T("publish.fix.removedRow"),
		ApplyLbl:    i18n.T("publish.fix.apply"),
		ApplyAct:    "pub-fixtimes-do:" + rec.ID,
		CancelLbl:   i18n.T("common.cancel"),
	}

	// Opener choice (heuristic plans only): the file can't order tracks that predate its
	// audible start - the workflow default is preselected, the DJ can overrule.
	audio := fix.NewStart
	if !fader {
		var cands []ssOpt
		for i, t := range rec.Tracks {
			if t.StartedAt.Before(audio) || i == fix.Opener {
				cands = append(cands, ssOpt{Val: fmt.Sprint(i), Label: fmt.Sprintf("%d. %s", i+1, orTrackLine(pubTrackLine(t)))})
			}
		}
		if len(cands) > 1 {
			sel := resolveSmartSelect("pub-fix-opener", "pub-fixopener:"+rec.ID, fmt.Sprint(fix.Opener), func() []ssOpt { return cands })
			sel.Label = i18n.T("publish.fix.opener")
			st.HasOpener, st.Opener = true, sel
		}
	}

	removed := map[int]bool{}
	for _, i := range fix.RemoveTracks {
		removed[i] = true
	}
	// Preview the resulting OFFSETS for every track (the rebased set start shifts them all,
	// not only the clamped ones) - rows whose displayed offset survives unchanged are skipped.
	for i, t := range rec.Tracks {
		oldOff := pubClock(t.StartedAt.Sub(rec.StartedAt).Seconds())
		label := orTrackLine(pubTrackLine(t))
		if removed[i] {
			st.Rows = append(st.Rows, pubFixRowSt{Num: fmt.Sprint(i + 1), Off: oldOff, Removed: true, Label: label})
			continue
		}
		ns, moved := fix.TrackStarts[i]
		if !moved {
			ns = t.StartedAt
		}
		newOff := pubClock(ns.Sub(fix.NewStart).Seconds())
		if oldOff == newOff {
			continue
		}
		st.Rows = append(st.Rows, pubFixRowSt{Num: fmt.Sprint(i + 1), Off: oldOff, NewOff: newOff, Label: label})
	}
	return st
}

// pubFixTimesApply commits the previewed plan (drains the persist queue - off the act lane).
func (u *UI) pubFixTimesApply(id string) {
	pubFixPlans.Lock()
	ctx, ok := pubFixPlans.m[id]
	delete(pubFixPlans.m, id)
	pubFixPlans.Unlock()
	u.closeModal()
	if !ok || u.svc.Recorder == nil {
		return
	}
	u.bg(func() {
		if _, err := u.svc.Recorder.ApplyTimeFix(id, ctx.fix); err != nil {
			u.logErr("apply time fix", err)
			if !u.stopped() {
				u.toast(i18n.T("publish.fix.failed") + err.Error())
			}
			return
		}
		if !u.stopped() {
			u.toast(i18n.T("publish.fix.applied"))
			u.patchMain()
		}
	})
}

// pubTrackCtx2 is the finished-set tracklist row context menu: works-together marking for
// library-resolved rows (same flows as pubTrackCtxModal) + remove-from-tracklist.
func (u *UI) pubTrackCtx2(arg string) {
	recID, rest, _ := strings.Cut(arg, "\x1f")
	idxStr, path, _ := strings.Cut(rest, "\x1f")
	var row []uiBtn
	if path != "" {
		row = u.pubCompatBtns(path)
	}
	row = append(row, uiBtn{Label: i18n.T("publish.track.remove"), Variant: "destructive",
		Act: "pub-trm:" + recID + "\x1f" + idxStr})
	title := filepath.Base(path)
	if path == "" {
		title = i18n.T("publish.track.n", i18n.A{"n": fmt.Sprint(atoi(idxStr) + 1)})
	}
	u.openModal(dlgChoiceHTML(dlgChoiceSt{Title: title, InBody: true, Btns: row}))
}

// pubRemoveTrack drops one track from a finished set's tracklist (context-menu action).
func (u *UI) pubRemoveTrack(arg string) {
	recID, idxStr, _ := strings.Cut(arg, "\x1f")
	idx := atoi(idxStr)
	u.closeModal()
	if u.svc.Recorder == nil || recID == "" || idx < 0 {
		return
	}
	u.bg(func() {
		if _, err := u.svc.Recorder.RemoveTrack(recID, idx); err != nil {
			u.logErr("remove track", err)
			if !u.stopped() {
				u.toast(i18n.T("publish.fix.failed") + err.Error())
			}
			return
		}
		if !u.stopped() {
			u.toast(i18n.T("publish.toast.trackRemoved"))
			u.patchMain()
		}
	})
}
