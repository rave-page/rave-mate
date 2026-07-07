package vrcphotos

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/vrcloc"
)

func tm(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	return t
}

func TestPhotoTime(t *testing.T) {
	mod := tm("2020-01-01 00:00:00")
	// date-first ordering
	got := PhotoTime("VRChat_2026-06-30_21-15-30.123_3840x2160.png", mod)
	if !got.Equal(tm("2026-06-30 21:15:30")) {
		t.Errorf("date-first = %v", got)
	}
	// resolution-first ordering
	got = PhotoTime("VRChat_3840x2160_2026-06-30_21-15-30.123.png", mod)
	if !got.Equal(tm("2026-06-30 21:15:30")) {
		t.Errorf("res-first = %v", got)
	}
	// no stamp → modTime
	if got = PhotoTime("random.png", mod); !got.Equal(mod) {
		t.Errorf("fallback = %v, want modTime", got)
	}
}

func newTL() *vrcloc.Timeline {
	tl := vrcloc.NewTimeline("")
	tl.Record(vrcloc.Location{JoinedAt: tm("2026-06-30 20:00:00"), WorldID: "w1", WorldName: "Club B", InstanceID: "w1:1"})
	tl.Record(vrcloc.Location{JoinedAt: tm("2026-06-30 22:00:00"), WorldID: "w2", WorldName: "Rave", InstanceID: "w2:1~group(grp_x)", GroupID: "grp_x", GroupName: "Night Owls"})
	return tl
}

func TestPlanFolder(t *testing.T) {
	tl := newTL()
	// during first instance
	if got, ok := PlanFolder(tm("2026-06-30 20:30:00"), tl, nil); !ok || got != "Club B (2026-06-30)" {
		t.Errorf("plan1 = %q ok=%v", got, ok)
	}
	// during group instance
	if got, ok := PlanFolder(tm("2026-06-30 22:30:00"), tl, nil); !ok || got != "Night Owls - Rave (2026-06-30)" {
		t.Errorf("plan2 = %q ok=%v", got, ok)
	}
	// before any known location
	if _, ok := PlanFolder(tm("2026-06-30 19:00:00"), tl, nil); ok {
		t.Error("expected unknown before first join")
	}
	// event match (primary) overrides the world folder when the time is in an event window
	ev := fakeEvents{{Name: "Summer Fest", Start: tm("2026-06-30 20:00:00"), End: tm("2026-06-30 21:00:00")}}
	if got, ok := PlanFolder(tm("2026-06-30 20:30:00"), tl, ev); !ok || got != "Summer Fest (2026-06-30)" {
		t.Errorf("event plan = %q", got)
	}
	// time outside any event window → falls back to the world folder
	if got, ok := PlanFolder(tm("2026-06-30 22:30:00"), tl, ev); !ok || got != "Night Owls - Rave (2026-06-30)" {
		t.Errorf("event fallback = %q ok=%v", got, ok)
	}
	// event match works with NO timeline data (old photo, in an event window)
	if got, ok := PlanFolder(tm("2026-06-30 20:30:00"), vrcloc.NewTimeline(""), ev); !ok || got != "Summer Fest (2026-06-30)" {
		t.Errorf("event without timeline = %q ok=%v", got, ok)
	}
}

// fakeEvents is a test EventSource: returns the first event whose window contains t.
type fakeEvents []Event

func (f fakeEvents) EventAt(t time.Time) (Event, bool) {
	for _, e := range f {
		if !t.Before(e.Start) && !t.After(e.End) {
			return e, true
		}
	}
	return Event{}, false
}

func TestProcessOrganizesByEvent(t *testing.T) {
	root := t.TempDir()
	photo := filepath.Join(root, "2026-06", "VRChat_2026-06-30_20-30-00.000_1920x1080.png")
	_ = os.MkdirAll(filepath.Dir(photo), 0o755)
	if err := os.WriteFile(photo, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := fakeEvents{{Name: "Summer Fest", Start: tm("2026-06-30 20:00:00"), End: tm("2026-06-30 21:00:00")}}
	o := New(root, newTL(), Copy, ev, nil)
	dest, ok, err := o.Process(photo)
	if err != nil || !ok {
		t.Fatalf("Process ok=%v err=%v", ok, err)
	}
	want := filepath.Join(root, organizedDir, "Summer Fest (2026-06-30)")
	if filepath.Dir(dest) != want {
		t.Errorf("dest dir = %q, want %q", filepath.Dir(dest), want)
	}
}

func TestProcessOutOfWindowFallsBackToWorld(t *testing.T) {
	root := t.TempDir()
	// 22:30 is outside the event window but inside the group instance → world fallback, still organized
	photo := filepath.Join(root, "2026-06", "VRChat_2026-06-30_22-30-00.000_1920x1080.png")
	_ = os.MkdirAll(filepath.Dir(photo), 0o755)
	_ = os.WriteFile(photo, []byte("png"), 0o644)
	ev := fakeEvents{{Name: "Summer Fest", Start: tm("2026-06-30 20:00:00"), End: tm("2026-06-30 21:00:00")}}
	dest, ok, _ := New(root, newTL(), Copy, ev, nil).Process(photo)
	if !ok || filepath.Base(filepath.Dir(dest)) != "Night Owls - Rave (2026-06-30)" {
		t.Errorf("out-of-window should fall back to world, got %q ok=%v", dest, ok)
	}
}

func TestScanAllIncludesUnorganized(t *testing.T) {
	root := t.TempDir()
	// loose photo before any timeline entry → unorganized but listed
	loose := filepath.Join(root, "2020-01", "VRChat_2020-01-01_00-00-00.000_1920x1080.png")
	_ = os.MkdirAll(filepath.Dir(loose), 0o755)
	_ = os.WriteFile(loose, []byte("png"), 0o644)
	// loose photo during the group instance → labeled by world
	worldShot := filepath.Join(root, "2026-06", "VRChat_2026-06-30_22-30-00.000_1920x1080.png")
	_ = os.MkdirAll(filepath.Dir(worldShot), 0o755)
	_ = os.WriteFile(worldShot, []byte("png"), 0o644)
	// an already-organized copy under Organized/<event>/
	orgDir := filepath.Join(root, organizedDir, "Summer Fest (2026-06-30)")
	_ = os.MkdirAll(orgDir, 0o755)
	org := filepath.Join(orgDir, "VRChat_2026-06-30_20-30-00.000_1920x1080.png")
	_ = os.WriteFile(org, []byte("png"), 0o644)

	photos := ScanAll(root, newTL(), nil)
	if len(photos) != 3 {
		t.Fatalf("ScanAll = %d photos, want 3", len(photos))
	}
	// newest first: 22:30 worldShot, then 20:30 organized, then 2020 loose
	if photos[0].TakenAt.Before(photos[1].TakenAt) || photos[1].TakenAt.Before(photos[2].TakenAt) {
		t.Error("ScanAll not sorted newest-first")
	}
	byName := map[string]Photo{}
	for _, p := range photos {
		byName[filepath.Base(p.File)] = p
	}
	if got := byName[filepath.Base(loose)]; got.Label != Unorganized || got.Organized {
		t.Errorf("loose old photo label=%q organized=%v, want Unorganized/false", got.Label, got.Organized)
	}
	if got := byName[filepath.Base(worldShot)]; got.Label != "Night Owls · Rave" {
		t.Errorf("world-shot label=%q, want world/group", got.Label)
	}
	if got := byName[filepath.Base(org)]; !got.Organized || got.Label != "Summer Fest (2026-06-30)" {
		t.Errorf("organized photo label=%q organized=%v", got.Label, got.Organized)
	}
}

func TestScanAllDedupesPreferOrganized(t *testing.T) {
	root := t.TempDir()
	base := "VRChat_2026-06-30_20-30-00.000_1920x1080.png"
	loose := filepath.Join(root, "2026-06", base)
	_ = os.MkdirAll(filepath.Dir(loose), 0o755)
	_ = os.WriteFile(loose, []byte("png"), 0o644)
	orgDir := filepath.Join(root, organizedDir, "Summer Fest (2026-06-30)")
	_ = os.MkdirAll(orgDir, 0o755)
	_ = os.WriteFile(filepath.Join(orgDir, base), []byte("png"), 0o644)

	photos := ScanAll(root, newTL(), nil)
	if len(photos) != 1 {
		t.Fatalf("dedupe: got %d, want 1", len(photos))
	}
	if !photos[0].Organized {
		t.Error("dedupe should keep the organized copy")
	}
}

func TestProcessOrganizesByInstance(t *testing.T) {
	root := t.TempDir()
	// a screenshot in a YYYY-MM folder, named with a timestamp during the group instance
	monthDir := filepath.Join(root, "2026-06")
	_ = os.MkdirAll(monthDir, 0o755)
	photo := filepath.Join(monthDir, "VRChat_2026-06-30_22-30-00.000_1920x1080.png")
	if err := os.WriteFile(photo, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := New(root, newTL(), Copy, nil, nil)
	dest, ok, err := o.Process(photo)
	if err != nil || !ok {
		t.Fatalf("Process ok=%v err=%v", ok, err)
	}
	wantDir := filepath.Join(root, organizedDir, "Night Owls - Rave (2026-06-30)")
	if filepath.Dir(dest) != wantDir {
		t.Errorf("dest dir = %q, want %q", filepath.Dir(dest), wantDir)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("organized copy missing: %v", err)
	}
	// original still present (Copy mode) + idempotent
	if _, err := os.Stat(photo); err != nil {
		t.Error("Copy should leave the original")
	}
	if _, ok, _ := o.Process(photo); ok {
		t.Error("second Process should be a no-op (already seen)")
	}
}

func TestProcessSkipsUnknownLocation(t *testing.T) {
	root := t.TempDir()
	photo := filepath.Join(root, "VRChat_2020-01-01_00-00-00.000_1920x1080.png") // before any timeline entry
	_ = os.WriteFile(photo, []byte("png"), 0o644)
	o := New(root, newTL(), Copy, nil, nil)
	if _, ok, _ := o.Process(photo); ok {
		t.Error("unknown-location photo should be left alone")
	}
}
