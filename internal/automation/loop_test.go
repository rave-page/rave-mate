package automation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rave.page/mate/internal/store"
	"rave.page/mate/internal/transcode"
)

// loopPresets resolves the presets the loop tests reference (output ext derives from Container).
func loopPresets(id string) (transcode.Preset, bool) {
	switch id {
	case "wavp":
		return transcode.Preset{ID: "wavp", Container: "wav", AudioCodec: "pcm_s16le"}, true
	case "mp3":
		return transcode.Preset{ID: "mp3", Container: "mp3", AudioCodec: "mp3", AudioBitrateK: 320}, true
	case trimPresetID:
		return transcode.Preset{ID: trimPresetID, Container: "wav", AudioCodec: "copy"}, true
	}
	return transcode.Preset{}, false
}

// TestCheckLoopDefiniteRefused: a transcode whose output lands in the watched folder and matches the
// rule (extensions empty = any) is a definite loop; Save refuses it and stores nothing.
func TestCheckLoopDefiniteRefused(t *testing.T) {
	watch := t.TempDir()
	a := Automation{Label: "loop", WatchDir: watch, Enabled: true,
		Match:   Match{},                                             // extensions empty = any → any produced file re-matches
		Actions: []Action{{Type: ActionTranscode, PresetID: "wavp"}}} // default output = alongside = watch dir
	if r := CheckLoop(a, loopPresets); r.Kind != LoopDefinite || r.Step != 1 {
		t.Fatalf("CheckLoop = %+v, want definite step 1", r)
	}
	m := NewManager(mustStore(t), nil, loopPresets, noopLog{})
	if _, err := m.Save(a); err == nil {
		t.Fatal("Save must refuse a definite feedback loop")
	}
	if len(m.List()) != 0 {
		t.Fatalf("a refused loop must not be stored: %d", len(m.List()))
	}
}

// TestCheckLoopPossibleWarns: a transcode producing a NON-matching extension into the watched folder
// is only a possible loop; Save allows it (the UI shows a warning).
func TestCheckLoopPossibleWarns(t *testing.T) {
	watch := t.TempDir()
	a := Automation{Label: "maybe", WatchDir: watch, Enabled: true,
		Match:   Match{Extensions: []string{".wav"}},
		Actions: []Action{{Type: ActionTranscode, PresetID: "mp3"}}} // output .mp3, does not match .wav
	if r := CheckLoop(a, loopPresets); r.Kind != LoopPossible {
		t.Fatalf("CheckLoop = %+v, want possible", r)
	}
	m := NewManager(mustStore(t), nil, loopPresets, noopLog{})
	if _, err := m.Save(a); err != nil {
		t.Fatalf("Save must allow a possible loop: %v", err)
	}
	if len(m.List()) != 1 {
		t.Fatal("a possible loop should be saved")
	}
}

// TestCheckLoopNoOverlapSaves: a transcode whose output goes OUTSIDE the watched folder is no loop.
func TestCheckLoopNoOverlapSaves(t *testing.T) {
	dir := t.TempDir()
	watch := filepath.Join(dir, "in")
	if err := os.MkdirAll(watch, 0o755); err != nil {
		t.Fatal(err)
	}
	a := Automation{Label: "clean", WatchDir: watch, Enabled: true,
		Match:   Match{}, // any ext, but the output is elsewhere
		Actions: []Action{{Type: ActionTranscode, PresetID: "wavp", OutputDir: filepath.Join(dir, "out")}}}
	if r := CheckLoop(a, loopPresets); r.Kind != LoopNone {
		t.Fatalf("CheckLoop = %+v, want none", r)
	}
	m := NewManager(mustStore(t), nil, loopPresets, noopLog{})
	if _, err := m.Save(a); err != nil {
		t.Fatalf("Save must allow a non-loop: %v", err)
	}
}

// TestSweepSkipsStoredLoop: a definite loop stored before the guard existed (or written over the
// wire) must not fire - RunSweep skips it and records the reason.
func TestSweepSkipsStoredLoop(t *testing.T) {
	watch := t.TempDir()
	// A real matching file so a non-skipping sweep WOULD act - proving the skip.
	if err := os.WriteFile(filepath.Join(watch, "set.wav"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := Automation{ID: "auto-loop", Label: "stored loop", WatchDir: watch, Enabled: true,
		Match: Match{}, Actions: []Action{{Type: ActionTranscode, PresetID: "wavp"}}}
	m := NewManager(mustStore(t), fakeTranscodeWorker{}, loopPresets, noopLog{})
	// Bypass Save's guard: store the loop directly, as old data or a wire write would.
	if err := m.st.PutJSON(store.BucketAutomations, a.ID, a); err != nil {
		t.Fatal(err)
	}
	m.invalidateAutos()

	res, err := m.RunSweep(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("runsweep: %v", err)
	}
	if len(res.Runs) != 0 {
		t.Fatalf("a stored loop must not sweep: %+v", res)
	}
	runs := m.Runs(10)
	if len(runs) != 1 || runs[0].Status != "error" {
		t.Fatalf("skip must record one error run: %+v", runs)
	}
	if len(runs[0].Steps) == 0 || !strings.Contains(runs[0].Steps[0].Error, "feedback loop") {
		t.Fatalf("recorded reason should name the feedback loop: %+v", runs[0].Steps)
	}
}

// TestCheckLoopFlagshipNotLoop: [transcode → move to archive → delete] is NOT a loop - the
// transcode output is relocated OUT of the watched folder before the run ends.
func TestCheckLoopFlagshipNotLoop(t *testing.T) {
	dir := t.TempDir()
	a := Automation{Label: "flagship", WatchDir: dir, Enabled: true,
		Match: Match{}, // any ext, yet no loop because the output leaves the watch dir
		Actions: []Action{
			{Type: ActionTranscode, PresetID: "mp3"}, // alongside = watch dir, transiently
			{Type: ActionMove, OutputDir: filepath.Join(dir, "..", "archive")},
			{Type: ActionDelete},
		}}
	if r := CheckLoop(a, loopPresets); r.Kind != LoopNone {
		t.Fatalf("flagship chain = %+v, want none", r)
	}
}

// TestCheckLoopTrimConverges: a trim-silence writing back into the watched folder converges (it
// skips when there is no silence), so it is only a POSSIBLE loop even with any-ext match.
func TestCheckLoopTrimConverges(t *testing.T) {
	dir := t.TempDir()
	a := Automation{Label: "trim", WatchDir: dir, Enabled: true, Match: Match{},
		Actions: []Action{{Type: ActionTrimSilence}}}
	if r := CheckLoop(a, loopPresets); r.Kind != LoopPossible {
		t.Fatalf("trim-into-watch = %+v, want possible", r)
	}
}

// TestCheckLoopMoveOriginalNotLoop: moving the ORIGINAL (already in the watch dir) into it is a
// no-op relocation, not a loop.
func TestCheckLoopMoveOriginalNotLoop(t *testing.T) {
	dir := t.TempDir()
	a := Automation{Label: "mv", WatchDir: dir, Enabled: true, Match: Match{},
		Actions: []Action{{Type: ActionMove, OutputDir: dir}}}
	if r := CheckLoop(a, loopPresets); r.Kind != LoopNone {
		t.Fatalf("move original into own watch dir = %+v, want none", r)
	}
}

// TestCheckLoopCopyProducedIntoWatch: a copy of a PRODUCED file into the watched folder is left
// behind as a new matching file → a definite loop.
func TestCheckLoopCopyProducedIntoWatch(t *testing.T) {
	dir := t.TempDir()
	a := Automation{Label: "cp", WatchDir: dir, Enabled: true, Match: Match{}, // any ext
		Actions: []Action{
			{Type: ActionTranscode, PresetID: "mp3", OutputDir: filepath.Join(dir, "..", "enc")}, // output outside
			{Type: ActionCopy, OutputDir: dir},                                                   // ...then duplicate it back into the watch dir
		}}
	if r := CheckLoop(a, loopPresets); r.Kind != LoopDefinite {
		t.Fatalf("copy produced into watch = %+v, want definite", r)
	}
}
