package webui

import (
	"fmt"
	"html"
	"path/filepath"
	"strings"

	"rave.page/mate/internal/appgroups"
	"rave.page/mate/internal/i18n"
)

// renderAppGroups: crash-recovery launcher - list groups, running count, Launch.
func (u *UI) renderAppGroups() string {
	if u.svc.AppGroups == nil {
		return panel(i18n.T("tab.appgroups"), "") + emptyState(i18n.T("appgroups.unavailable"))
	}
	return panel(i18n.T("tab.appgroups"), i18n.T("appgroups.subtitle")) +
		`<div id=appgroups-body>` + u.appGroupsBody() + `</div>`
}

func (u *UI) appGroupsBody() string {
	groups := u.svc.Cfg.Features.AppGroups.Groups
	if len(groups) == 0 {
		return emptyState(i18n.T("appgroups.empty"))
	}
	// One process-table snapshot for ALL groups (was one full walk per group per render/tick).
	var counts []appgroups.Counts
	if u.svc.AppGroups != nil {
		counts = u.svc.AppGroups.RunningCounts(groups)
	} else {
		counts = make([]appgroups.Counts, len(groups))
	}
	var b strings.Builder
	b.WriteString(`<div class=grid>`)
	for i, g := range groups {
		running, total := counts[i].Running, counts[i].Total
		variant := "muted"
		if running > 0 && running >= total {
			variant = "success"
		} else if running > 0 {
			variant = "warning"
		}
		var apps strings.Builder
		for _, a := range g.Apps {
			tag := ""
			if a.Elevated {
				tag = ` ` + badge(i18n.T("appgroups.admin"), "warning")
			}
			apps.WriteString(`<div class=kv><span class=kv-k>` + html.EscapeString(filepath.Base(a.Path)) + tag + `</span><span class=kv-v></span></div>`)
		}
		b.WriteString(`<div class="rp-card"><div class=card-label>` + html.EscapeString(g.Name) + `</div>` +
			`<div class=np-meta>` + badgeDot(i18n.T("appgroups.upCount", i18n.A{"running": fmt.Sprint(running), "total": fmt.Sprint(total)}), variant) + `</div>` +
			apps.String() +
			btnRow(btn(i18n.T("common.launch"), "go", "ag-launch:"+g.ID, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// badgeDot pairs a status dot with a badge.
func badgeDot(text, variant string) string { return dot(variant) + ` ` + badge(text, variant) }
