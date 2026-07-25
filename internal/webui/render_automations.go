package webui

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/zigui"
)

// Automations is a Zig-rendered tab (native/zigui/src/automations.zig): Go resolves
// all state (service data + i18n) into autoState, the Zig lib renders HTML
// byte-identical to the Go renderers below (fallback + golden reference,
// zigui_golden_automations_test.go).

// autoLabels are the per-card control labels (shared by automation + schedule cards).
type autoLabels struct {
	Enabled   string `json:"enabled"`
	EnabledDL string `json:"enabledDl"` // strings.ToLower(Enabled)
	Run       string `json:"run"`
	SchAdd    string `json:"schAdd"`
	Edit      string `json:"edit"`
	Delete    string `json:"delete"`
}

// autoCard is one automation card.
type autoCard struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	WatchDir  string `json:"watchDir"`
	Status    string `json:"status"` // LastStatus; "" = no badge
	StatusVar string `json:"statusVar"`
	Chain     string `json:"chain"`
	Enabled   bool   `json:"enabled"`
}

// autoListState is the automations-list section.
type autoListState struct {
	New   string     `json:"new"`
	Empty string     `json:"empty"`
	Cards []autoCard `json:"cards"`
}

// autoSchedCard is one schedule card.
type autoSchedCard struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Target    string `json:"target"`
	StateText string `json:"stateText"`
	StateVar  string `json:"stateVar"`
	Trigger   string `json:"trigger"`
	Gates     string `json:"gates"`
	LastFired string `json:"lastFired"`
	WarnTone  string `json:"warnTone"` // "" = no warning
	WarnText  string `json:"warnText"`
	Enabled   bool   `json:"enabled"`
}

// autoSchedsState is the schedules section. Gated = no automation to point at, so New
// is disabled (visible + explained) and GateWhy doubles as the empty-state text.
type autoSchedsState struct {
	New     string          `json:"new"`
	Gated   bool            `json:"gated"`
	GateWhy string          `json:"gateWhy"`
	Empty   string          `json:"empty"`
	Cards   []autoSchedCard `json:"cards"`
}

// autoRunRow is one recent-run line.
type autoRunRow struct {
	Name    string `json:"name"` // shortPath(FilePath)
	Trigger string `json:"trigger"`
	Status  string `json:"status"`
	Variant string `json:"variant"`
}

// autoRunsState is the recent-runs section.
type autoRunsState struct {
	Empty string       `json:"empty"`
	Rows  []autoRunRow `json:"rows"`
}

// autoBodyState is the #auto-body inner state (version-gated ~1 Hz tick patch target).
type autoBodyState struct {
	ListTitle  string          `json:"listTitle"`
	SchedTitle string          `json:"schedTitle"`
	RunsTitle  string          `json:"runsTitle"`
	Labels     autoLabels      `json:"labels"`
	List       autoListState   `json:"list"`
	Scheds     autoSchedsState `json:"scheds"`
	Runs       autoRunsState   `json:"runs"`
}

// autoState is the resolved render state for the Automations view (JSON → Zig).
type autoState struct {
	Title       string        `json:"title"`
	Sub         string        `json:"sub"`
	Available   bool          `json:"available"`
	Unavailable string        `json:"unavailable"`
	Body        autoBodyState `json:"body"`
}

// emptyAutoBody zeroes the body with NON-NIL slices: nil marshals to JSON null, which
// fails the Zig slice parse (and would silently drop the tab to the Go fallback).
func emptyAutoBody() autoBodyState {
	return autoBodyState{
		List:   autoListState{Cards: []autoCard{}},
		Scheds: autoSchedsState{Cards: []autoSchedCard{}},
		Runs:   autoRunsState{Rows: []autoRunRow{}},
	}
}

// autoLabelsOf resolves the per-card control labels.
func autoLabelsOf() autoLabels {
	en := i18n.T("common.enabledCap")
	return autoLabels{
		Enabled: en, EnabledDL: strings.ToLower(en),
		Run:    i18n.T("automations.run.btn"),
		SchAdd: i18n.T("automations.sch.add"),
		Edit:   i18n.T("common.edit"),
		Delete: i18n.T("common.delete"),
	}
}

// automationsState resolves availability + i18n + the whole body into render state.
func (u *UI) automationsState() autoState {
	st := autoState{
		Title:       i18n.T("tab.automations"),
		Sub:         i18n.T("automations.subtitle"),
		Available:   u.svc.Automations != nil,
		Unavailable: i18n.T("automations.unavailable"),
		Body:        emptyAutoBody(),
	}
	if st.Available {
		st.Body = u.autoBodyState()
	}
	return st
}

// autoBodyState resolves the three sections. List() runs once: the schedule cards name
// their target from it.
func (u *UI) autoBodyState() autoBodyState {
	autos := u.svc.Automations.List()
	return autoBodyState{
		ListTitle:  i18n.T("tab.automations"),
		SchedTitle: i18n.T("automations.schedules"),
		RunsTitle:  i18n.T("automations.recentRuns"),
		Labels:     autoLabelsOf(),
		List:       autoListStateOf(autos),
		Scheds:     autoSchedsStateOf(u.svc.Automations.ListSchedules(), autos),
		Runs:       autoRunsStateOf(u.svc.Automations.Runs(20)),
	}
}

func autoListStateOf(autos []automation.Automation) autoListState {
	st := autoListState{
		New:   i18n.T("automations.new"),
		Empty: i18n.T("automations.emptyList"),
		Cards: make([]autoCard, 0, len(autos)),
	}
	for _, a := range autos {
		v := ""
		if a.LastStatus != "" {
			v = "secondary"
			switch a.LastStatus {
			case "success":
				v = "success"
			case "error":
				v = "error"
			case "partial":
				v = "warning"
			}
		}
		st.Cards = append(st.Cards, autoCard{
			ID: a.ID, Label: autoLabelOf(a.Label), WatchDir: a.WatchDir,
			Status: a.LastStatus, StatusVar: v,
			Chain: autoChainSummary(a.Actions), Enabled: a.Enabled,
		})
	}
	return st
}

// autoSchedsStateOf resolves the schedule cards. autos supplies each schedule's target
// name (a Schedule stores only the automation's id).
//
// Creating a schedule needs an automation to point at; RENDERING one does not. Gating the whole
// section on len(autos)>0 hid every existing schedule - and its delete/toggle controls - while the
// scheduler kept firing them: a nightly delete-purge with no UI to see or stop it. The cards
// render regardless; only the New button is gated.
func autoSchedsStateOf(scheds []automation.Schedule, autos []automation.Automation) autoSchedsState {
	st := autoSchedsState{
		New:     i18n.T("automations.sch.new"),
		Gated:   len(autos) == 0,
		GateWhy: i18n.T("automations.sch.needAutomation"),
		Empty:   i18n.T("automations.noSchedules"),
		Cards:   make([]autoSchedCard, 0, len(scheds)),
	}
	byID := make(map[string]automation.Automation, len(autos))
	for _, a := range autos {
		byID[a.ID] = a // scalars + read-only slice reads; never mutated (elements alias the service cache)
	}
	for _, s := range scheds {
		a, ok := byID[s.AutomationID]
		c := autoSchedCard{
			ID: s.ID, Label: autoLabelOf(s.Label), Target: autoLabelOf(a.Label),
			StateText: i18n.T("common.off"), StateVar: "secondary",
			Trigger: autoTriggerSummary(s), Gates: autoGateSummary(s),
			LastFired: autoLastFired(s), Enabled: s.Enabled,
		}
		switch {
		case !ok:
			// Its automation is gone, so onSchedule skips every fire. Service.Delete cascades now,
			// so this is data from before the cascade (or a store that refused one) - it must be
			// visible AND deletable, never hidden behind an empty state.
			c.Target = i18n.T("automations.sch.missingAutomation")
			c.WarnTone, c.WarnText = "bad", i18n.T("automations.sch.orphanWarn")
		case s.Enabled && !a.Enabled:
			// Armed, but onSchedule skips the fire while the automation itself is off. The card would
			// otherwise show an enabled schedule against a trigger summary that never happens.
			c.WarnTone, c.WarnText = "warn", i18n.T("automations.sch.automationOffWarn")
		}
		if s.Enabled {
			c.StateText, c.StateVar = string(s.Kind), "info"
		}
		st.Cards = append(st.Cards, c)
	}
	return st
}

func autoRunsStateOf(runs []automation.Run) autoRunsState {
	st := autoRunsState{Empty: i18n.T("automations.noRuns"), Rows: make([]autoRunRow, 0, len(runs))}
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
		st.Rows = append(st.Rows, autoRunRow{
			Name: shortPath(r.FilePath), Trigger: r.Trigger, Status: r.Status, Variant: v,
		})
	}
	return st
}

// renderAutomations: file-arrival + scheduled action chains (list/create/edit/enable/delete,
// schedules, run-now, recent runs). The create/edit form lives in automations_editor.go, the
// schedule form in automations_schedules.go, and run-now in automations_runnow.go.
func (u *UI) renderAutomations() string {
	st := u.automationsState()
	if zigui.Available() {
		if h, ok := zigWire("RenderAutomationsV2", wireAutoState(st), zigui.RenderAutomationsV2,
			zigui.RenderAutomations, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return automationsHTML(st)
}

// autoBody is the #auto-body inner fragment (version-gated ~1 Hz tick patch target).
func (u *UI) autoBody() string {
	st := u.autoBodyState()
	if zigui.Available() {
		if h, ok := zigWire("RenderAutomationsBodyV2", wireAutoBodyState(st), zigui.RenderAutomationsBodyV2,
			zigui.RenderAutomationsBody, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return autoBodyHTML(st)
}

// autoSchedulesHTML renders the schedules section for the given data (ctl/behaviour tests).
func (u *UI) autoSchedulesHTML(scheds []automation.Schedule, autos []automation.Automation) string {
	return autoSchedsHTML(autoSchedsStateOf(scheds, autos), autoLabelsOf())
}

// automationsHTML is the pure Go renderer (golden reference; byte-identical to Zig).
func automationsHTML(st autoState) string {
	if !st.Available {
		return panel(st.Title, "") + emptyState(st.Unavailable)
	}
	return panel(st.Title, st.Sub) + `<div id=auto-body>` + autoBodyHTML(st.Body) + `</div>`
}

// autoBodyHTML is the pure #auto-body inner renderer.
func autoBodyHTML(st autoBodyState) string {
	return section(st.ListTitle, autoListHTML(st.List, st.Labels)) +
		section(st.SchedTitle, autoSchedsHTML(st.Scheds, st.Labels)) +
		section(st.RunsTitle, autoRunsHTML(st.Runs))
}

func autoListHTML(st autoListState, lb autoLabels) string {
	newBtn := btnRow(btn(st.New, "primary", "auto-new", ""))
	if len(st.Cards) == 0 {
		return newBtn + emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(newBtn)
	b.WriteString(`<div class=grid>`)
	for _, a := range st.Cards {
		status := ""
		if a.Status != "" {
			status = badge(a.Status, a.StatusVar)
		}
		b.WriteString(`<div class="rp-card"><div class=card-label>` + html.EscapeString(a.Label) + `</div>` +
			`<div class=np-artist>` + html.EscapeString(a.WatchDir) + `</div>` +
			`<div class=np-meta>` + status + `</div>` +
			`<div class=np-meta>` + html.EscapeString(a.Chain) + `</div>` +
			toggleRowDL(lb.Enabled, lb.EnabledDL, "auto-toggle:"+a.ID, a.Enabled) +
			btnRow(btn(lb.Run, "go", "auto-run:"+a.ID, ""),
				btn(lb.SchAdd, "outline", "auto-sch-add:"+a.ID, ""),
				btn(lb.Edit, "outline", "auto-edit:"+a.ID, ""),
				btn(lb.Delete, "destructive", "auto-del:"+a.ID, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func autoSchedsHTML(st autoSchedsState, lb autoLabels) string {
	newBtn := btnRow(btn(st.New, "primary", "auto-sch-new", ""))
	if st.Gated {
		newBtn = btnRow(btnGated(st.New, st.GateWhy))
	}
	if len(st.Cards) == 0 {
		if st.Gated {
			return newBtn + emptyState(st.GateWhy)
		}
		return newBtn + emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(newBtn)
	b.WriteString(`<div class=grid>`)
	for _, s := range st.Cards {
		warn := ""
		if s.WarnTone != "" {
			warn = hint(s.WarnTone, s.WarnText)
		}
		b.WriteString(`<div class="rp-card"><div class=card-label>` + html.EscapeString(s.Label) + `</div>` +
			`<div class=np-artist>` + html.EscapeString(s.Target) + `</div>` +
			`<div class=np-meta>` + badge(s.StateText, s.StateVar) + ` ` + html.EscapeString(s.Trigger) + `</div>` +
			`<div class=np-meta>` + html.EscapeString(s.Gates) + `</div>` +
			`<div class=np-meta>` + html.EscapeString(s.LastFired) + `</div>` + warn +
			toggleRowDL(lb.Enabled, lb.EnabledDL, "auto-sch-tgl:"+s.ID, s.Enabled) +
			btnRow(btn(lb.Edit, "outline", "auto-sch-edit:"+s.ID, ""),
				btn(lb.Delete, "destructive", "auto-sch-del:"+s.ID, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func autoRunsHTML(st autoRunsState) string {
	if len(st.Rows) == 0 {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	for _, r := range st.Rows {
		b.WriteString(`<div class=kv><span class=kv-k>` + html.EscapeString(r.Name) + ` <span class=np-artist>` + html.EscapeString(r.Trigger) + `</span></span>` +
			`<span class=kv-v>` + badge(r.Status, r.Variant) + `</span></div>`)
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
