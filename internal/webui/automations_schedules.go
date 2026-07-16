package webui

// Automations schedule editor - the webview's authoring path for the recurring triggers that
// re-run an automation over its watch folder (interval/daily/cron/idle) and the gates that can
// hold a fire back. Follows the form idiom automations_editor.go established: a mutex'd Go-held
// working copy, acts that mutate it, the modal re-opened only on shape/validity changes so
// tabbing between free-text inputs doesn't re-render under the cursor.
//
// Off the actWorker: the store reads (List/ListSchedules) and the writes (SaveSchedule and
// DeleteSchedule both fsync bbolt) run in u.bg. Every mutation goes through the Manager, which
// invalidates its caches, re-arms the scheduler, and bumps Version() - the gate the ~1Hz
// automations tick (live_ticks.go) reads - so the list repaints with no hand-rolled refresh.

import (
	"fmt"
	"html"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/i18n"
)

// A blank interval/idle field materializes these on save rather than persisting 0: the scheduler
// clamps a 0 interval to ONE minute (armIntervalLocked) and skips an idle schedule with
// IdleMinutes<=0 outright (evalTick), so 0 means neither "engine default" nor "off" - it is a
// trap either way, and the placeholder would be lying about it. Same values the Fyne surface used.
const (
	asDefaultInterval = 60
	asDefaultIdle     = 10
)

// asOwner is this modal's slot-owner key (ui.go modalTok): the store reads behind New/Edit and
// SaveSchedule's fsync all land off the actWorker, and none may patch a modal the user replaced.
const asOwner = "auto-sch"

// asKinds is the trigger palette, in the order the picker offers them.
var asKinds = []automation.ScheduleKind{
	automation.ScheduleInterval, automation.ScheduleDaily, automation.ScheduleCron, automation.ScheduleIdle,
}

// asAuto is one automation as the schedule form needs it: a picker row, whether its chain ends by
// erasing the file (a recurring sweep over a delete chain has to say so), and its own enabled flag
// (off = onSchedule skips the fire, so the schedule is armed but inert - automationOffWarn).
// Snapshotted when the form opens: a smartSelect options closure runs during render and must never
// read bbolt.
type asAuto struct {
	id, label string
	deletes   bool
	enabled   bool
}

// asSt is the schedule form's working copy. lastFired is carried through unshown: it is the
// scheduler's own bookkeeping, and an edit must not silently reset it.
type asSt struct {
	mu        sync.Mutex
	id        string // "" = create
	label     string
	autoID    string
	enabled   bool
	kind      automation.ScheduleKind
	interval  int
	atHour    int
	atMinute  int
	cronTx    string
	idle      int
	reqIdle   int
	reqApps   string // free text ("Traktor, obs64") - normalized on save
	exclApps  string
	lastFired string
	autos     []asAuto
	errTx     string // last validation/save failure, shown in the form
}

func init() {
	onExact("auto-sch-new", func(u *UI, _ actMsg) { u.asNew("") })
	onPrefix("auto-sch-add:", func(u *UI, m actMsg) { u.asNew(m.arg("auto-sch-add:")) })
	onPrefix("auto-sch-edit:", func(u *UI, m actMsg) { u.asEdit(m.arg("auto-sch-edit:")) })
	onPrefix("auto-sch-tgl:", func(u *UI, m actMsg) { u.asToggle(m.arg("auto-sch-tgl:"), m.Val == "true") })
	onPrefix("auto-sch-del:", func(u *UI, m actMsg) { u.asDelete(m.arg("auto-sch-del:")) })
	onPrefix("auto-sch:", func(u *UI, m actMsg) { u.asField(u.actTok(m), m.arg("auto-sch:"), m.Val) })
	onExact("auto-sch-save", func(u *UI, m actMsg) { u.asSave(u.actTok(m)) })
}

// ── open ──

// asNew opens a create form, targeting autoID (or the first automation when opened from the
// Schedules section, which has no automation in hand). prev pins the slot as it was at click time;
// the load runs inside claimModalWith, so an open that lands after the user moved on neither
// renders nor overwrites the form they moved to.
func (u *UI) asNew(autoID string) {
	if u.svc.Automations == nil {
		return
	}
	prev := u.modalCur()
	u.bg(func() {
		autos := u.asAutos()
		if u.stopped() {
			return
		}
		id := autoID
		if id == "" && len(autos) > 0 {
			id = autos[0].id
		}
		u.claimModalWith(prev, asOwner, func() string {
			s := &u.as
			s.mu.Lock()
			defer s.mu.Unlock()
			s.load(automation.Schedule{
				Enabled: true, Kind: automation.ScheduleInterval, AutomationID: id,
				IntervalMinutes: asDefaultInterval,
			}, autos)
			return u.asModalHTMLLocked(s)
		})
	})
}

// asEdit loads id into the form.
func (u *UI) asEdit(id string) {
	if u.svc.Automations == nil {
		return
	}
	prev := u.modalCur()
	u.bg(func() {
		autos := u.asAutos()
		sc, ok := u.asGet(id)
		if !ok || u.stopped() {
			return
		}
		u.claimModalWith(prev, asOwner, func() string {
			s := &u.as
			s.mu.Lock()
			defer s.mu.Unlock()
			s.load(sc, autos)
			return u.asModalHTMLLocked(s)
		})
	})
}

// load resets the form to sc. Field-wise, never `*s = asSt{…}`: that would copy the sync.Mutex
// we're holding. Caller holds s.mu.
func (s *asSt) load(sc automation.Schedule, autos []asAuto) {
	s.id, s.label, s.autoID, s.enabled = sc.ID, sc.Label, sc.AutomationID, sc.Enabled
	s.kind = sc.Kind
	if s.kind == "" {
		s.kind = automation.ScheduleInterval // pre-kind rows + the engine's own default
	}
	s.interval, s.idle = sc.IntervalMinutes, sc.IdleMinutes
	s.atHour, s.atMinute = asClamp(sc.AtHour, 0, 23), asClamp(sc.AtMinute, 0, 59)
	s.cronTx = sc.CronExpr
	s.reqIdle = sc.RequireIdleMinutes
	// Join (never retain): a ListSchedules element's nested slices still alias the service cache.
	s.reqApps = strings.Join(sc.RequireAppsRunning, ", ")
	s.exclApps = strings.Join(sc.ExcludeAppsRunning, ", ")
	s.lastFired = sc.LastFiredAt
	s.autos = autos
	s.errTx = ""
}

// asGet finds a schedule by id. The Manager has no GetSchedule, so this scans ListSchedules();
// its elements' nested slices (Require/ExcludeAppsRunning) alias the service's cache, so the form
// only ever READS them (load's strings.Join) - asBuild emits fresh slices via asSplitCSV.
func (u *UI) asGet(id string) (automation.Schedule, bool) {
	for _, sc := range u.svc.Automations.ListSchedules() {
		if sc.ID == id {
			return sc, true
		}
	}
	return automation.Schedule{}, false
}

// asAutos snapshots the automation list for the picker (label + the delete verdict).
func (u *UI) asAutos() []asAuto {
	autos := u.svc.Automations.List()
	out := make([]asAuto, 0, len(autos))
	for _, a := range autos {
		out = append(out, asAuto{
			id: a.ID, label: autoLabelOf(a.Label),
			deletes: autoChainDeletes(a.Actions), enabled: a.Enabled,
		})
	}
	return out
}

// auto returns the currently targeted automation. Caller holds s.mu.
func (s *asSt) auto() (asAuto, bool) {
	for _, a := range s.autos {
		if a.id == s.autoID {
			return a, true
		}
	}
	return asAuto{}, false
}

// ── field mutation ──

// asField applies a form field. Free text stores quietly so tabbing between inputs doesn't
// re-render under the cursor; everything a smartSelect feeds MUST re-open, because ssPick's own
// ssPatch re-renders the control from ssCurs, which only a render refreshes - without the re-open
// the picked value lands in state but the control keeps showing the old one.
func (u *UI) asField(tok modalTok, f, val string) {
	u.updateModalIf(tok, func() string { // refused = this form is gone; the write would hit its successor
		s := &u.as
		s.mu.Lock()
		defer s.mu.Unlock()
		reopen := false
		switch f {
		case "label":
			s.label = val
		case "auto":
			s.autoID = val
			reopen = true // the delete-chain warning is per-automation
		case "enabled":
			s.enabled = val == "true"
		case "kind":
			s.kind = automation.ScheduleKind(val)
			reopen = true // swaps the whole trigger block
		case "interval":
			s.interval = atoi(val)
		case "hour":
			s.atHour = asClamp(atoi(val), 0, 23)
			reopen = true // smartSelect-fed: see above
		case "minute":
			s.atMinute = asClamp(atoi(val), 0, 59)
			reopen = true // smartSelect-fed: see above
		case "cron":
			s.cronTx = strings.TrimSpace(val)
			reopen = true // the live parser verdict below the field
		case "idle":
			s.idle = atoi(val)
		case "reqidle":
			s.reqIdle = atoi(val)
		case "reqapps":
			s.reqApps = val
		case "exclapps":
			s.exclApps = val
		}
		if !reopen {
			return ""
		}
		return u.asModalHTMLLocked(s)
	})
}

// ── build + save ──

// asBuild converts the form to a Schedule, applying the checks the store can't make. Cron is
// validated with the engine's own parser (automation.ValidateCron) rather than a second copy of
// the grammar - the same call the live verdict under the field makes.
func (u *UI) asBuild() (automation.Schedule, error) {
	s := &u.as
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.label) == "" {
		return automation.Schedule{}, fmt.Errorf("%s", i18n.T("automations.sch.errLabel"))
	}
	if s.autoID == "" {
		return automation.Schedule{}, fmt.Errorf("%s", i18n.T("automations.sch.errAutomation"))
	}
	out := automation.Schedule{
		ID: s.id, Label: strings.TrimSpace(s.label), AutomationID: s.autoID,
		Enabled: s.enabled, Kind: s.kind,
		RequireIdleMinutes: s.reqIdle,
		RequireAppsRunning: asSplitCSV(s.reqApps),
		ExcludeAppsRunning: asSplitCSV(s.exclApps),
		LastFiredAt:        s.lastFired, // scheduler bookkeeping - an edit must not reset it
	}
	switch s.kind {
	case automation.ScheduleDaily:
		out.AtHour, out.AtMinute = s.atHour, s.atMinute
	case automation.ScheduleCron:
		out.CronExpr = strings.TrimSpace(s.cronTx)
		if err := automation.ValidateCron(out.CronExpr); err != nil {
			return automation.Schedule{}, fmt.Errorf("%s: %s", i18n.T("automations.sch.errCron"), err)
		}
	case automation.ScheduleIdle:
		out.IdleMinutes = asPos(s.idle, asDefaultIdle)
	default:
		out.Kind = automation.ScheduleInterval // an unknown kind arms nothing; fall back to the default
		out.IntervalMinutes = asPos(s.interval, asDefaultInterval)
	}
	return out, nil
}

// asSave validates, then persists off the actWorker (SaveSchedule fsyncs bbolt + re-arms). tok is
// the form session that clicked Save and the only one the completion may touch - the user can close
// this form, or open another schedule, before the fsync lands.
func (u *UI) asSave(tok modalTok) {
	if u.svc.Automations == nil {
		return
	}
	sc, err := u.asBuild()
	if err != nil {
		u.asSetErr(tok, err.Error())
		return
	}
	if !u.actStart("auto-sch-save") {
		return
	}
	u.pendingAct("auto-sch-save")
	u.bg(func() {
		defer u.actEnd("auto-sch-save")
		// Same trap as aeSave: an id means "update", but SaveSchedule is a blind Put, so a schedule
		// deleted under the open form (by its card, or by the cascade behind its automation's
		// delete) would be resurrected here - re-arming a timer the user just stopped.
		if sc.ID != "" {
			if _, ok := u.asGet(sc.ID); !ok {
				u.asVanished(tok)
				return
			}
		}
		saved, err := u.svc.Automations.SaveSchedule(sc)
		if u.stopped() {
			return
		}
		if err != nil {
			u.logErr("schedule save", err)
			u.asFailed(tok, err.Error())
			return
		}
		// A create becomes an edit: a second Save must update, not duplicate. Only into the form
		// that asked - writing this id into a form the user has since loaded another schedule into
		// would retarget its next Save at this one, re-arming the wrong timer.
		u.updateModalIf(tok, func() string {
			u.as.mu.Lock()
			u.as.id = saved.ID
			u.as.mu.Unlock()
			return "" // closeModalIf takes the form down next; no re-render
		})
		u.closeModalIf(tok) // false = the user already closed it, or another form owns the slot
		u.patchMain()
		u.toast(i18n.T("automations.sch.saved"))
	})
}

// asVanished reports the schedule was deleted under the open form; the form reverts to a create so
// the edits on screen survive one more Save - but only while it IS the form on screen.
func (u *UI) asVanished(tok modalTok) {
	if !u.asSetErrIf(tok, i18n.T("automations.sch.errVanished"), true) {
		u.toast(i18n.T("automations.sch.errVanished"))
	}
}

// asFailed surfaces a save failure in the form that raised it, or as a toast when that form is
// gone: a completion must never re-open a modal the user closed, nor write its error into the form
// that replaced it.
func (u *UI) asFailed(tok modalTok, msg string) {
	if !u.asSetErrIf(tok, msg, false) {
		u.toast(msg)
	}
}

// asSetErrIf writes msg into tok's form and re-renders it; toCreate also clears the id (the row is
// gone). false = tok no longer owns the modal, so nothing was written.
func (u *UI) asSetErrIf(tok modalTok, msg string, toCreate bool) bool {
	return u.updateModalIf(tok, func() string {
		s := &u.as
		s.mu.Lock()
		defer s.mu.Unlock()
		if toCreate {
			s.id = ""
		}
		s.errTx = msg
		return u.asModalHTMLLocked(s)
	})
}

// asToggle flips a schedule's enabled flag from its card. Routes through SaveSchedule so the
// scheduler re-arms and Version() bumps (mirrors autoToggle).
func (u *UI) asToggle(id string, on bool) {
	if u.svc.Automations == nil || !u.actStart("auto-sch:"+id) {
		return
	}
	u.pendingAct("auto-sch-tgl:" + id)
	u.bg(func() {
		defer u.actEnd("auto-sch:" + id)
		sc, ok := u.asGet(id)
		if !ok {
			return
		}
		sc.Enabled = on // scalar only: sc's nested slices alias the service cache
		_, err := u.svc.Automations.SaveSchedule(sc)
		u.logErr("schedule save", err)
		if !u.stopped() {
			u.patchMain()
		}
	})
}

// asDelete removes a schedule (the automation itself is untouched).
func (u *UI) asDelete(id string) {
	if u.svc.Automations == nil || !u.actStart("auto-sch:"+id) {
		return
	}
	u.pendingAct("auto-sch-del:" + id)
	u.bg(func() {
		defer u.actEnd("auto-sch:" + id)
		u.logErr("schedule delete", u.svc.Automations.DeleteSchedule(id))
		if u.stopped() {
			return
		}
		u.patchMain()
		u.toast(i18n.T("automations.sch.deleted"))
	})
}

// asSetErr shows a validation failure in tok's form. Called on the act lane, pre-save, where the
// form is by construction the modal on screen - a Save act arriving without it must not open it.
func (u *UI) asSetErr(tok modalTok, msg string) { u.asSetErrIf(tok, msg, false) }

// ── render (pure: reads the working copy + the automation snapshot, no I/O) ──

func (u *UI) asModalHTML() string {
	s := &u.as
	s.mu.Lock()
	defer s.mu.Unlock()
	return u.asModalHTMLLocked(s)
}

// asModalHTMLLocked renders the form. Caller holds s.mu - the mutators already do, so the write and
// the render it produces stay one atomic step under the slot lock.
func (u *UI) asModalHTMLLocked(s *asSt) string {
	var b strings.Builder
	if s.errTx != "" {
		b.WriteString(`<div class=ae-err>` + hint("bad", s.errTx) + `</div>`)
	}
	b.WriteString(field(i18n.T("automations.sch.label"), "auto-sch:label", s.label, "text"))
	b.WriteString(asAutoSelect(s))
	b.WriteString(toggleRow(i18n.T("common.enabledCap"), "auto-sch:enabled", s.enabled))
	if a, ok := s.auto(); ok {
		if a.deletes {
			// A one-off delete is a decision; a delete on a timer is a standing order. Say it here,
			// where the timer is being set, not in the run history afterwards.
			b.WriteString(hint("bad", i18n.T("automations.sch.deleteChainWarn")))
		}
		if !a.enabled && s.enabled {
			b.WriteString(hint("warn", i18n.T("automations.sch.automationOffWarn")))
		}
	}
	b.WriteString(section(i18n.T("automations.sch.secTrigger"), asTriggerHTML(s)))
	b.WriteString(section(i18n.T("automations.sch.secGates"), asGatesHTML(s)))

	footer := btnRow(btn(i18n.T("automations.sch.save"), "primary", "auto-sch-save", ""),
		btn(i18n.T("common.cancel"), "ghost", "modal-close", ""))
	title := i18n.T("automations.sch.titleNew")
	if s.id != "" {
		title = i18n.T("automations.sch.titleEdit")
	}
	return modal(title, b.String(), footer)
}

// asTriggerHTML renders the kind picker + only the fields that kind actually reads. Caller holds s.mu.
func asTriggerHTML(s *asSt) string {
	var b strings.Builder
	b.WriteString(asKindSelect(s))
	switch s.kind {
	case automation.ScheduleDaily:
		b.WriteString(fpair(asClockSelect("auto-sch-hour", i18n.T("automations.sch.atHour"), "auto-sch:hour", s.atHour, 23),
			asClockSelect("auto-sch-min", i18n.T("automations.sch.atMinute"), "auto-sch:minute", s.atMinute, 59)))
		b.WriteString(`<div class=pb-hint>` + html.EscapeString(i18n.T("automations.sch.dailyHint")) + `</div>`)
	case automation.ScheduleCron:
		b.WriteString(fieldEx(i18n.T("automations.sch.cron"), "auto-sch:cron", s.cronTx, "text",
			"*/15 * * * *", tipTopic("auto-sch-cron")))
		b.WriteString(asCronVerdict(s.cronTx))
	case automation.ScheduleIdle:
		b.WriteString(fieldEx(i18n.T("automations.sch.idleMinutes"), "auto-sch:idle", aeIntTx(s.idle), "number",
			strconv.Itoa(asDefaultIdle), tipTopic("auto-sch-idle")))
		if runtime.GOOS != "windows" {
			// The idle TRIGGER fails closed: evalTick skips the schedule outright when the platform
			// can't report idle time, so this would be a schedule that never fires. (The idle GATE
			// below fails open - opposite behaviour, hence the separate warning.)
			b.WriteString(hint("bad", i18n.T("automations.sch.idleUnsupported")))
		}
	default:
		b.WriteString(fieldEx(i18n.T("automations.sch.intervalMinutes"), "auto-sch:interval", aeIntTx(s.interval), "number",
			strconv.Itoa(asDefaultInterval), tipTopic("auto-sch-interval")))
	}
	return b.String()
}

// asGatesHTML renders the gates that apply to ANY kind. Caller holds s.mu.
func asGatesHTML(s *asSt) string {
	var b strings.Builder
	b.WriteString(fieldEx(i18n.T("automations.sch.requireIdle"), "auto-sch:reqidle", aeIntTx(s.reqIdle), "number", "0",
		tipTopic("auto-sch-require-idle")))
	b.WriteString(fieldEx(i18n.T("automations.sch.requireApps"), "auto-sch:reqapps", s.reqApps, "text",
		i18n.T("automations.sch.requireAppsPH"), tipTopic("auto-sch-apps")))
	b.WriteString(fieldEx(i18n.T("automations.sch.excludeApps"), "auto-sch:exclapps", s.exclApps, "text",
		i18n.T("automations.sch.excludeAppsPH"), tipTopic("auto-sch-apps")))
	if runtime.GOOS != "windows" {
		// gateBlock fails OPEN when the platform can't report idle/processes - the schedule still
		// fires, ungated. Silently ignoring a stated condition would be the worse surprise.
		b.WriteString(hint("warn", i18n.T("automations.sch.gatesUnsupported")))
	}
	return b.String()
}

// asAutoSelect picks the automation this schedule re-runs. The options closure is pure - it
// closes over the snapshot taken when the form opened.
func asAutoSelect(s *asSt) string {
	autos := s.autos
	return smartSelect("auto-sch-auto", i18n.T("automations.sch.automation"), "auto-sch:auto", s.autoID, func() []ssOpt {
		out := make([]ssOpt, 0, len(autos))
		for _, a := range autos {
			o := ssOpt{Val: a.id, Label: a.label}
			if a.deletes {
				o.Badge = i18n.T("automations.act.delete") // erasing chains are worth spotting in the list
			}
			out = append(out, o)
		}
		return out
	})
}

// asKindSelect picks the trigger. Each row carries a one-line description of when it fires;
// smartSelectRaw (not smartSelect) so the full topic tooltip can sit beside the label.
func asKindSelect(s *asSt) string {
	lbl := `<span class=ss-label>` + html.EscapeString(i18n.T("automations.sch.kind")) + tipTopic("auto-sch-kind") + `</span>`
	return smartSelectRaw("auto-sch-kind", lbl, "auto-sch:kind", string(s.kind), func() []ssOpt {
		out := make([]ssOpt, 0, len(asKinds))
		for _, k := range asKinds {
			out = append(out, ssOpt{Val: string(k), Label: asKindLabel(k), Sub: asKindDesc(k)})
		}
		return out
	})
}

// asClockSelect renders one hour/minute picker - two of these stand in for a native time input,
// over the only legal values, so 25:99 is unrepresentable. The options closure is pure formatting.
func asClockSelect(id, label, act string, cur, hi int) string {
	return smartSelect(id, label, act, strconv.Itoa(cur), func() []ssOpt {
		out := make([]ssOpt, 0, hi+1)
		for n := 0; n <= hi; n++ {
			out = append(out, ssOpt{Val: strconv.Itoa(n), Label: fmt.Sprintf("%02d", n)})
		}
		return out
	})
}

// asCronVerdict renders the engine parser's own verdict as the expression is typed - the same
// ValidateCron asBuild refuses on, so a typo can't first surface as a failed save.
func asCronVerdict(expr string) string {
	if strings.TrimSpace(expr) == "" {
		return hint("warn", i18n.T("automations.sch.cronEmpty"))
	}
	if err := automation.ValidateCron(expr); err != nil {
		return hint("bad", err.Error())
	}
	return hint("ok", i18n.T("automations.sch.cronOK"))
}

// ── helpers ──

func asKindLabel(k automation.ScheduleKind) string {
	switch k {
	case automation.ScheduleInterval:
		return i18n.T("automations.sch.kindInterval")
	case automation.ScheduleDaily:
		return i18n.T("automations.sch.kindDaily")
	case automation.ScheduleCron:
		return i18n.T("automations.sch.kindCron")
	case automation.ScheduleIdle:
		return i18n.T("automations.sch.kindIdle")
	}
	return string(k)
}

func asKindDesc(k automation.ScheduleKind) string {
	switch k {
	case automation.ScheduleInterval:
		return i18n.T("automations.sch.kindIntervalDesc")
	case automation.ScheduleDaily:
		return i18n.T("automations.sch.kindDailyDesc")
	case automation.ScheduleCron:
		return i18n.T("automations.sch.kindCronDesc")
	case automation.ScheduleIdle:
		return i18n.T("automations.sch.kindIdleDesc")
	}
	return ""
}

// asSplitCSV splits a comma list into trimmed, non-empty app names. sysactivity matches
// case-insensitively, with or without the .exe, so the raw text is stored as typed.
func asSplitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// asPos returns n when positive, else def (see the asDefault* rationale).
func asPos(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

func asClamp(n, lo, hi int) int {
	return min(max(n, lo), hi)
}
