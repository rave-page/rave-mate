package vrccampaths

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/vrcloc"
)

func writeExport(t *testing.T, dir, name string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(`[{"Position":{"x":1},"Duration":2}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func exportTimeline(t *testing.T, dir string, locs ...vrcloc.Location) *vrcloc.Timeline {
	t.Helper()
	tl := vrcloc.NewTimeline(filepath.Join(dir, "timeline.json"))
	for _, l := range locs {
		tl.Record(l)
	}
	return tl
}

// A fresh export is backed up for the world the timeline puts at its mtime; re-sweeps are
// no-ops; a second export in the same world becomes that world's restorable latest.
func TestSweepExports(t *testing.T) {
	cam, backup := t.TempDir(), t.TempDir()
	now := time.Now()
	tl := exportTimeline(t, t.TempDir(),
		vrcloc.Location{JoinedAt: now.Add(-time.Hour), WorldID: "wrld_a", WorldName: "Club A", InstanceID: "i1"})
	first := writeExport(t, cam, "CameraPath_1.json", now.Add(-10*time.Minute))
	writeExport(t, cam, "CameraPath_1.json.meta.json", now.Add(-10*time.Minute)) // sidecar: ignored
	if err := os.Mkdir(filepath.Join(cam, "Organized"), 0o755); err != nil {
		t.Fatal(err)
	}

	seen := map[string]time.Time{}
	n, changed := SweepExports(cam, backup, tl, seen, now, nil)
	if n != 1 || !changed {
		t.Fatalf("first sweep n=%d changed=%v", n, changed)
	}
	e, ok := LatestBackup(backup, "wrld_a")
	if !ok || e.Source != first || e.WorldName != "Club A" {
		t.Fatalf("backup entry %+v ok=%v", e, ok)
	}
	if n, changed = SweepExports(cam, backup, tl, seen, now, nil); n != 0 || changed {
		t.Fatalf("re-sweep n=%d changed=%v", n, changed)
	}

	second := writeExport(t, cam, "CameraPath_2.json", now.Add(-5*time.Minute))
	if n, _ = SweepExports(cam, backup, tl, seen, now, nil); n != 1 {
		t.Fatalf("second export n=%d", n)
	}
	if e, _ = LatestBackup(backup, "wrld_a"); e.Source != second {
		t.Fatalf("latest = %+v, want the newer export", e)
	}

	// persistence round-trip keeps it idempotent across restarts
	SaveExportSeen(backup, seen)
	if n, _ = SweepExports(cam, backup, tl, LoadExportSeen(backup), now, nil); n != 0 {
		t.Fatalf("post-restart sweep n=%d", n)
	}
}

// Backfill: several pre-existing exports across two worlds process in mtime order - each
// world's latest wins its slot.
func TestSweepExportsBackfill(t *testing.T) {
	cam, backup := t.TempDir(), t.TempDir()
	now := time.Now()
	tl := exportTimeline(t, t.TempDir(),
		vrcloc.Location{JoinedAt: now.Add(-3 * time.Hour), WorldID: "wrld_a", WorldName: "Club A", InstanceID: "i1"},
		vrcloc.Location{JoinedAt: now.Add(-90 * time.Minute), WorldID: "wrld_b", WorldName: "Club B", InstanceID: "i2"})
	writeExport(t, cam, "a_old.json", now.Add(-170*time.Minute))
	aNew := writeExport(t, cam, "a_new.json", now.Add(-100*time.Minute))
	bOnly := writeExport(t, cam, "b.json", now.Add(-30*time.Minute))

	seen := map[string]time.Time{}
	if n, _ := SweepExports(cam, backup, tl, seen, now, nil); n != 3 {
		t.Fatalf("backfill n=%d", n)
	}
	if e, _ := LatestBackup(backup, "wrld_a"); e.Source != aNew {
		t.Fatalf("world A latest = %+v", e)
	}
	if e, _ := LatestBackup(backup, "wrld_b"); e.Source != bOnly {
		t.Fatalf("world B latest = %+v", e)
	}
}

// Unknown world (no timeline coverage, file too old for the current-world fallback) is
// marked seen without a backup; a mid-write file (inside quiesce) is left for next sweep;
// a young export with no at-mtime entry falls back to the CURRENT world.
func TestSweepExportsEdges(t *testing.T) {
	cam, backup := t.TempDir(), t.TempDir()
	now := time.Now()
	tl := exportTimeline(t, t.TempDir(),
		vrcloc.Location{JoinedAt: now.Add(-time.Minute), WorldID: "wrld_now", WorldName: "Now", InstanceID: "i9"})

	orphan := writeExport(t, cam, "orphan.json", now.Add(-24*time.Hour)) // pre-timeline
	writeExport(t, cam, "hot.json", now)                                 // inside quiesce
	fresh := writeExport(t, cam, "fresh.json", now.Add(-30*time.Second))

	seen := map[string]time.Time{}
	n, _ := SweepExports(cam, backup, tl, seen, now, nil)
	if n != 1 {
		t.Fatalf("n=%d want 1 (fresh only)", n)
	}
	if _, ok := seen[orphan]; !ok {
		t.Fatal("orphan not marked seen")
	}
	if _, ok := seen[filepath.Join(cam, "hot.json")]; ok {
		t.Fatal("quiesce file marked seen")
	}
	e, ok := LatestBackup(backup, "wrld_now")
	if !ok || e.Source != fresh {
		t.Fatalf("current-world fallback: %+v ok=%v", e, ok)
	}

	// deleting an export prunes its seen entry
	if err := os.Remove(fresh); err != nil {
		t.Fatal(err)
	}
	if _, changed := SweepExports(cam, backup, tl, seen, now, nil); !changed {
		t.Fatal("prune not reported")
	}
	if _, ok := seen[fresh]; ok {
		t.Fatal("seen entry survived deletion")
	}
}
