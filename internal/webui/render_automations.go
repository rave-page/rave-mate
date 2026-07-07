package webui

import (
	"html"
	"strings"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/i18n"
)

// renderAutomations: file-arrival + scheduled action chains (list/enable/delete + read-only
// schedules & recent runs). Editors/run-now (need a file picker) are follow-ups.
func (u *UI) renderAutomations() string {
	if u.svc.Automations == nil {
		return panel(i18n.T("tab.automations"), "") + emptyState(i18n.T("automations.unavailable"))
	}
	return panel(i18n.T("tab.automations"), i18n.T("automations.subtitle")) + `<div id=auto-body>` + u.autoBody() + `</div>`
}

func (u *UI) autoBody() string {
	var b strings.Builder
	b.WriteString(section(i18n.T("tab.automations"), u.autoListHTML(u.svc.Automations.List())))
	b.WriteString(section(i18n.T("automations.schedules"), u.autoSchedulesHTML(u.svc.Automations.ListSchedules())))
	b.WriteString(section(i18n.T("automations.recentRuns"), u.autoRunsHTML(u.svc.Automations.Runs(20))))
	return b.String()
}

func (u *UI) autoListHTML(autos []automation.Automation) string {
	if len(autos) == 0 {
		return emptyState(i18n.T("automations.emptyList"))
	}
	var b strings.Builder
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
		label := a.Label
		if label == "" {
			label = i18n.T("automations.unnamed")
		}
		b.WriteString(`<div class="rp-card"><div class=card-label>` + html.EscapeString(label) + `</div>` +
			`<div class=np-artist>` + html.EscapeString(a.WatchDir) + `</div>` +
			`<div class=np-meta>` + status + `</div>` +
			toggleRow(i18n.T("common.enabledCap"), "auto-toggle:"+a.ID, a.Enabled) +
			btnRow(btn(i18n.T("common.delete"), "destructive", "auto-del:"+a.ID, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func (u *UI) autoSchedulesHTML(scheds []automation.Schedule) string {
	if len(scheds) == 0 {
		return emptyState(i18n.T("automations.noSchedules"))
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	for _, s := range scheds {
		state := i18n.T("common.off")
		if s.Enabled {
			state = string(s.Kind)
		}
		b.WriteString(kv(orDash(s.Label), state))
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
