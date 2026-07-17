package automation

import (
	"testing"

	"rave.page/mate/internal/transcode"
)

// TestDeleteCascadesSchedules: a schedule has no meaning without the automation it re-runs.
// Left behind, it stays armed and the scheduler keeps firing it - onSchedule then skips every fire
// because the target is gone, so it is invisible work forever. Only the target's schedules go.
func TestDeleteCascadesSchedules(t *testing.T) {
	m := NewManager(mustStore(t), nil, noPreset, noopLog{})

	doomed, err := m.Save(Automation{Label: "nightly purge", WatchDir: t.TempDir(), Enabled: true,
		Actions: []Action{{Type: ActionDelete}}})
	if err != nil {
		t.Fatalf("save doomed: %v", err)
	}
	keep, err := m.Save(Automation{Label: "keeper", WatchDir: t.TempDir(), Enabled: true,
		Actions: []Action{{Type: ActionCopy, OutputDir: t.TempDir()}}})
	if err != nil {
		t.Fatalf("save keeper: %v", err)
	}
	for _, sc := range []Schedule{
		{Label: "purge nightly", AutomationID: doomed.ID, Enabled: true, Kind: ScheduleDaily},
		{Label: "purge hourly", AutomationID: doomed.ID, Enabled: true, Kind: ScheduleInterval, IntervalMinutes: 60},
		{Label: "keeper's own", AutomationID: keep.ID, Enabled: true, Kind: ScheduleInterval, IntervalMinutes: 30},
	} {
		if _, err := m.SaveSchedule(sc); err != nil {
			t.Fatalf("save schedule %q: %v", sc.Label, err)
		}
	}

	before := m.Version()
	if err := m.Delete(doomed.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	left := m.ListSchedules()
	for _, s := range left {
		if s.AutomationID == doomed.ID {
			t.Fatalf("schedule %q outlived its automation - the scheduler keeps firing it with no UI to stop it", s.Label)
		}
	}
	if len(left) != 1 || left[0].AutomationID != keep.ID {
		t.Fatalf("cascade took the wrong schedules: %+v", left)
	}
	// The tick gate: without a bump the list keeps rendering the schedules it just deleted.
	if m.Version() <= before {
		t.Fatalf("Version() did not bump past %d - the automations tick would not repaint", before)
	}
}

// TestDeleteWithoutSchedulesStillBumps guards the cascade's early-out: the epoch must still move
// for the automation's own removal.
func TestDeleteWithoutSchedulesStillBumps(t *testing.T) {
	m := NewManager(mustStore(t), nil, noPreset, noopLog{})
	a, err := m.Save(Automation{Label: "lonely", WatchDir: t.TempDir(),
		Actions: []Action{{Type: ActionCopy, OutputDir: t.TempDir()}}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	before := m.Version()
	if err := m.Delete(a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if m.Version() <= before {
		t.Fatal("Version() did not bump on a schedule-less delete")
	}
	if _, ok := m.Get(a.ID); ok {
		t.Fatal("the automation survived its own delete")
	}
}

// realPresets resolves the shipped builtins: "remux" copies audio, "audioAac" re-encodes it.
func realPresets(id string) (transcode.Preset, bool) { return transcode.Find(id) }

// TestValidateLoudnessRejectsCopyCodec: normalization needs an audio re-encode, so a copy/none
// codec drops it. Accepting the target would store a setting that silently never runs.
func TestValidateLoudnessRejectsCopyCodec(t *testing.T) {
	acts := []Action{{Type: ActionTranscode, PresetID: "remux", LoudnessOn: true, LoudnessI: -14}}
	err := ValidateLoudness(acts, realPresets)
	if err == nil {
		t.Fatal("a LUFS target on a copy-audio preset was accepted - it would be saved and then ignored on every run")
	}
	// Structural validation must NOT have opinions here: the two rules stay separable.
	if err := ValidateActions(acts); err != nil {
		t.Fatalf("ValidateActions grew a loudness opinion: %v", err)
	}
}

// TestValidateLoudnessTrimSilenceDefaultPreset: a trim-silence step with no preset falls back to
// remux (copy audio), so its override is inert too - the fallback must be resolved, not skipped.
func TestValidateLoudnessTrimSilenceDefaultPreset(t *testing.T) {
	if err := ValidateLoudness([]Action{{Type: ActionTrimSilence, LoudnessOn: true}}, realPresets); err == nil {
		t.Fatal("trim-silence's remux default was not resolved, so an inert LUFS target passed")
	}
}

// TestValidateLoudnessAllowsReencode + the off/nil cases: the check must be narrow enough not to
// block anything legitimate.
func TestValidateLoudnessAllowsReencode(t *testing.T) {
	cases := []struct {
		name    string
		acts    []Action
		presets PresetResolver
	}{
		{"re-encoding preset", []Action{{Type: ActionTranscode, PresetID: "audioAac", LoudnessOn: true, LoudnessI: -14}}, realPresets},
		{"override off", []Action{{Type: ActionTranscode, PresetID: "remux"}}, realPresets},
		{"no resolver", []Action{{Type: ActionTranscode, PresetID: "remux", LoudnessOn: true}}, nil},
		{"unknown preset", []Action{{Type: ActionTranscode, PresetID: "nope", LoudnessOn: true}}, realPresets},
		{"non-transcoding step", []Action{{Type: ActionCopy, OutputDir: "x", LoudnessOn: true}}, realPresets},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateLoudness(c.acts, c.presets); err != nil {
				t.Fatalf("rejected a legitimate chain: %v", err)
			}
		})
	}
}
