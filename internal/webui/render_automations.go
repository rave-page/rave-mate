package webui

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/i18n"
)

// renderAutomations: file-arrival + scheduled action chains (list/create/edit/enable/delete,
// schedules, run-now, recent runs). The create/edit form lives in automations_editor.go, the
// schedule form in automations_schedules.go, and run-now in automations_runnow.go.
func (u *UI) renderAutomations() string {
	if u.svc.Automations == nil {
		return panel(i18n.T("tab.automations"), "") + emptyState(i18n.T("automations.unavailable"))
	}
	return panel(i18n.T("tab.automations"), i18n.T("automations.subtitle")) + `<div id=auto-body>` + u.autoBody() + `</div>`
}

func (u *UI) autoBody() string {
	autos := u.svc.Automations.List() // listed once: the schedule cards name their target from it
	var b strings.Builder
	b.WriteString(section(i18n.T("tab.automations"), u.autoListHTML(autos)))
	b.WriteString(section(i18n.T("automations.schedules"), u.autoSchedulesHTML(u.svc.Automations.ListSchedules(), autos)))
	b.WriteString(section(i18n.T("automations.recentRuns"), u.autoRunsHTML(u.svc.Automations.Runs(20))))
	return b.String()
}

func (u *UI) autoListHTML(autos []automation.Automation) string {
	newBtn := btnRow(btn(i18n.T("automations.new"), "primary", "auto-new", ""))
	if len(autos) == 0 {
		return newBtn + emptyState(i18n.T("automations.emptyList"))
	}
	var b strings.Builder
	b.WriteString(newBtn)
	b.WriteString(`<div class=grid>`)
	for _, a := range autos {
		status := ""
		if a.LastStatus != "" {
			v := "secondary"
			switch a.LastStatus {
			case "success":
				v = "success"
			case "error":
				v = "error"
			case "partial":
				v = "warning"
			}
			status = badge(a.LastStatus, v)
		}
		b.WriteString(`<div class="rp-card"><div class=card-label>` + html.EscapeString(autoLabelOf(a.Label)) + `</div>` +
			`<div class=np-artist>` + html.EscapeString(a.WatchDir) + `</div>` +
			`<div class=np-meta>` + status + `</div>` +
			`<div class=np-meta>` + html.EscapeString(autoChainSummary(a.Actions)) + `</div>` +
			toggleRow(i18n.T("common.enabledCap"), "auto-toggle:"+a.ID, a.Enabled) +
			btnRow(btn(i18n.T("automations.run.btn"), "go", "auto-run:"+a.ID, ""),
				btn(i18n.T("automations.sch.add"), "outline", "auto-sch-add:"+a.ID, ""),
				btn(i18n.T("common.edit"), "outline", "auto-edit:"+a.ID, ""),
				btn(i18n.T("common.delete"), "destructive", "auto-del:"+a.ID, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// autoSchedulesHTML renders the schedule cards. autos supplies each schedule's target name (a
// Schedule stores only the automation's id) and is passed in so autoBody's List() stays single.
//
// Creating a schedule needs an automation to point at; RENDERING one does not. Gating the whole
// section on len(autos)>0 hid every existing schedule - and its delete/toggle controls - while the
// scheduler kept firing them: a nightly delete-purge with no UI to see or stop it. The cards
// render regardless; only the New button is gated.
func (u *UI) autoSchedulesHTML(scheds []automation.Schedule, autos []automation.Automation) string {
	newBtn := btnRow(btn(i18n.T("automations.sch.new"), "primary", "auto-sch-new", ""))
	if len(autos) == 0 {
		newBtn = btnRow(btnGated(i18n.T("automations.sch.new"), i18n.T("automations.sch.needAutomation")))
	}
	if len(scheds) == 0 {
		if len(autos) == 0 {
			return newBtn + emptyState(i18n.T("automations.sch.needAutomation"))
		}
		return newBtn + emptyState(i18n.T("automations.noSchedules"))
	}
	byID := make(map[string]automation.Automation, len(autos))
	for _, a := range autos {
		byID[a.ID] = a // scalars + read-only slice reads; never mutated (elements alias the service cache)
	}
	var b strings.Builder
	b.WriteString(newBtn)
	b.WriteString(`<div class=grid>`)
	for _, s := range scheds {
		a, ok := byID[s.AutomationID]
		target := autoLabelOf(a.Label)
		warn := ""
		switch {
		case !ok:
			// Its automation is gone, so onSchedule skips every fire. Service.Delete cascades now,
			// so this is data from before the cascade (or a store that refused one) - it must be
			// visible AND deletable, never hidden behind an empty state.
			target = i18n.T("automations.sch.missingAutomation")
			warn = hint("bad", i18n.T("automations.sch.orphanWarn"))
		case s.Enabled && !a.Enabled:
			// Armed, but onSchedule skips the fire while the automation itself is off. The card would
			// otherwise show an enabled schedule against a trigger summary that never happens.
			warn = hint("warn", i18n.T("automations.sch.automationOffWarn"))
		}
		state := badge(i18n.T("common.off"), "secondary")
		if s.Enabled {
			state = badge(string(s.Kind), "info")
		}
		b.WriteString(`<div class="rp-card"><div class=card-label>` + html.EscapeString(autoLabelOf(s.Label)) + `</div>` +
			`<div class=np-artist>` + html.EscapeString(target) + `</div>` +
			`<div class=np-meta>` + state + ` ` + html.EscapeString(autoTriggerSummary(s)) + `</div>` +
			`<div class=np-meta>` + html.EscapeString(autoGateSummary(s)) + `</div>` +
			`<div class=np-meta>` + html.EscapeString(autoLastFired(s)) + `</div>` + warn +
			toggleRow(i18n.T("common.enabledCap"), "auto-sch-tgl:"+s.ID, s.Enabled) +
			btnRow(btn(i18n.T("common.edit"), "outline", "auto-sch-edit:"+s.ID, ""),
				btn(i18n.T("common.delete"), "destructive", "auto-sch-del:"+s.ID, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func (u *UI) autoRunsHTML(runs []automation.Run) string {
	if len(runs) == 0 {
		return emptyState(i18n.T("automations.noRuns"))
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	for _, r := range runs {
		v := "secondary"
		switch r.Status {
		case "success":
			v = "success"
		case "error":
			v = "error"
		case "running":
			v = "info"
		}
		b.WriteString(`<div class=kv><span class=kv-k>` + html.EscapeString(shortPath(r.FilePath)) + ` <span class=np-artist>` + html.EscapeString(r.Trigger) + `</span></span>` +
			`<span class=kv-v>` + badge(r.Status, v) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// autoChainSummary renders the chain as "Trim silence → Transcode → Move to" so the card says
// what the automation actually does without opening the editor.
func autoChainSummary(acts []automation.Action) string {
	if len(acts) == 0 {
		return i18n.T("automations.ed.noSteps")
	}
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		out = append(out, aeTypeLabel(a.Type))
	}
	return strings.Join(out, " → ")
}

// autoChainDeletes reports the chain erases the recording it started from. ValidateActions keeps
// delete terminal, so in any chain saved since it existed the delete is last - but scan the whole
// chain: older persisted chains can still carry one mid-chain (the engine stops there regardless).
func autoChainDeletes(acts []automation.Action) bool {
	for _, a := range acts {
		if a.Type == automation.ActionDelete {
			return true
		}
	}
	return false
}

// autoTriggerSummary renders a schedule's trigger as "every 60 min" / "daily at 09:00" so the
// card says when it fires without opening the form. Mirrors what the scheduler actually arms,
// including the blank-field defaults asBuild materializes.
func autoTriggerSummary(s automation.Schedule) string {
	switch s.Kind {
	case automation.ScheduleDaily:
		return i18n.T("automations.sch.sumDaily", i18n.A{"at": fmt.Sprintf("%02d:%02d", s.AtHour, s.AtMinute)})
	case automation.ScheduleCron:
		return s.CronExpr
	case automation.ScheduleIdle:
		return i18n.T("automations.sch.sumIdle", i18n.A{"n": strconv.Itoa(asPos(s.IdleMinutes, asDefaultIdle))})
	}
	return i18n.T("automations.sch.sumInterval", i18n.A{"n": strconv.Itoa(asPos(s.IntervalMinutes, asDefaultInterval))})
}

// autoGateSummary renders the conditions that can hold a fire back ("" gates omitted).
func autoGateSummary(s automation.Schedule) string {
	var parts []string
	if s.RequireIdleMinutes > 0 {
		parts = append(parts, i18n.T("automations.sch.sumRequireIdle", i18n.A{"n": strconv.Itoa(s.RequireIdleMinutes)}))
	}
	if len(s.RequireAppsRunning) > 0 {
		parts = append(parts, i18n.T("automations.sch.sumRequireApps", i18n.A{"apps": strings.Join(s.RequireAppsRunning, ", ")}))
	}
	if len(s.ExcludeAppsRunning) > 0 {
		parts = append(parts, i18n.T("automations.sch.sumExcludeApps", i18n.A{"apps": strings.Join(s.ExcludeAppsRunning, ", ")}))
	}
	if len(parts) == 0 {
		return i18n.T("automations.sch.sumNoGates")
	}
	return strings.Join(parts, " · ")
}

func autoLastFired(s automation.Schedule) string {
	if s.LastFiredAt == "" {
		return i18n.T("automations.sch.neverFired")
	}
	return i18n.T("automations.sch.lastFired", i18n.A{"at": s.LastFiredAt})
}

// autoLabelOf falls back to the "(unnamed)" placeholder - an automation/schedule with a blank
// label must still be identifiable enough to click.
func autoLabelOf(label string) string {
	if strings.TrimSpace(label) == "" {
		return i18n.T("automations.unnamed")
	}
	return label
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func shortPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	if p == "" {
		return i18n.T("automations.manual")
	}
	return p
}
