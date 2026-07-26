package webui

// aeSave / asSave against a REAL automation store: the save lands off the actWorker, so the row it
// targets can be deleted underneath it.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/ui"
)

// autoUI wires a UI to a real bbolt-backed automation service (no shell: evals are no-ops).
func autoUI(t *testing.T) (*UI, *automation.Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := automation.NewManager(st, nil, func(id string) (transcode.Preset, bool) {
		return transcode.Find(id)
	}, logbus.New(32))
	return &UI{svc: ui.Services{Automations: svc}, stop: make(chan struct{})}, svc
}

// waitFor polls until cond or the deadline: aeSave/asSave complete in u.bg. The deadline
// covers a real bbolt Put+fsync on a loaded CI runner disk - 3s flaked on windows-latest.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestAeSaveDoesNotResurrectDeletedAutomation is the reported bug: Save treats a non-empty id as an
// update with no existence check, and Save is a blind Put - so an automation deleted under an open
// editor comes back the moment the user hits Save.
func TestAeSaveDoesNotResurrectDeletedAutomation(t *testing.T) {
	u, svc := autoUI(t)
	dir := t.TempDir()
	saved, err := svc.Save(automation.Automation{Label: "Purge", WatchDir: dir, Enabled: true,
		Actions: []automation.Action{{Type: automation.ActionDelete}}})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The user opens it in the editor...
	u.ae.load(saved)
	if u.ae.id != saved.ID {
		t.Fatalf("editor did not load the automation: id=%q", u.ae.id)
	}
	// ...and it is deleted underneath them (another surface, or their own card click).
	if err := svc.Delete(saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	tok := aeOpenForm(u) // Save is clicked IN the form; the completion may only write back to it
	u.aeSave(tok)
	waitFor(t, "save to settle", func() bool { return u.ae.errTx != "" })

	if _, ok := svc.Get(saved.ID); ok {
		t.Fatal("Save resurrected an automation the user deleted on purpose")
	}
	if len(svc.List()) != 0 {
		t.Fatalf("the store should be empty, has %d", len(svc.List()))
	}
	// The form must say so plainly, and turn into a create so the work on screen is recoverable.
	if u.ae.id != "" {
		t.Fatalf("the form still claims to be editing id %q - a second Save would re-create it silently", u.ae.id)
	}
	if u.ae.errTx == "" {
		t.Fatal("the user was told nothing about the automation vanishing")
	}

	// Recovery: saving again creates it fresh, under a new id.
	u.aeSave(tok)
	waitFor(t, "re-create", func() bool { return len(svc.List()) == 1 })
	if got := svc.List()[0]; got.ID == saved.ID {
		t.Fatal("the re-create reused the deleted id instead of minting a new one")
	}
}

// TestAeSaveNormalUpdateStillWorks: the existence check must not break the ordinary edit path.
func TestAeSaveNormalUpdateStillWorks(t *testing.T) {
	u, svc := autoUI(t)
	saved, err := svc.Save(automation.Automation{Label: "Keep", WatchDir: t.TempDir(), Enabled: true,
		Actions: []automation.Action{{Type: automation.ActionCopy, OutputDir: t.TempDir()}}})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	u.ae.load(saved)
	tok := aeOpenForm(u)
	u.aeField(tok, "label", "Renamed")
	u.aeSave(tok)
	waitFor(t, "update", func() bool {
		a, ok := svc.Get(saved.ID)
		return ok && a.Label == "Renamed"
	})
	if len(svc.List()) != 1 {
		t.Fatalf("the update duplicated the automation: %d rows", len(svc.List()))
	}
}

// TestAsSaveDoesNotResurrectDeletedSchedule: same trap on the schedule form - and resurrecting one
// re-arms a timer the user stopped.
func TestAsSaveDoesNotResurrectDeletedSchedule(t *testing.T) {
	u, svc := autoUI(t)
	auto, err := svc.Save(automation.Automation{Label: "Sets", WatchDir: t.TempDir(), Enabled: true,
		Actions: []automation.Action{{Type: automation.ActionCopy, OutputDir: t.TempDir()}}})
	if err != nil {
		t.Fatalf("seed automation: %v", err)
	}
	sc, err := svc.SaveSchedule(automation.Schedule{Label: "Hourly", AutomationID: auto.ID,
		Enabled: true, Kind: automation.ScheduleInterval, IntervalMinutes: 60})
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	u.as.load(sc, []asAuto{{id: auto.ID, label: "Sets", enabled: true}})
	if err := svc.DeleteSchedule(sc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	u.asSave(u.openModalAs(asOwner, u.asModalHTML()))
	waitFor(t, "save to settle", func() bool { return u.as.errTx != "" })

	if len(svc.ListSchedules()) != 0 {
		t.Fatal("Save re-armed a schedule the user deleted")
	}
	if u.as.id != "" {
		t.Fatalf("the form still claims to be editing id %q", u.as.id)
	}
}

// slowSave holds Save open until release is closed - a deterministic stand-in for the bbolt fsync
// aeSave does off the actWorker, so a test can drive the user's next move INTO that window.
type slowSave struct {
	automation.Manager
	entered chan struct{}
	release chan struct{}
}

func (m *slowSave) Save(a automation.Automation) (automation.Automation, error) {
	close(m.entered)
	<-m.release
	return m.Manager.Save(a)
}

// waitSaveDone waits for aeSave's bg completion to finish: its deferred actEnd is the last thing it
// does, so re-acquiring the key means every write it makes has landed.
func waitSaveDone(t *testing.T, u *UI) {
	t.Helper()
	waitFor(t, "the save completion to finish", func() bool { return u.actStart("auto-ed-save") })
	u.actEnd("auto-ed-save")
}

// TestAeSaveIdLandsOnlyInTheFormThatAsked: Save fsyncs bbolt off the actWorker and writes the new
// id back into the form's SHARED state. Cancel that form while the fsync is in flight, open another
// automation, and the completion silently retargets the form on screen at the row it just saved -
// so the user's next Save overwrites a different automation with the edits in front of them. Same
// shape as this session's precedent (tfEditOpen seeded a new track with the old track's tags).
func TestAeSaveIdLandsOnlyInTheFormThatAsked(t *testing.T) {
	u, svc := autoUI(t)
	other, err := svc.Save(automation.Automation{Label: "Other", WatchDir: t.TempDir(), Enabled: true,
		Actions: []automation.Action{{Type: automation.ActionCopy, OutputDir: t.TempDir()}}})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	slow := &slowSave{Manager: svc, entered: make(chan struct{}), release: make(chan struct{})}
	u.svc.Automations = slow

	// The user fills in a NEW automation and hits Save.
	u.ae.mu.Lock()
	u.ae.load(automation.Automation{Label: "Fresh", WatchDir: t.TempDir(), Enabled: true})
	u.ae.acts = aeSteps(&u.ae, automation.Action{Type: automation.ActionCopy, OutputDir: t.TempDir()})
	u.ae.mu.Unlock()
	tok := aeOpenForm(u)
	u.aeSave(tok)
	<-slow.entered // the fsync is in flight

	// ...they cancel it and open a different automation in the editor.
	u.closeModal()
	u.ae.mu.Lock()
	u.ae.load(other)
	u.ae.mu.Unlock()
	aeOpenForm(u)

	close(slow.release)
	waitSaveDone(t, u)

	u.ae.mu.Lock()
	defer u.ae.mu.Unlock()
	if u.ae.id != other.ID {
		t.Fatalf("the create's completion retargeted the open form at %q, want %q - the next Save would "+
			"overwrite %q with the edits on screen", u.ae.id, other.ID, u.ae.id)
	}
}

// TestAeVanishedRefusesAnotherFormsState: the same completion, on its other exit. Reporting "the
// automation you were editing is gone" reverts the form to a create - into whichever form is
// loaded, which by then can be a real automation. Its next Save would then DUPLICATE it, and the
// error text blames it for a vanishing it had nothing to do with.
func TestAeVanishedRefusesAnotherFormsState(t *testing.T) {
	u, svc := autoUI(t)
	other, err := svc.Save(automation.Automation{Label: "Other", WatchDir: t.TempDir(), Enabled: true,
		Actions: []automation.Action{{Type: automation.ActionCopy, OutputDir: t.TempDir()}}})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The user was editing an automation that has since been deleted; its Save is in flight.
	u.ae.mu.Lock()
	u.ae.load(automation.Automation{ID: "gone", Label: "A", WatchDir: t.TempDir()})
	u.ae.mu.Unlock()
	stale := aeOpenForm(u)
	u.closeModal()
	// ...they cancel and open a live automation instead.
	u.ae.mu.Lock()
	u.ae.load(other)
	u.ae.mu.Unlock()
	aeOpenForm(u)

	u.aeVanished(stale) // A's completion lands

	u.ae.mu.Lock()
	defer u.ae.mu.Unlock()
	if u.ae.id != other.ID {
		t.Fatalf("a dead form's completion reverted the live form to a create (id=%q): its next Save "+
			"would duplicate %q", u.ae.id, other.ID)
	}
	if u.ae.errTx != "" {
		t.Fatalf("a dead form's completion parked its error in the live form: %q", u.ae.errTx)
	}
}

// TestAeSaveRejectsInertLoudness pins the wire/editor agreement: the editor refuses exactly what
// the studio wire refuses, so an automation authored in one is editable from the other.
func TestAeSaveRejectsInertLoudness(t *testing.T) {
	u, svc := autoUI(t)
	u.ae.load(automation.Automation{Label: "Remux", WatchDir: t.TempDir(), Enabled: true,
		Actions: []automation.Action{{Type: automation.ActionTranscode, PresetID: "remux",
			LoudnessOn: true, LoudnessI: -14}}})
	if _, err := u.aeBuild(); err == nil {
		t.Fatal("the editor accepted a LUFS target on a copy-audio preset; the studio wire rejects it")
	}
	u.aeSave(aeOpenForm(u))
	if len(svc.List()) != 0 {
		t.Fatal("an inert-loudness chain reached the store")
	}
	// The live verdict must name it before the save click, not only after.
	if !strings.Contains(u.aeModalHTML(), "re-encod") {
		t.Fatal("the chain banner does not explain why the chain cannot be saved")
	}
}
