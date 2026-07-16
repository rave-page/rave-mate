package automation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/transcode"
)

func writeFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDeleteTargetGuards: the delete guard fails closed on everything outside a plain regular
// file inside the watch dir.
func TestDeleteTargetGuards(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	inside := writeFile(t, filepath.Join(root, "in.wav"))
	sub := filepath.Join(root, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := writeFile(t, filepath.Join(sub, "deep.wav"))

	if got, err := deleteTarget(root, inside); err != nil || got != inside {
		t.Fatalf("file in watch dir: got %q err %v", got, err)
	}
	if _, err := deleteTarget(root, nested); err != nil {
		t.Fatalf("file below watch dir must pass: %v", err)
	}
	for name, tc := range map[string]struct{ root, path string }{
		"outside root":  {root, writeFile(t, filepath.Join(outside, "x.wav"))},
		"empty path":    {root, ""},
		"empty root":    {"", inside},
		"the root dir":  {root, root},
		"a directory":   {root, sub},
		"missing file":  {root, filepath.Join(root, "gone.wav")},
		"escaping root": {root, filepath.Join(root, "..", filepath.Base(outside), "x.wav")},
	} {
		if _, err := deleteTarget(tc.root, tc.path); err == nil {
			t.Errorf("%s: expected refusal, got none", name)
		}
	}
}

// TestRunChainDeleteTerminal: copy-to then delete = "back it up, then drop the original".
// The delete consumes the working file and ends the chain with a clean success.
func TestRunChainDeleteTerminal(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	backup := filepath.Join(dir, "backup")

	a, err := m.Save(Automation{Label: "archive", WatchDir: dir, Actions: []Action{
		{Type: ActionCopy, OutputDir: backup},
		{Type: ActionDelete},
	}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("status = %q (%+v)", run.Status, run.Steps)
	}
	if _, err := os.Stat(filepath.Join(backup, "set.wav")); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("original still present: %v", err)
	}
	if len(run.Steps) != 2 || !run.Steps[1].OK || run.Steps[1].OutputPath != "" {
		t.Fatalf("delete step should record OK with no output: %+v", run.Steps)
	}
}

// TestRunChainDeleteStopsTrailingSteps: a legacy chain with actions after a delete stops at the
// delete (partial) instead of running a step against the deleted path.
func TestRunChainDeleteStopsTrailingSteps(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	after := filepath.Join(dir, "after")

	a, _ := m.Save(Automation{Label: "bad", WatchDir: dir, Actions: []Action{
		{Type: ActionDelete},
		{Type: ActionCopy, OutputDir: after},
	}})
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "partial" {
		t.Fatalf("status = %q, want partial (%+v)", run.Status, run.Steps)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("chain must stop at the delete: %+v", run.Steps)
	}
	if _, err := os.Stat(after); !os.IsNotExist(err) {
		t.Fatalf("trailing step ran after delete: %v", err)
	}
}

// TestRunChainDeleteOutsideWatchDirRefused: the guard is the engine's, not just the UI's.
func TestRunChainDeleteOutsideWatchDirRefused(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	m := newTestSvc(t)
	src := writeFile(t, filepath.Join(other, "set.wav"))

	a, _ := m.Save(Automation{Label: "rm", WatchDir: dir, Actions: []Action{{Type: ActionDelete}}})
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "error" {
		t.Fatalf("status = %q, want error (%+v)", run.Status, run.Steps)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("file outside the watch dir must survive: %v", err)
	}
}

// TestManualModeGatesDelete: the destructive step must wait for an explicit commit.
func TestManualModeGatesDelete(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	a, _ := m.Save(Automation{Label: "rm", WatchDir: dir, Actions: []Action{{Type: ActionDelete}}})

	get, unsub := collectEvents(m)
	defer unsub()
	runID, err := m.StartRun(ModeManual, a.ID, src)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, get, func(e []RunEvent) bool { return hasState(e, StateAwaiting) })
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("file deleted before commit: %v", err)
	}
	if err := m.CommitStep(runID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	waitFor(t, get, func(e []RunEvent) bool { return hasTerminal(e, 1) })
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("file not deleted after commit: %v", err)
	}
}

// TestManualModeAbortDelete: aborting the gate leaves the file alone.
func TestManualModeAbortDelete(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	a, _ := m.Save(Automation{Label: "rm", WatchDir: dir, Actions: []Action{{Type: ActionDelete}}})

	get, unsub := collectEvents(m)
	defer unsub()
	runID, _ := m.StartRun(ModeManual, a.ID, src)
	waitFor(t, get, func(e []RunEvent) bool { return hasState(e, StateAwaiting) })
	if err := m.AbortRun(runID); err != nil {
		t.Fatalf("abort: %v", err)
	}
	waitFor(t, get, func(e []RunEvent) bool { return hasState(e, StateAborted) })
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("aborted delete must not remove the file: %v", err)
	}
}

// TestMatchMinAgeDays: the age gate keys off mtime and fails closed on an unknown one.
func TestMatchMinAgeDays(t *testing.T) {
	now := time.Now()
	m := Match{MinAgeDays: 7}
	if m.matches(".wav", "a.wav", 1, now.Add(-6*24*time.Hour)) {
		t.Error("6-day-old file must not pass a 7-day gate")
	}
	if !m.matches(".wav", "a.wav", 1, now.Add(-8*24*time.Hour)) {
		t.Error("8-day-old file must pass a 7-day gate")
	}
	if m.matches(".wav", "a.wav", 1, time.Time{}) {
		t.Error("unknown mtime must fail closed")
	}
	// 0 = off: any mtime passes.
	if !(Match{}).matches(".wav", "a.wav", 1, now) {
		t.Error("no age gate must accept a fresh file")
	}
}

// TestEligibleMinAgeDays wires the age gate through the stat-based eligibility path.
func TestEligibleMinAgeDays(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	old := writeFile(t, filepath.Join(dir, "old.wav"))
	fresh := writeFile(t, filepath.Join(dir, "fresh.wav"))
	aged := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(old, aged, aged); err != nil {
		t.Fatal(err)
	}
	a := Automation{WatchDir: dir, Match: Match{Extensions: []string{".wav"}, MinAgeDays: 30}}
	if !m.eligible(a, old) {
		t.Error("40-day-old file should be eligible under a 30-day gate")
	}
	if m.eligible(a, fresh) {
		t.Error("fresh file must not be eligible under a 30-day gate")
	}
}

// TestApplyActionLoudness pins the override semantics: the action wins only when it opts in,
// zero values fall back to defaults, and an untouched action leaves the preset alone.
func TestApplyActionLoudness(t *testing.T) {
	preset := transcode.Preset{ID: "p", Container: "mp3", AudioCodec: "mp3",
		LoudnessOn: true, LoudnessI: -18, LoudnessTP: -2, LoudnessRaiseOnly: true}

	// No override → preset's own loudness survives verbatim (back-compat for saved chains).
	if got := applyActionLoudness(preset, Action{Type: ActionTranscode}); got != preset {
		t.Fatalf("preset must be untouched without an override: %+v", got)
	}
	// Override → replaces the whole loudness block, not a field-wise merge.
	got := applyActionLoudness(preset, Action{Type: ActionTranscode, LoudnessOn: true, LoudnessI: -8, LoudnessTP: -0.3})
	if !got.LoudnessOn || got.LoudnessI != -8 || got.LoudnessTP != -0.3 || got.LoudnessRaiseOnly {
		t.Fatalf("action override should win wholesale: %+v", got)
	}
	// Zero target → default streaming target; zero TP → EffectiveTP's default ceiling.
	got = applyActionLoudness(transcode.Preset{ID: "q"}, Action{Type: ActionTranscode, LoudnessOn: true})
	if got.LoudnessI != defaultLoudnessI {
		t.Errorf("LoudnessI = %v, want %v", got.LoudnessI, defaultLoudnessI)
	}
	if got.EffectiveTP() != transcode.DefaultLoudnessTP {
		t.Errorf("EffectiveTP = %v, want %v", got.EffectiveTP(), transcode.DefaultLoudnessTP)
	}
	// A preset with no loudness stays that way when the action doesn't ask for any.
	if got := applyActionLoudness(transcode.Preset{ID: "q"}, Action{Type: ActionTranscode, LoudnessI: -14}); got.LoudnessOn {
		t.Error("LoudnessI without LoudnessOn must not enable normalization")
	}
}

// TestValidateActions covers the delete-is-terminal rule + the per-action requirements.
func TestValidateActions(t *testing.T) {
	ok := []([]Action){
		{{Type: ActionDelete}},
		{{Type: ActionCopy, OutputDir: "d"}, {Type: ActionDelete}},
		{{Type: ActionTranscode, PresetID: "mp3-320"}},
	}
	for i, acts := range ok {
		if err := ValidateActions(acts); err != nil {
			t.Errorf("case %d: unexpected error: %v", i, err)
		}
	}
	bad := []([]Action){
		{},
		{{Type: ActionDelete}, {Type: ActionCopy, OutputDir: "d"}}, // delete not last
		{{Type: ActionTranscode}},                                  // no preset
		{{Type: ActionMove}},                                       // no output dir
		{{Type: ActionType("nope")}},
	}
	for i, acts := range bad {
		if err := ValidateActions(acts); err == nil {
			t.Errorf("case %d: expected an error, got none", i)
		}
	}
}
