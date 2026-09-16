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
	State     string `json:"state"` // live coordinator state (running/queued/deferred); "" = idle
	StateVar  string `json:"stateVar"`
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
	Coalesced string `json:"coalesced"` // "coalesced at HH:MM" when a pending sweep is folded; "" otherwise
	WarnTone  string `json:"warnTone"`  // "" = no warning
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

// autoCoordRow is one live run in the coordinator status region (running or queued).
type autoCoordRow struct {
	Dot      string `json:"dot"` // status-dot variant
	Label    string `json:"label"`
	Line     string `json:"line"` // trigger·file, or the wait reason
	Badge    string `json:"badge"`
	BadgeVar string `json:"badgeVar"`
}

// autoCoordState is the live status region (running · queued). Empty rows ⇒ nothing running, so the
// section is omitted entirely (nothing to say when idle).
type autoCoordState struct {
	Title string         `json:"title"`
	Rows  []autoCoordRow `json:"rows"`
}

// autoBodyState is the #auto-body inner state (version-gated ~1 Hz tick patch target).
type autoBodyState struct {
	ListTitle  string          `json:"listTitle"`
	SchedTitle string          `json:"schedTitle"`
	RunsTitle  string          `json:"runsTitle"`
	Labels     autoLabels      `json:"labels"`
	Coord      autoCoordState  `json:"coord"`
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
		Coord:  autoCoordState{Rows: []autoCoordRow{}},
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
	cs := u.svc.Automations.CoordStatus()
	live := autoLiveIndex(cs)
	return autoBodyState{
		ListTitle:  i18n.T("tab.automations"),
		SchedTitle: i18n.T("automations.schedules"),
		RunsTitle:  i18n.T("automations.recentRuns"),
		Labels:     autoLabelsOf(),
		Coord:      autoCoordStateOf(cs),
		List:       autoListStateOf(autos, live),
		Scheds:     autoSchedsStateOf(u.svc.Automations.ListSchedules(), autos, live),
		Runs:       autoRunsStateOf(u.svc.Automations.Runs(20)),
	}
}

// autoLive is an automation's live coordinator state, indexed by automation id.
type autoLive struct {
	State       string // running/queued/deferred (localized); "" = idle
	StateVar    string
	CoalescedAt string // "HH:MM" when a pending sweep of this automation is coalesced
}

// autoLiveIndex maps automation id → its live state from a coordinator snapshot. Running wins over
// queued (an automation can appear queued for a coalesced follow-up while running).
func autoLiveIndex(cs automation.CoordStatus) map[string]autoLive {
	m := make(map[string]autoLive, len(cs.Running)+len(cs.Queued))
	for _, q := range cs.Queued {
		l := autoLive{State: i18n.T("automations.coord.queued"), StateVar: "secondary"}
		if q.Reason == automation.ReasonStreaming {
			l.State = i18n.T("automations.coord.deferred")
		}
		if q.Coalesced {
			l.CoalescedAt = q.CoalescedAt
		}
		m[q.AutomationID] = l
	}
	for _, r := range cs.Running {
		m[r.AutomationID] = autoLive{State: i18n.T("automations.coord.running"), StateVar: "info", CoalescedAt: m[r.AutomationID].CoalescedAt}
	}
	return m
}

func autoListStateOf(autos []automation.Automation, live map[string]autoLive) autoListState {
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
		lv := live[a.ID]
		st.Cards = append(st.Cards, autoCard{
			ID: a.ID, Label: autoLabelOf(a.Label), WatchDir: a.WatchDir,
			Status: a.LastStatus, StatusVar: v,
			State: lv.State, StateVar: lv.StateVar,
			Chain: autoChainSummary(a.Actions), Enabled: a.Enabled,
		})
	}
	return st
}

// autoCoordStateOf builds the live status region (running first, then queued with reasons).
func autoCoordStateOf(cs automation.CoordStatus) autoCoordState {
	st := autoCoordState{Title: i18n.T("automations.coord.title"), Rows: make([]autoCoordRow, 0, len(cs.Running)+len(cs.Queued))}
	for _, r := range cs.Running {
		st.Rows = append(st.Rows, autoCoordRow{
			Dot: "info", Label: autoLabelOf(r.Label), Line: autoCoordLine(r.Trigger, r.File),
			Badge: i18n.T("automations.coord.running"), BadgeVar: "info",
		})
	}
	for _, q := range cs.Queued {
		badge, bvar := i18n.T("automations.coord.queued"), "secondary"
		if q.Reason == automation.ReasonStreaming {
			badge = i18n.T("automations.coord.deferred")
		}
		if q.Coalesced {
			badge = i18n.T("automations.coord.coalesced")
		}
		st.Rows = append(st.Rows, autoCoordRow{
			Dot: "secondary", Label: autoLabelOf(q.Label), Line: autoCoordReason(q), Badge: badge, BadgeVar: bvar,
		})
	}
	// Bound the on-screen region: the coordinator queue is already capped, but a mis-set
	// transcode-into-its-own-watch-dir feedback loop can still fill it with many rows. Show a cap +
	// a "+N" summary so the status region never grows tall on screen.
	if len(st.Rows) > autoCoordRowCap {
		extra := len(st.Rows) - (autoCoordRowCap - 1)
		st.Rows = st.Rows[:autoCoordRowCap-1]
		st.Rows = append(st.Rows, autoCoordRow{
			Dot: "secondary", Label: i18n.T("automations.coord.more"), Badge: "+" + strconv.Itoa(extra), BadgeVar: "secondary",
		})
	}
	return st
}

// autoCoordRowCap bounds how many live rows the status region shows (rest fold into a "+N" summary).
const autoCoordRowCap = 10

// autoCoordLine renders a running row's sub-line: "trigger" for a sweep, "trigger · file" for one file.
func autoCoordLine(trigger, file string) string {
	if file == "" {
		return i18n.T("automations.coord.sweep", i18n.A{"trigger": trigger})
	}
	return i18n.T("automations.coord.onFile", i18n.A{"trigger": trigger, "file": file})
}

// autoCoordReason renders a queued row's wait reason.
func autoCoordReason(q automation.CoordQueued) string {
	switch q.Reason {
	case automation.ReasonPath:
		return i18n.T("automations.coord.waitPath", i18n.A{"label": autoLabelOf(q.BlockLabel)})
	case automation.ReasonHeavy:
		return i18n.T("automations.coord.waitHeavy")
	case automation.ReasonStreaming:
		return i18n.T("automations.coord.waitStreaming")
	default:
		return i18n.T("automations.coord.waitCurrent")
	}
}

// autoSchedsStateOf resolves the schedule cards. autos supplies each schedule's target
// name (a Schedule stores only the automation's id).
//
// Creating a schedule needs an automation to point at; RENDERING one does not. Gating the whole
// section on len(autos)>0 hid every existing schedule - and its delete/toggle controls - while the
// scheduler kept firing them: a nightly delete-purge with no UI to see or stop it. The cards
// render regardless; only the New button is gated.
func autoSchedsStateOf(scheds []automation.Schedule, autos []automation.Automation, live map[string]autoLive) autoSchedsState {
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
		if at := live[s.AutomationID].CoalescedAt; at != "" {
			c.Coalesced = i18n.T("automations.sch.coalesced", i18n.A{"at": at})
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
	return autoSchedsHTML(autoSchedsStateOf(scheds, autos, nil), autoLabelsOf())
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
	return autoCoordHTML(st.Coord) +
		section(st.ListTitle, autoListHTML(st.List, st.Labels)) +
		section(st.SchedTitle, autoSchedsHTML(st.Scheds, st.Labels)) +
		section(st.RunsTitle, autoRunsHTML(st.Runs))
}

// autoCoordHTML renders the live status region (running · queued with reasons). Renders nothing
// when idle - there is nothing to say, and a permanent empty box is noise.
func autoCoordHTML(st autoCoordState) string {
	if len(st.Rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	for _, r := range st.Rows {
		b.WriteString(`<div class=kv><span class=kv-k>` + dot(r.Dot) + ` ` + html.EscapeString(r.Label) +
			` <span class=np-artist>` + html.EscapeString(r.Line) + `</span></span>` +
			`<span class=kv-v>` + badge(r.Badge, r.BadgeVar) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	return section(st.Title, b.String())
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
		if a.State != "" {
			status += badge(a.State, a.StateVar) // live coordinator state leads the last-run status
		}
		if a.Status != "" {
			status += badge(a.Status, a.StatusVar)
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
		coalesced := ""
		if s.Coalesced != "" {
			coalesced = ` ` + badge(s.Coalesced, "secondary")
		}
		b.WriteString(`<div class="rp-card"><div class=card-label>` + html.EscapeString(s.Label) + `</div>` +
			`<div class=np-artist>` + html.EscapeString(s.Target) + `</div>` +
			`<div class=np-meta>` + badge(s.StateText, s.StateVar) + ` ` + html.EscapeString(s.Trigger) + `</div>` +
			`<div class=np-meta>` + html.EscapeString(s.Gates) + `</div>` +
			`<div class=np-meta>` + html.EscapeString(s.LastFired) + coalesced + `</div>` + warn +
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
