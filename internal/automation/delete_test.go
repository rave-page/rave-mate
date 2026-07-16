package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/worker"
)

func writeFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeTranscodeWorker stands in for the worker pool: it creates the output file transcode.run
// would have produced, which is what lets a test prove WHICH file a chain erases and which
// survives.
type fakeTranscodeWorker struct{}

func (fakeTranscodeWorker) RunStream(_ context.Context, _, method string, params any, _ worker.ProgressFunc) (json.RawMessage, error) {
	p, ok := params.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("params %T, want map[string]any", params)
	}
	out, _ := p["output"].(string)
	if method != "transcode.run" || out == "" {
		return nil, fmt.Errorf("unexpected worker call %q (output=%q)", method, out)
	}
	return json.RawMessage(`{}`), os.WriteFile(out, []byte("encoded"), 0o644)
}

// newTranscodeSvc is newTestSvc + a worker/preset pair that can actually run a transcode step.
func newTranscodeSvc(t *testing.T) *Service {
	t.Helper()
	preset := transcode.Preset{ID: "mp3", Container: "mp3", AudioCodec: "mp3", AudioBitrateK: 320}
	return NewManager(mustStore(t), fakeTranscodeWorker{},
		func(id string) (transcode.Preset, bool) { return preset, id == preset.ID }, noopLog{})
}

// fakeTrimWorker answers transcode.silence with a canned lead/trail/duration - the one input that
// decides whether planTrim skips - and otherwise behaves like fakeTranscodeWorker (writes the
// output file). lead=trail=0 makes the trim a no-op: the "already topped and tailed" recording.
type fakeTrimWorker struct{ lead, trail, dur float64 }

func (w fakeTrimWorker) RunStream(ctx context.Context, svc, method string, params any, pf worker.ProgressFunc) (json.RawMessage, error) {
	if method == "transcode.silence" {
		return json.RawMessage(fmt.Sprintf(
			`{"leadingSeconds":%v,"trailingSeconds":%v,"durationSeconds":%v}`, w.lead, w.trail, w.dur)), nil
	}
	return fakeTranscodeWorker{}.RunStream(ctx, svc, method, params, pf)
}

// newTrimSvc is newTestSvc + a worker whose silence probe reports the given window, and a preset
// resolver that knows the trim step's default remux preset (trimPresetID) as well as mp3.
func newTrimSvc(t *testing.T, lead, trail, dur float64) *Service {
	t.Helper()
	return NewManager(mustStore(t), fakeTrimWorker{lead, trail, dur}, func(id string) (transcode.Preset, bool) {
		switch id {
		case trimPresetID:
			return transcode.Preset{ID: trimPresetID, Container: "wav", AudioCodec: "copy"}, true
		case "mp3":
			return transcode.Preset{ID: "mp3", Container: "mp3", AudioCodec: "mp3", AudioBitrateK: 320}, true
		}
		return transcode.Preset{}, false
	}, noopLog{})
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

// TestMoveThenDeleteRejectedAndFailsClosed pins the total-data-loss case. [move-to
// <watch>/archive, delete] used to relocate the recording INTO a subfolder of the watch dir
// (so the containment guard still said "inside"), then erase the archived copy - the recording
// existed nowhere and the run reported "success". Now: rejected at validate, and a chain
// persisted before that rule fails closed at run with the archived copy intact.
func TestMoveThenDeleteRejectedAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	archive := filepath.Join(dir, "archive") // inside the watch dir: containment alone won't save it

	acts := []Action{{Type: ActionMove, OutputDir: archive}, {Type: ActionDelete}}
	if err := ValidateActions(acts); err == nil {
		t.Fatal("move-to followed by delete must be rejected: the move leaves the delete nothing to erase")
	}
	// Save deliberately doesn't validate (legacy chains), so the engine must fail closed itself.
	a, err := m.Save(Automation{Label: "archive", WatchDir: dir, Actions: acts})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archive, "set.wav")); err != nil {
		t.Fatalf("the moved recording must survive the delete: %v", err)
	}
	if run.Status == "success" {
		t.Fatalf("a chain that erases the only copy must never report success: %+v", run)
	}
	if len(run.Steps) != 2 || run.Steps[1].Error == "" {
		t.Fatalf("the delete step must error against the relocated original: %+v", run.Steps)
	}
}

// TestTranscodeThenDeleteKeepsOutput pins the inverse-intent case. [transcode, delete] with no
// OutputDir lands the MP3 inside the watch dir; delete used to target the threaded current file
// and erase that NEW MP3 while keeping the original WAV. Delete now consumes the chain's input:
// the source goes, the output stays.
func TestTranscodeThenDeleteKeepsOutput(t *testing.T) {
	dir := t.TempDir()
	m := newTranscodeSvc(t)
	src := writeFile(t, filepath.Join(dir, "set.wav"))

	acts := []Action{{Type: ActionTranscode, PresetID: "mp3"}, {Type: ActionDelete}}
	if err := ValidateActions(acts); err != nil {
		t.Fatalf("transcode + delete is the user's literal ask and must validate: %v", err)
	}
	a, err := m.Save(Automation{Label: "conv", WatchDir: dir, Actions: acts})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("status = %q, want success (%+v)", run.Status, run.Steps)
	}
	if _, err := os.Stat(filepath.Join(dir, "set-mp3.mp3")); err != nil {
		t.Fatalf("the transcode output must survive: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("the original source must be the file deleted: %v", err)
	}
}

// TestTrimSkippedThenDeleteKeepsOriginal pins the total-data-loss BLOCK. [trim-silence, delete]
// validates (trim-silence PRODUCES, so nothing relocates the original) and the delete is last -
// but a recording with no leading/trailing silence (an Icecast capture starting on the first
// beat, or an already topped-and-tailed set) makes planTrim skip, so NO trimmed file is written.
// The delete must refuse: erasing the source would leave the recording nowhere.
func TestTrimSkippedThenDeleteKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	m := newTrimSvc(t, 0, 0, 100) // no silence either end → planTrim skips
	src := writeFile(t, filepath.Join(dir, "set.wav"))

	acts := []Action{{Type: ActionTrimSilence}, {Type: ActionDelete}}
	if err := ValidateActions(acts); err != nil {
		t.Fatalf("trim + delete is structurally sound and must save: %v", err)
	}
	a, err := m.Save(Automation{Label: "trim", WatchDir: dir, Actions: acts})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the trim produced nothing, so the original is the ONLY copy and must survive: %v", err)
	}
	if run.Status == "success" {
		t.Fatalf("a run that refused its delete must not report success: %+v", run)
	}
	if len(run.Steps) != 2 || run.Steps[1].Error == "" {
		t.Fatalf("the delete step must error, naming why nothing survives: %+v", run.Steps)
	}
}

// TestTrimProducedThenDeleteDropsOriginal is the same chain's happy path: the probe finds silence,
// the trim writes a file, and the delete may now consume the source. Pins that the survivor guard
// gates on what was PRODUCED, not on the step types (which are identical to the test above).
func TestTrimProducedThenDeleteDropsOriginal(t *testing.T) {
	dir := t.TempDir()
	m := newTrimSvc(t, 5, 2, 100) // 5s lead, 2s trail → a real cut
	src := writeFile(t, filepath.Join(dir, "set.wav"))

	a, err := m.Save(Automation{Label: "trim", WatchDir: dir, Actions: []Action{
		{Type: ActionTrimSilence}, {Type: ActionDelete},
	}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("status = %q, want success (%+v)", run.Status, run.Steps)
	}
	if _, err := os.Stat(filepath.Join(dir, "set-"+trimPresetID+".wav")); err != nil {
		t.Fatalf("the trimmed file must survive: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("with a trimmed copy on disk the source must go: %v", err)
	}
}

// TestRenameSkippedThenDeleteKeepsOriginal pins the round-3 HIGH: a rename-from-event that SKIPS
// (no API creds - a common default) does not relocate the original, so a following delete would
// erase the only copy. ValidateActions rejects [rename, delete] at authoring, but Save is
// permissive and remotectl.automations.save reaches the engine without validating - the live
// bypass. The survivor gate must count a skipped rename, exactly like a skipped trim.
func TestRenameSkippedThenDeleteKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	m := newTranscodeSvc(t) // no bgCreds → planRename skips with "API token not provided"
	src := writeFile(t, filepath.Join(dir, "set.wav"))

	acts := []Action{{Type: ActionRename}, {Type: ActionDelete}}
	if err := ValidateActions(acts); err == nil {
		t.Fatal("ValidateActions must reject [rename, delete] - this test exercises the Save/remotectl bypass")
	}
	a, err := m.Save(Automation{Label: "rename", WatchDir: dir, Actions: acts}) // bypasses validate
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the rename skipped, so the original is the ONLY copy and must survive: %v", err)
	}
	if run.Status == "success" {
		t.Fatalf("a run that refused its delete must not report success: %+v", run)
	}
	if len(run.Steps) != 2 || run.Steps[1].Error == "" {
		t.Fatalf("the delete step must error, naming why nothing survives: %+v", run.Steps)
	}
}

// TestTranscodeMoveDeleteIsTheFlagshipChain pins the HIGH: [transcode, move-to, delete] is
// "convert the recording, file the MP3 into my library, drop the source WAV". The old index-blind
// prefix scan rejected it - it could not tell a move of the transcode OUTPUT (harmless: current
// already points away from the original) from a move of the chain's INPUT (fatal). The engine
// always ran it correctly; both authoring surfaces gate on ValidateActions, so it was unsaveable.
func TestTranscodeMoveDeleteIsTheFlagshipChain(t *testing.T) {
	dir := t.TempDir()
	lib := t.TempDir() // the library folder the MP3 gets filed into
	m := newTranscodeSvc(t)
	src := writeFile(t, filepath.Join(dir, "set.wav"))

	acts := []Action{
		{Type: ActionTranscode, PresetID: "mp3"},
		{Type: ActionMove, OutputDir: lib},
		{Type: ActionDelete},
	}
	if err := ValidateActions(acts); err != nil {
		t.Fatalf("the move relocates the transcode OUTPUT, not the original - must validate: %v", err)
	}
	a, err := m.Save(Automation{Label: "file it", WatchDir: dir, Actions: acts})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("status = %q, want success (%+v)", run.Status, run.Steps)
	}
	if _, err := os.Stat(filepath.Join(lib, "set-mp3.mp3")); err != nil {
		t.Fatalf("the moved output must survive at its destination: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("the source WAV must be the file dropped: %v", err)
	}
}

// TestCopyMoveDeleteRejectedAndFailsClosed: copy-to does NOT move the working file, so a move
// behind it still relocates the ORIGINAL - and the delete would erase nothing while the chain's
// only untouched artifact walked out of the watch dir. Rejected at validate; fails closed at run.
func TestCopyMoveDeleteRejectedAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	backup, archive := filepath.Join(dir, "backup"), filepath.Join(dir, "archive")

	acts := []Action{
		{Type: ActionCopy, OutputDir: backup},
		{Type: ActionMove, OutputDir: archive},
		{Type: ActionDelete},
	}
	if err := ValidateActions(acts); err == nil {
		t.Fatal("a copy doesn't move current, so the move still relocates the original: must reject")
	}
	a, err := m.Save(Automation{Label: "legacy", WatchDir: dir, Actions: acts})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status == "success" {
		t.Fatalf("must not report success: %+v", run)
	}
	if len(run.Steps) != 3 || run.Steps[2].Error == "" {
		t.Fatalf("the delete step must error against the relocated original: %+v", run.Steps)
	}
	if _, err := os.Stat(filepath.Join(archive, "set.wav")); err != nil {
		t.Fatalf("the moved recording must survive: %v", err)
	}
}

// TestManualSkipOfTrimRefusesDelete pins the invariant on the OTHER path. runInteractive +
// buildStep/commitStepSideEffects must apply the survivor rule too: a human who skips the trim
// gate has produced nothing, so the delete behind it must refuse rather than charge them the
// recording for declining a step. The refusal surfaces at PROPOSE time - the gate never even
// offers a delete it would go on to refuse.
func TestManualSkipOfTrimRefusesDelete(t *testing.T) {
	dir := t.TempDir()
	m := newTrimSvc(t, 5, 2, 100) // real silence: the trim WOULD produce - the human declines it
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	a, err := m.Save(Automation{Label: "trim", WatchDir: dir, Actions: []Action{
		{Type: ActionTrimSilence}, {Type: ActionDelete},
	}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	get, unsub := collectEvents(m)
	defer unsub()
	runID, err := m.StartRun(ModeManual, a.ID, src)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, get, func(e []RunEvent) bool { return hasState(e, StateAwaiting) })
	if err := m.SkipStep(runID); err != nil {
		t.Fatalf("skip: %v", err)
	}
	// Terminal StateError at Step == len(actions); emitted after the run is persisted.
	waitFor(t, get, func(e []RunEvent) bool {
		for _, ev := range e {
			if ev.State == StateError && ev.Step == 2 {
				return true
			}
		}
		return false
	})
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("skipping the trim must not cost the user the recording: %v", err)
	}
	// The delete must never have been gated for confirmation: only the trim awaited.
	awaiting := 0
	for _, ev := range get() {
		if ev.State == StateAwaiting {
			awaiting++
		}
	}
	if awaiting != 1 {
		t.Fatalf("delete must be refused at propose time, not offered: %d awaiting gates", awaiting)
	}
	runs := m.Runs(10)
	if len(runs) != 1 || runs[0].Status == "success" {
		t.Fatalf("a run whose delete refused must not report success: %+v", runs)
	}
}

// TestRunStepDeleteNeverFallsBackToCurrent: with the original already gone, delete errors and
// leaves the working file alone. No fallback to `current` - not even when `current` sits inside
// the watch dir and would clear every guard.
func TestRunStepDeleteNeverFallsBackToCurrent(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "set.wav") // never created: an earlier step relocated it
	current := writeFile(t, filepath.Join(dir, "set-mp3.mp3"))

	if _, err := runStep(context.Background(), engine{}, Action{Type: ActionDelete}, dir, orig, current, survivors{}); err == nil {
		t.Fatal("delete must error when the chain's input is missing")
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("the working file must never be erased in the original's place: %v", err)
	}
}

// TestRunChainDeleteTerminal: copy-to then delete = the archive shape. The copy leaves the
// working file on the original, delete consumes that original, and the chain ends in success
// with the archived copy intact.
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

// TestValidateActions covers the delete rules and the per-action requirements. The delete rule is
// "reject iff the ORIGINAL is relocated before it": a relocating step (move-to, rename-from-event)
// reached while the working file is STILL the original. Position decides, not step type - the same
// move-to is fatal at the head of a chain and harmless after a producing step.
func TestValidateActions(t *testing.T) {
	ok := []([]Action){
		{{Type: ActionDelete}}, // the pure-purge shape
		{{Type: ActionCopy, OutputDir: "d"}, {Type: ActionDelete}},                                           // the archive shape: copy leaves current on the original
		{{Type: ActionTranscode, PresetID: "mp3-320"}, {Type: ActionDelete}},                                 // convert, drop the source
		{{Type: ActionTranscode, PresetID: "mp3"}, {Type: ActionMove, OutputDir: "d"}, {Type: ActionDelete}}, // the flagship: the move relocates the OUTPUT
		{{Type: ActionTrimSilence}, {Type: ActionMove, OutputDir: "d"}, {Type: ActionDelete}},                // trim produces too
		{{Type: ActionTranscode, PresetID: "mp3"}, {Type: ActionRename}, {Type: ActionDelete}},               // rename of the output, likewise
		{{Type: ActionTranscode, PresetID: "mp3-320"}},
		{{Type: ActionMove, OutputDir: "d"}},                             // a move on its own is fine - it's move-before-delete that isn't
		{{Type: ActionRename}, {Type: ActionTranscode, PresetID: "mp3"}}, // same for a rename
	}
	for i, acts := range ok {
		if err := ValidateActions(acts); err != nil {
			t.Errorf("case %d: unexpected error: %v", i, err)
		}
	}
	bad := []([]Action){
		{},
		{{Type: ActionDelete}, {Type: ActionCopy, OutputDir: "d"}},                                     // delete not last
		{{Type: ActionMove, OutputDir: "d"}, {Type: ActionDelete}},                                     // the move relocated the original itself
		{{Type: ActionMove, OutputDir: "d"}, {Type: ActionCopy, OutputDir: "e"}, {Type: ActionDelete}}, // ...anywhere earlier
		{{Type: ActionRename}, {Type: ActionDelete}},                                                   // the rename relocated the original too
		{{Type: ActionCopy, OutputDir: "d"}, {Type: ActionMove, OutputDir: "e"}, {Type: ActionDelete}}, // copy doesn't move current, so the move still takes the original
		{{Type: ActionTranscode}}, // no preset
		{{Type: ActionMove}},      // no output dir
		{{Type: ActionType("nope")}},
	}
	for i, acts := range bad {
		if err := ValidateActions(acts); err == nil {
			t.Errorf("case %d: expected an error, got none", i)
		}
	}
}
