package webui

// Publish tracklist: text-export style options, inline start-offset edits, and the
// capture-aligned "Fix start times" flow. All actions are `pub-`-namespaced; the export
// style persists in config (Features.Recorder.Export*).

import (
	"context"
	"fmt"
	"html"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	body := `<div class=pub-txt-opts>` +
		`<span class=pub-txt-presel>` + smartSelect("pub-txt-preset", i18n.T("publish.textExport.style"), "pub-txt-preset:"+id, c.preset, func() []ssOpt { return opts01 }) + `</span>` +
		field(i18n.T("publish.textExport.template"), "pub-txt-line:"+id, line, "text") +
		toggleRow(i18n.T("publish.textExport.header"), "pub-txt-header:"+id, !c.noHeader) +
		`</div>` +
		`<p class=page-sub>` + html.EscapeString(i18n.T("publish.textExport.placeholders")+" {n} {nn} {offset} {artist} {title} {track} {album} {key} {bpm} {deck}") + `</p>` +
		`<textarea class=pub-export-ta readonly rows=12>` + html.EscapeString(content) + `</textarea>`
	footer := `<button class="rp-btn rp-btn--primary" data-act="copy" data-val="` + html.EscapeString(content) + `">` + html.EscapeString(i18n.T("common.copy")) + `</button>` +
		btn(i18n.T("common.close"), "outline", "modal-close", "")
	u.openModal(modal(i18n.T("publish.textExport.title"), body, footer))
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

// pubFixPlans holds the previewed fix per recording between the preview modal and Apply.
// Bounded: one entry per set, overwritten per open, deleted on apply.
var pubFixPlans = struct {
	sync.Mutex
	m map[string]recorder.TimeFix
}{m: map[string]recorder.TimeFix{}}

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
		fix, planned := recorder.PlanTimeFix(rec, capr.StartedAt, time.Duration(lead*float64(time.Second)))
		if !planned {
			if !u.stopped() {
				u.toast(i18n.T("publish.fix.nothing"))
			}
			return
		}
		pubFixPlans.Lock()
		pubFixPlans.m[id] = fix
		pubFixPlans.Unlock()
		if !u.stopped() {
			u.openModal(pubFixModal(rec, capr, lead, fix))
		}
	})
}

// pubFixModal previews a planned fix: what the set start becomes and every track offset
// that changes, before anything is written.
func pubFixModal(rec recorder.Recording, capr libdb.SetRecording, lead float64, fix recorder.TimeFix) string {
	var b strings.Builder
	b.WriteString(`<div class=np-artist>` + html.EscapeString(i18n.T("publish.fix.desc", i18n.A{
		"file": filepath.Base(capr.Path), "lead": pubClock(lead)})) + `</div>`)
	b.WriteString(`<div class=pub-fix-rows>`)
	b.WriteString(`<div class=pub-fix-row><span class=pub-track-l>` + html.EscapeString(i18n.T("publish.fix.setStart")) + `</span>` +
		`<span class=pub-track-o>` + rec.StartedAt.Local().Format("15:04:05") + ` → ` + fix.NewStart.Local().Format("15:04:05") + `</span></div>`)
	for i, t := range rec.Tracks {
		ns, changed := fix.TrackStarts[i]
		if !changed {
			continue
		}
		oldOff := pubClock(t.StartedAt.Sub(rec.StartedAt).Seconds())
		newOff := pubClock(ns.Sub(fix.NewStart).Seconds())
		b.WriteString(`<div class=pub-fix-row><span class=pub-track-n>` + fmt.Sprint(i+1) + `.</span>` +
			`<span class=pub-track-o>[` + oldOff + `] → [` + newOff + `]</span>` +
			`<span class=pub-track-l>` + html.EscapeString(orTrackLine(pubTrackLine(t))) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	footer := btnRow(
		btn(i18n.T("publish.fix.apply"), "primary", "pub-fixtimes-do:"+rec.ID, ""),
		btn(i18n.T("common.cancel"), "ghost", "modal-close", ""),
	)
	return modal(i18n.T("publish.fix.title"), b.String(), footer)
}

// pubFixTimesApply commits the previewed plan (drains the persist queue - off the act lane).
func (u *UI) pubFixTimesApply(id string) {
	pubFixPlans.Lock()
	fix, ok := pubFixPlans.m[id]
	delete(pubFixPlans.m, id)
	pubFixPlans.Unlock()
	u.closeModal()
	if !ok || u.svc.Recorder == nil {
		return
	}
	u.bg(func() {
		if _, err := u.svc.Recorder.ApplyTimeFix(id, fix); err != nil {
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
