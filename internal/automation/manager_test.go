package automation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/store"
	"rave.page/mate/internal/transcode"
)

type noopLog struct{}

func (noopLog) Info(string, string, map[string]any) {}
func (noopLog) Warn(string, string, map[string]any) {}

func noPreset(string) (transcode.Preset, bool) { return transcode.Preset{}, false }

// TestManagerRunManualCopy exercises CRUD + a copy-to run end-to-end (no worker/ffmpeg).
func TestManagerRunManualCopy(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	src := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(src, []byte("RIFFxxxxWAVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")

	m := NewManager(st, nil, noPreset, noopLog{})

	saved, err := m.Save(Automation{
		Label:    "copy wavs",
		WatchDir: dir,
		Enabled:  true,
		Match:    Match{Extensions: []string{".wav"}},
		Actions:  []Action{{Type: ActionCopy, OutputDir: out}},
	})
	if err != nil || saved.ID == "" {
		t.Fatalf("save: %v id=%q", err, saved.ID)
	}
	if got := m.List(); len(got) != 1 {
		t.Fatalf("list = %d", len(got))
	}

	run, err := m.RunManual(context.Background(), saved.ID, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("status = %q (%+v)", run.Status, run.Steps)
	}
	if _, err := os.Stat(filepath.Join(out, "in.wav")); err != nil {
		t.Fatalf("copy not produced: %v", err)
	}
	if runs := m.Runs(10); len(runs) != 1 || runs[0].Status != "success" {
		t.Fatalf("runs = %+v", runs)
	}

	// eligibility honours the extension match.
	if !m.eligible(saved, src) {
		t.Fatal("wav should be eligible")
	}
	mp3 := filepath.Join(dir, "x.mp3")
	_ = os.WriteFile(mp3, []byte("x"), 0o644)
	if m.eligible(saved, mp3) {
		t.Fatal("mp3 should not match .wav-only automation")
	}

	if err := m.Delete(saved.ID); err != nil || len(m.List()) != 0 {
		t.Fatalf("delete: %v remaining=%d", err, len(m.List()))
	}
}

func TestManagerSchedulesCRUD(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	m := NewManager(st, nil, noPreset, noopLog{})

	s, err := m.SaveSchedule(Schedule{Label: "nightly", Kind: ScheduleDaily, AtHour: 3, Enabled: true})
	if err != nil || s.ID == "" {
		t.Fatalf("save sched: %v", err)
	}
	if len(m.ListSchedules()) != 1 {
		t.Fatal("schedule not listed")
	}
	if err := m.DeleteSchedule(s.ID); err != nil || len(m.ListSchedules()) != 0 {
		t.Fatalf("delete sched: %v", err)
	}
}
