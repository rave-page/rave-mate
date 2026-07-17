package webui

// Rendering rules that keep armed-but-invisible state impossible, plus the ctl-reachability of the
// fields the automations editor renders.

import (
	"strings"
	"testing"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/i18n"
)

// TestSchedulesRenderWithoutAnyAutomation is the reported bug: the section used to render a
// "create an automation first" empty state whenever the automation list was empty, hiding every
// existing schedule AND its controls - while the scheduler kept firing them.
func TestSchedulesRenderWithoutAnyAutomation(t *testing.T) {
	u := &UI{}
	scheds := []automation.Schedule{{
		ID: "sch-1", Label: "Nightly purge", AutomationID: "auto-gone",
		Enabled: true, Kind: automation.ScheduleDaily,
	}}
	got := u.autoSchedulesHTML(scheds, nil) // nil = every automation deleted

	if !strings.Contains(got, "Nightly purge") {
		t.Fatal("the schedule vanished from the UI while the scheduler still holds it")
	}
	for _, act := range []string{"auto-sch-del:sch-1", "auto-sch-tgl:sch-1", "auto-sch-edit:sch-1"} {
		if !strings.Contains(got, act) {
			t.Fatalf("control %q is unreachable - the user cannot see or stop this schedule", act)
		}
	}
	if !strings.Contains(got, hint("bad", i18n.T("automations.sch.orphanWarn"))) {
		t.Fatal("an orphaned schedule renders with no explanation of why it does nothing")
	}
	// New needs a target, so it is GATED (visible + explained), never silently absent.
	if !strings.Contains(got, "disabled") || !strings.Contains(got, i18n.T("automations.sch.needAutomation")) {
		t.Fatal("the New button should be gated with the reason, not hidden")
	}
}

// TestOrphanWarningOnlyForOrphans: a live schedule must not wear the orphan banner, and the
// automation-off warning is a different (recoverable) state that must still show.
func TestOrphanWarningOnlyForOrphans(t *testing.T) {
	u := &UI{}
	autos := []automation.Automation{{ID: "auto-1", Label: "Sets", Enabled: false}}
	scheds := []automation.Schedule{{ID: "sch-1", Label: "Live one", AutomationID: "auto-1",
		Enabled: true, Kind: automation.ScheduleInterval, IntervalMinutes: 60}}
	got := u.autoSchedulesHTML(scheds, autos)

	if strings.Contains(got, hint("bad", i18n.T("automations.sch.orphanWarn"))) {
		t.Fatal("a schedule whose automation exists was branded an orphan")
	}
	if !strings.Contains(got, hint("warn", i18n.T("automations.sch.automationOffWarn"))) {
		t.Fatal("the automation-off warning stopped rendering")
	}
	if !strings.Contains(got, i18n.T("automations.sch.new")) {
		t.Fatal("New should be live when there is an automation to point at")
	}
}

// TestEmptyStatesStillDistinct: no schedules at all is not an orphan situation - each empty state
// must keep saying the thing that is actually true.
func TestEmptyStatesStillDistinct(t *testing.T) {
	u := &UI{}
	autos := []automation.Automation{{ID: "auto-1", Label: "Sets", Enabled: true}}
	if got := u.autoSchedulesHTML(nil, autos); !strings.Contains(got, i18n.T("automations.noSchedules")) {
		t.Fatal("with an automation but no schedules, the empty state should invite creating one")
	}
	if got := u.autoSchedulesHTML(nil, nil); !strings.Contains(got, i18n.T("automations.sch.needAutomation")) {
		t.Fatal("with nothing at all, the empty state should point at creating an automation")
	}
}

// TestPbFieldExIsCtlReachable: ctl read/set resolve a control by its data-label host. Without one
// the shared loudness block's LUFS + true-peak inputs are undrivable on EVERY surface that renders
// it - and ctl is this app's mandated verification path.
func TestPbFieldExIsCtlReachable(t *testing.T) {
	got := pbFieldEx("LUFS target", "auto-ed-af:0:loudi", "", "number", "-14", "hint text")
	if !strings.Contains(got, `data-label="lufs target"`) {
		t.Fatalf("pb-field has no data-label, so ctl cannot reach it:\n%s", got)
	}
	// The label host must wrap the input, or the lookup finds a host with nothing to drive.
	if strings.Index(got, "data-label") > strings.Index(got, "<input") {
		t.Fatalf("data-label is not on the wrapper around the input:\n%s", got)
	}
}

// TestAeLoudnessFieldsAreCtlReachable pins the reported surface end-to-end: the automations
// editor's loudness override block, rendered through the shared primitive.
func TestAeLoudnessFieldsAreCtlReachable(t *testing.T) {
	u := &UI{}
	tok := aeOpenForm(u)
	u.aeAdd(tok, automation.ActionTranscode)
	k := aeK(aeKeyAt(u, 0))
	u.aeActField(tok, k+":preset", "audioAac") // a preset that re-encodes, so the targets render
	u.aeActField(tok, k+":loudon", "true")

	html := u.aeModalHTML()
	for _, want := range []string{
		`data-label="` + strings.ToLower(i18n.T("library.enc.lufsTarget")) + `"`,
		`data-label="` + strings.ToLower(i18n.T("library.enc.truePeak")) + `"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("loudness field %s is unreachable from ctl read/set", want)
		}
	}
}
