package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/worker"
)

// The engine path (watch / schedule / Run now) and the interactive path (the studio's manually
// gated runs) must do the SAME work: everything these tests pin is a place the two drifted.

// wcall is one recorded worker call.
type wcall struct {
	method string
	params map[string]any
}

// probeWorker answers transcode.silence with a fixed probe result and transcode.run by writing the
// output file, recording every call. It is what lets a test assert WHICH numbers reached the
// worker - the trim controls used to be read by the editor and then dropped on the floor.
type probeWorker struct {
	mu               sync.Mutex
	calls            []wcall
	lead, trail, dur float64
}

func (w *probeWorker) RunStream(_ context.Context, _, method string, params any, _ worker.ProgressFunc) (json.RawMessage, error) {
	p, ok := params.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("params %T, want map[string]any", params)
	}
	w.mu.Lock()
	w.calls = append(w.calls, wcall{method, p})
	w.mu.Unlock()
	switch method {
	case "transcode.silence":
		return json.RawMessage(fmt.Sprintf(`{"leadingSeconds":%g,"trailingSeconds":%g,"durationSeconds":%g}`,
			w.lead, w.trail, w.dur)), nil
	case "transcode.run":
		out, _ := p["output"].(string)
		if out == "" {
			return nil, fmt.Errorf("transcode.run without an output path")
		}
		return json.RawMessage(`{}`), os.WriteFile(out, []byte("encoded"), 0o644)
	}
	return nil, fmt.Errorf("unexpected worker method %q", method)
}

// of returns the recorded calls to one method.
func (w *probeWorker) of(method string) []wcall {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []wcall
	for _, c := range w.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func (w *probeWorker) reset() {
	w.mu.Lock()
	w.calls = nil
	w.mu.Unlock()
}

// newProbeSvc is a Service whose worker records calls and whose presets are the real builtins
// (trim-silence's "remux" default has to resolve).
func newProbeSvc(t *testing.T, w *probeWorker) *Service {
	t.Helper()
	return NewManager(mustStore(t), w, func(id string) (transcode.Preset, bool) { return transcode.Find(id) }, noopLog{})
}

// numOf reads a float param the worker was handed (JSON-free: params cross no wire in-process).
func numOf(t *testing.T, c wcall, key string) float64 {
	t.Helper()
	v, ok := c.params[key].(float64)
	if !ok {
		t.Fatalf("%s param %q = %#v, want a float64", c.method, key, c.params[key])
	}
	return v
}

// ── rename-from-event on the engine path (was: dead, "unknown action type") ──

// eventsServer stands in for the rave.page API: /auth/me + one booked event around `start`.
func eventsServer(t *testing.T, start time.Time) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/me" {
			_, _ = io.WriteString(w, `{"id":"u1"}`)
			return
		}
		_, _ = io.WriteString(w, fmt.Sprintf(
			`[{"id":"e1","title":"Tonight","starts_at":%q,"ends_at":%q,"venue_name":"Club X","slug":"tonight"}]`,
			start.Format(time.RFC3339), start.Add(4*time.Hour).Format(time.RFC3339)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunChainRenameFromEvent: rename-from-event RUNS on the engine path. ValidateActions accepted
// the step and the editor offered it, but runStep had no case for it - so every watch/schedule/Run
// now chain carrying one died on `unknown action type "rename-from-event"`.
func TestRunChainRenameFromEvent(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	start := time.Now().Add(-1 * time.Hour)
	srv := eventsServer(t, start)

	m := newTestSvc(t)
	m.SetBackgroundCredentials(srv.URL, "tok")
	a, err := m.Save(Automation{Label: "name it", WatchDir: dir, Actions: []Action{{Type: ActionRename}}})
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
	want := filepath.Join(dir, start.UTC().Format("2006-01-02")+"_Club-X_tonight.wav")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the renamed file must exist at %q: %v", want, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("the original name must be gone: %v", err)
	}
	if len(run.Steps) != 1 || run.Steps[0].OutputPath != want {
		t.Fatalf("step must record the new path: %+v", run.Steps)
	}
}

// TestRunChainRenameSharesTheInteractivePlan: both paths resolve the same target from the same
// booked event. A second implementation on the engine side is exactly how the two drift.
func TestRunChainRenameSharesTheInteractivePlan(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	srv := eventsServer(t, time.Now().Add(-1*time.Hour))

	m := newTestSvc(t)
	m.SetBackgroundCredentials(srv.URL, "tok")
	act := Action{Type: ActionRename}

	// What the manual gate would show the user...
	_, proposed, skip, err := m.buildRename(context.Background(), &runContext{currentPath: src}, act)
	if err != nil || skip != "" {
		t.Fatalf("buildRename: err=%v skip=%q", err, skip)
	}
	// ...must be exactly where an unattended run puts the file.
	a, _ := m.Save(Automation{Label: "name it", WatchDir: dir, Actions: []Action{act}})
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(run.Steps) != 1 || run.Steps[0].OutputPath != proposed {
		t.Fatalf("engine wrote %+v, the proposal promised %q", run.Steps, proposed)
	}
}

// TestRunChainRenameSkipsWithoutCredentials: no API token is a no-op, not a crash and not a
// silent success - the run reports partial and the file keeps its name.
func TestRunChainRenameSkipsWithoutCredentials(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	m := newTestSvc(t) // no SetBackgroundCredentials

	a, _ := m.Save(Automation{Label: "name it", WatchDir: dir, Actions: []Action{{Type: ActionRename}}})
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "partial" {
		t.Fatalf("status = %q, want partial: a step that did not happen is not a success (%+v)", run.Status, run.Steps)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("a skipped rename must leave the file alone: %v", err)
	}
}

// ── trim-silence: the editor's four controls must reach the worker ──

// TestTrimSilenceParamsReachTheProbe: threshold + min-silence used to be hardcoded 0 on the engine
// path (cachedSilenceProbe(..., 0, 0)), so the worker's -50dB/2s defaults always won and the
// editor's fields did nothing.
func TestTrimSilenceParamsReachTheProbe(t *testing.T) {
	dir := t.TempDir()
	w := &probeWorker{lead: 3, trail: 2, dur: 100}
	m := newProbeSvc(t, w)
	src := writeFile(t, filepath.Join(dir, "set.wav"))

	a, _ := m.Save(Automation{Label: "trim", WatchDir: dir, Actions: []Action{
		{Type: ActionTrimSilence, ThresholdDb: -30, MinSilenceSeconds: 5},
	}})
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("status = %q (%+v)", run.Status, run.Steps)
	}
	probes := w.of("transcode.silence")
	if len(probes) != 1 {
		t.Fatalf("expected 1 silence probe, got %d", len(probes))
	}
	if got := numOf(t, probes[0], "thresholdDb"); got != -30 {
		t.Errorf("thresholdDb = %v, want the action's -30", got)
	}
	if got := numOf(t, probes[0], "minSilence"); got != 5 {
		t.Errorf("minSilence = %v, want the action's 5", got)
	}
}

// TestTrimSilenceCutsBothEnds: trailing silence was never trimmed (trimEnd was hardcoded 0.0 in
// doTranscode), and trim-start=off did nothing because the probed lead was passed unconditionally.
func TestTrimSilenceCutsBothEnds(t *testing.T) {
	off := false
	cases := []struct {
		name               string
		act                Action
		wantStart, wantEnd float64
	}{
		// Defaults (both flags unset = on): cut the 3s lead, stop 2s before the end.
		{"both ends", Action{Type: ActionTrimSilence}, 3, 98},
		// Start off: keep the lead, still cut the tail.
		{"start off", Action{Type: ActionTrimSilence, TrimStart: &off}, 0, 98},
		// End off: cut the lead, encode to the end (trimEnd == duration → Args omits -t).
		{"end off", Action{Type: ActionTrimSilence, TrimEnd: &off}, 3, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			w := &probeWorker{lead: 3, trail: 2, dur: 100}
			m := newProbeSvc(t, w)
			src := writeFile(t, filepath.Join(dir, "set.wav"))

			a, _ := m.Save(Automation{Label: "trim", WatchDir: dir, Actions: []Action{tc.act}})
			if _, err := m.RunManual(context.Background(), a.ID, src); err != nil {
				t.Fatalf("run: %v", err)
			}
			runs := w.of("transcode.run")
			if len(runs) != 1 {
				t.Fatalf("expected 1 transcode.run, got %d", len(runs))
			}
			if got := numOf(t, runs[0], "trimStart"); got != tc.wantStart {
				t.Errorf("trimStart = %v, want %v", got, tc.wantStart)
			}
			if got := numOf(t, runs[0], "trimEnd"); got != tc.wantEnd {
				t.Errorf("trimEnd = %v, want %v", got, tc.wantEnd)
			}
		})
	}
}

// TestTrimSilenceBothFlagsOffSkips: with nothing to cut the step is a no-op, not a pointless
// re-encode - and the run says so (partial).
func TestTrimSilenceBothFlagsOffSkips(t *testing.T) {
	dir := t.TempDir()
	off := false
	w := &probeWorker{lead: 3, trail: 2, dur: 100}
	m := newProbeSvc(t, w)
	src := writeFile(t, filepath.Join(dir, "set.wav"))

	a, _ := m.Save(Automation{Label: "trim", WatchDir: dir, Actions: []Action{
		{Type: ActionTrimSilence, TrimStart: &off, TrimEnd: &off},
	}})
	run, err := m.RunManual(context.Background(), a.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(w.of("transcode.run")); n != 0 {
		t.Fatalf("a step with nothing to cut must not re-encode: %d transcode.run calls", n)
	}
	if run.Status != "partial" {
		t.Fatalf("status = %q, want partial for a skipped step (%+v)", run.Status, run.Steps)
	}
}

// TestSilenceCacheKeyIncludesProbeParams: the KindSilence cache is keyed by path+mtime ONLY, with
// the params folded into the blob and validated on read. A non-default threshold must therefore
// re-probe rather than read back a default-param result - and an identical repeat must still hit.
func TestSilenceCacheKeyIncludesProbeParams(t *testing.T) {
	dir := t.TempDir()
	w := &probeWorker{lead: 3, trail: 2, dur: 100}
	m := newProbeSvc(t, w)
	src := writeFile(t, filepath.Join(dir, "set.wav"))

	dflt, _ := m.Save(Automation{Label: "dflt", WatchDir: dir, Actions: []Action{{Type: ActionTrimSilence}}})
	hot, _ := m.Save(Automation{Label: "hot", WatchDir: dir, Actions: []Action{
		{Type: ActionTrimSilence, ThresholdDb: -30},
	}})

	// Warm the cache with a default-param probe (-50dB/2s).
	if _, err := m.RunManual(context.Background(), dflt.ID, src); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(w.of("transcode.silence")); n != 1 {
		t.Fatalf("expected 1 probe, got %d", n)
	}

	// Same params again → served from the cache (proves the cache is real, not that it never hits).
	w.reset()
	if _, err := m.RunManual(context.Background(), dflt.ID, src); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(w.of("transcode.silence")); n != 0 {
		t.Fatalf("an identical probe must hit the cache, got %d fresh probes", n)
	}

	// Different threshold, same file+mtime → params mismatch = miss, NOT a default-param readback.
	w.reset()
	if _, err := m.RunManual(context.Background(), hot.ID, src); err != nil {
		t.Fatalf("run: %v", err)
	}
	probes := w.of("transcode.silence")
	if len(probes) != 1 {
		t.Fatalf("a non-default threshold must re-probe, got %d probes (cache collision)", len(probes))
	}
	if got := numOf(t, probes[0], "thresholdDb"); got != -30 {
		t.Errorf("thresholdDb = %v, want -30", got)
	}
}

// ── proposal == commit ──

// TestTranscodeProposalMatchesCommit: the manual gate builds its proposal from the SAME normalized
// preset the commit encodes with. NormalizePreset drops loudness for a copy/none audio codec, so an
// un-normalized proposal claimed normalization that would never run.
func TestTranscodeProposalMatchesCommit(t *testing.T) {
	dir := t.TempDir()
	w := &probeWorker{}
	m := newProbeSvc(t, w)
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	rc := &runContext{origPath: src, currentPath: src}

	// "remux" copies audio (-c:a copy); the action asks to normalize anyway.
	act := Action{Type: ActionTranscode, PresetID: "remux", LoudnessOn: true, LoudnessI: -8}
	proposal, proposed, skip, err := m.buildTranscode(rc, act)
	if err != nil || skip != "" {
		t.Fatalf("buildTranscode: err=%v skip=%q", err, skip)
	}
	p, _ := proposal.(map[string]any)
	if _, claimed := p["loudnessOn"]; claimed {
		t.Fatal("the proposal must not promise normalization the encode drops for a copy audio codec")
	}

	out, err := m.commitStepSideEffects(context.Background(), rc, act, proposal, proposed)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if out != proposed {
		t.Fatalf("commit wrote %q, the proposal promised %q", out, proposed)
	}
	runs := w.of("transcode.run")
	if len(runs) != 1 {
		t.Fatalf("expected 1 transcode.run, got %d", len(runs))
	}
	pre, ok := runs[0].params["preset"].(transcode.Preset)
	if !ok {
		t.Fatalf("preset param = %#v, want a transcode.Preset", runs[0].params["preset"])
	}
	if pre.LoudnessOn {
		t.Error("the encoder must not be handed loudness it cannot apply to a copied audio stream")
	}
}

// TestTranscodeProposalShowsClampedLoudness: the positive control - where normalization CAN run,
// the proposal shows the clamped targets the encoder will really use, not the raw ask.
func TestTranscodeProposalShowsClampedLoudness(t *testing.T) {
	dir := t.TempDir()
	m := newProbeSvc(t, &probeWorker{})
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	rc := &runContext{origPath: src, currentPath: src}

	// -99 LUFS is outside the sane range; NormalizePreset clamps it to -36.
	act := Action{Type: ActionTranscode, PresetID: "mp3-320", LoudnessOn: true, LoudnessI: -99}
	proposal, _, skip, err := m.buildTranscode(rc, act)
	if err != nil || skip != "" {
		t.Fatalf("buildTranscode: err=%v skip=%q", err, skip)
	}
	p, _ := proposal.(map[string]any)
	if p["loudnessOn"] != true {
		t.Fatalf("mp3 re-encodes audio - the proposal must show normalization: %+v", p)
	}
	if got := p["loudnessI"]; got != -36.0 {
		t.Errorf("loudnessI = %v, want the clamped -36 the encoder will use", got)
	}
}

// ── schedules honour the automation's own switch ──

// TestOnScheduleSkipsDisabledAutomation: switching an automation OFF stops it running, whatever
// started it. onSchedule used to ignore the flag entirely (onWatchFile checks it), so a disabled
// automation - a delete-purge chain included - kept firing on its timer.
func TestOnScheduleSkipsDisabledAutomation(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := writeFile(t, filepath.Join(dir, "set.wav"))
	dest := filepath.Join(dir, "archive")

	a, err := m.Save(Automation{Label: "mv", WatchDir: dir, Enabled: false,
		Actions: []Action{{Type: ActionMove, OutputDir: dest}}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	s, err := m.SaveSchedule(Schedule{Label: "nightly", Enabled: true, AutomationID: a.ID,
		Kind: ScheduleInterval, IntervalMinutes: 1})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	m.onSchedule(s.ID)
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("a disabled automation must not touch the file: %v", err)
	}
	if runs := m.Runs(10); len(runs) != 0 {
		t.Fatalf("a disabled automation must not run: %+v", runs)
	}
	if got := m.ListSchedules(); len(got) != 1 || got[0].LastFiredAt != "" {
		t.Fatalf(`a fire that ran nothing must not claim "last fired": %+v`, got)
	}

	// Positive control: the same schedule, once the automation is switched back on.
	a.Enabled = true
	if _, err := m.Save(a); err != nil {
		t.Fatalf("save: %v", err)
	}
	m.onSchedule(s.ID)
	if _, err := os.Stat(filepath.Join(dest, "set.wav")); err != nil {
		t.Fatalf("an enabled automation must still run on its schedule: %v", err)
	}
	if runs := m.Runs(10); len(runs) != 1 || runs[0].Trigger != "schedule" {
		t.Fatalf("expected one scheduled run: %+v", runs)
	}
	if got := m.ListSchedules(); len(got) != 1 || got[0].LastFiredAt == "" {
		t.Fatalf("a fire that swept must record lastFired: %+v", got)
	}
}
