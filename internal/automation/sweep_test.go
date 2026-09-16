package automation

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// oldFile writes name into dir with content + an mtime ageDays in the past (for the MinAgeDays gate).
func oldFile(t *testing.T, dir, name string, ageDays int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("data-"+name), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().AddDate(0, 0, -ageDays)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

// sweepFixture builds a copy-chain automation over a dir holding 3 matching files (old .wav named
// set-*), 1 too-fresh .wav (fails the age gate + the pattern) and 2 .txt (fail the ext + pattern).
// The match set is deliberately narrowed by extension AND filename pattern AND min-age at once.
func sweepFixture(t *testing.T) (*Service, Automation, string) {
	t.Helper()
	dir := t.TempDir()
	oldFile(t, dir, "set-01.wav", 10)
	oldFile(t, dir, "set-02.wav", 10)
	oldFile(t, dir, "set-03.wav", 10)
	oldFile(t, dir, "fresh.wav", 0)   // fails MinAgeDays (and the ^set- pattern)
	oldFile(t, dir, "note-a.txt", 10) // fails the .wav ext (and the pattern)
	oldFile(t, dir, "note-b.txt", 10)
	m := NewManager(mustStore(t), nil, noPreset, noopLog{})
	a, err := m.Save(Automation{
		Label: "purge", WatchDir: dir, Enabled: true,
		Match:   Match{Extensions: []string{".wav"}, MinAgeDays: 1, FilenamePattern: "^set-"},
		Actions: []Action{{Type: ActionCopy, OutputDir: filepath.Join(dir, "out")}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	return m, a, dir
}

func fileNames(fs []SweepFile) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	sort.Strings(out)
	return out
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var wantMatched = []string{"set-01.wav", "set-02.wav", "set-03.wav"}

// TestPreviewIsExactlyTheSweepSet: Preview() lists exactly the files sweep()/a schedule fire would
// act on, with per-file size+mtime and a correct total.
func TestPreviewIsExactlyTheSweepSet(t *testing.T) {
	m, a, _ := sweepFixture(t)

	prev, err := m.Preview(a.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got := fileNames(prev.Files); !eqStrs(got, wantMatched) {
		t.Fatalf("preview files = %v, want %v", got, wantMatched)
	}
	if prev.Total != 3 {
		t.Fatalf("preview total = %d, want 3", prev.Total)
	}
	if prev.TotalBytes == 0 {
		t.Fatal("preview totalBytes = 0")
	}
	for _, f := range prev.Files {
		if f.Size == 0 || f.ModTime.IsZero() || f.Path == "" {
			t.Fatalf("preview file missing stat: %+v", f)
		}
	}
	// The preview must equal what the sweep run path (schedule + manual) actually enumerates.
	if got := baseNames(m.sweep(a)); !eqStrs(got, wantMatched) {
		t.Fatalf("sweep = %v, want %v", got, wantMatched)
	}
}

// TestRunSweepEqualsScheduleFire: RunSweep (manual default) and a schedule fire, over identical
// fixtures, act on the SAME files and produce the SAME per-file outputs - only the trigger differs.
func TestRunSweepEqualsScheduleFire(t *testing.T) {
	// Manual sweep.
	mm, ma, mdir := sweepFixture(t)
	res, err := mm.RunSweep(context.Background(), ma.ID)
	if err != nil {
		t.Fatalf("runsweep: %v", err)
	}
	if len(res.Runs) != 3 || res.Trigger != "manual" {
		t.Fatalf("res = %+v", res)
	}
	for _, r := range res.Runs {
		if r.Status != "success" || r.Trigger != "manual" {
			t.Fatalf("run = %+v", r)
		}
	}

	// Schedule fire over an identical fixture.
	sm, sa, sdir := sweepFixture(t)
	sch, err := sm.SaveSchedule(Schedule{Label: "nightly", Enabled: true, AutomationID: sa.ID,
		Kind: ScheduleInterval, IntervalMinutes: 1})
	if err != nil {
		t.Fatalf("save sched: %v", err)
	}
	sm.onSchedule(sch.ID)

	// Same files copied by both paths; the non-matching files untouched by both.
	manualOut := baseNames(lsDir(t, filepath.Join(mdir, "out")))
	schedOut := baseNames(lsDir(t, filepath.Join(sdir, "out")))
	if !eqStrs(manualOut, wantMatched) {
		t.Fatalf("manual output = %v, want %v", manualOut, wantMatched)
	}
	if !eqStrs(schedOut, wantMatched) {
		t.Fatalf("schedule output = %v, want %v", schedOut, wantMatched)
	}
	// Both recorded 3 runs; the schedule's carry trigger "schedule", the manual's "manual".
	if runs := sm.Runs(10); len(runs) != 3 {
		t.Fatalf("schedule runs = %d, want 3", len(runs))
	} else {
		for _, r := range runs {
			if r.Trigger != "schedule" {
				t.Fatalf("schedule run trigger = %q", r.Trigger)
			}
		}
	}
	if runs := mm.Runs(10); len(runs) != 3 {
		t.Fatalf("manual runs = %d, want 3", len(runs))
	}
}

// TestRunManualBypassesMatch: the secondary single-file path still runs a file that fails EVERY
// match rule, and records it with trigger "manual-file".
func TestRunManualBypassesMatch(t *testing.T) {
	m, a, dir := sweepFixture(t)
	txt := filepath.Join(dir, "note-a.txt") // fails ext + pattern + (moot) age
	run, err := m.RunManual(context.Background(), a.ID, txt)
	if err != nil {
		t.Fatalf("runmanual: %v", err)
	}
	if run.Trigger != "manual-file" {
		t.Fatalf("trigger = %q, want manual-file", run.Trigger)
	}
	if run.Status != "success" {
		t.Fatalf("status = %q (%+v)", run.Status, run.Steps)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "note-a.txt")); err != nil {
		t.Fatalf("manual file not copied: %v", err)
	}
}

func lsDir(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
