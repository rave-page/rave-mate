package vrccampaths

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/vrcloc"
)

// realPath is two keyframes copied verbatim from a real VRChat_CameraPath_*.json.
const realPath = `[
  {"IsLocal": false, "Position": {"X": 0.409314215, "Y": 1.79786825, "Z": -2.66283727},
   "Rotation": {"X": 15.7592516, "Y": 250.345871, "Z": 358.566284},
   "FocalDistance": 2.0, "Aperture": 15.0, "Hue": 120.0, "Saturation": 100.0, "Lightness": 50.0,
   "LookAtMeXOffset": 0.0, "LookAtMeYOffset": 0.0, "Zoom": 45.0000038, "Exposure": 0.0,
   "Speed": 0.4, "Duration": 2.0, "Index": 0, "PathIndex": 0},
  {"IsLocal": false, "Position": {"X": -0.645427942, "Y": 1.908314, "Z": -4.46229267},
   "Rotation": {"X": 40.3096237, "Y": 288.245941, "Z": 358.566284},
   "FocalDistance": 2.0, "Aperture": 15.0, "Hue": 120.0, "Saturation": 100.0, "Lightness": 50.0,
   "LookAtMeXOffset": 0.0, "LookAtMeYOffset": 0.0, "Zoom": 45.0000038, "Exposure": 0.0,
   "Speed": 0.4, "Duration": 2.0, "Index": 1, "PathIndex": 0}
]`

func TestLoadPointsRealFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "VRChat_CameraPath_x.json")
	if err := os.WriteFile(f, []byte(realPath), 0o644); err != nil {
		t.Fatal(err)
	}
	pts, err := LoadPoints(f)
	if err != nil {
		t.Fatalf("LoadPoints: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("points = %d, want 2", len(pts))
	}
	p := pts[0]
	if p.IsLocal {
		t.Error("IsLocal should be false")
	}
	if p.Position.X < 0.409 || p.Position.X > 0.410 {
		t.Errorf("Position.X = %v", p.Position.X)
	}
	if p.Rotation.Y < 250.3 || p.Rotation.Y > 250.4 {
		t.Errorf("Rotation.Y = %v", p.Rotation.Y)
	}
	if p.Zoom < 45 || p.Zoom > 45.001 {
		t.Errorf("Zoom = %v", p.Zoom)
	}
	if p.Duration != 2.0 || p.Speed != 0.4 {
		t.Errorf("Duration/Speed = %v/%v", p.Duration, p.Speed)
	}
	if pts[1].Index != 1 {
		t.Errorf("pts[1].Index = %d", pts[1].Index)
	}
}

func TestScanTagsWorldFromTimeline(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "VRChat_CameraPath_x.json")
	if err := os.WriteFile(f, []byte(realPath), 0o644); err != nil {
		t.Fatal(err)
	}
	saved := time.Now()
	_ = os.Chtimes(f, saved, saved)

	tl := vrcloc.NewTimeline("")
	tl.Record(vrcloc.Location{JoinedAt: saved.Add(-time.Hour), WorldID: "wrld_abc", WorldName: "Club B", InstanceID: "wrld_abc:1"})

	paths := Scan(dir, tl)
	if len(paths) != 1 {
		t.Fatalf("paths = %d, want 1", len(paths))
	}
	p := paths[0]
	if p.Points != 2 {
		t.Errorf("Points = %d, want 2", p.Points)
	}
	if p.DurationSec != 4.0 {
		t.Errorf("DurationSec = %v, want 4", p.DurationSec)
	}
	if p.WorldName != "Club B" || p.WorldID != "wrld_abc" {
		t.Errorf("world = %q/%q, want Club B/wrld_abc", p.WorldName, p.WorldID)
	}
}

func TestPlayerRelativeRouting(t *testing.T) {
	dir := t.TempDir()
	// a path whose every point is player-relative (IsLocal:true)
	local := `[{"IsLocal":true,"Position":{"X":0,"Y":1,"Z":0},"Rotation":{"X":0,"Y":0,"Z":0},"Duration":2.0,"Index":0,"PathIndex":0},
	           {"IsLocal":true,"Position":{"X":1,"Y":1,"Z":0},"Rotation":{"X":0,"Y":0,"Z":0},"Duration":2.0,"Index":1,"PathIndex":0}]`
	f := filepath.Join(dir, "VRChat_CameraPath_local.json")
	_ = os.WriteFile(f, []byte(local), 0o644)
	saved := time.Now()
	_ = os.Chtimes(f, saved, saved)
	// even with a known world, a player-relative path groups separately
	tl := vrcloc.NewTimeline("")
	tl.Record(vrcloc.Location{JoinedAt: saved.Add(-time.Hour), WorldID: "w", WorldName: "Club B", InstanceID: "w:1"})

	paths := Scan(dir, tl)
	if len(paths) != 1 || !paths[0].Local {
		t.Fatalf("expected 1 local path, got %+v", paths)
	}
	if paths[0].Folder() != PlayerRelativeFolder {
		t.Errorf("Folder = %q, want %q", paths[0].Folder(), PlayerRelativeFolder)
	}
	o := New(dir, tl, false, nil)
	o.Organize()
	if _, err := os.Stat(filepath.Join(dir, organizedDir, PlayerRelativeFolder, "VRChat_CameraPath_local.json")); err != nil {
		t.Errorf("player-relative path not organized into %q: %v", PlayerRelativeFolder, err)
	}
}

func TestWorldFolder(t *testing.T) {
	saved := time.Now()
	tl := vrcloc.NewTimeline("")
	tl.Record(vrcloc.Location{JoinedAt: saved.Add(-time.Minute), WorldID: "w", WorldName: "Rave: Room/1", InstanceID: "w:1"})
	if got := WorldFolder(saved, tl); got != "Rave_ Room_1" {
		t.Errorf("WorldFolder = %q (want sanitized)", got)
	}
	// unknown → "Unknown World"
	if got := WorldFolder(saved.Add(-2*time.Hour), tl); got != "Unknown World" {
		t.Errorf("unknown WorldFolder = %q", got)
	}
}

func TestOrganizeIntoPerWorld(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "VRChat_CameraPath_x.json")
	_ = os.WriteFile(f, []byte(realPath), 0o644)
	saved := time.Now()
	_ = os.Chtimes(f, saved, saved)
	tl := vrcloc.NewTimeline("")
	tl.Record(vrcloc.Location{JoinedAt: saved.Add(-time.Hour), WorldID: "wrld_abc", WorldName: "Club B", InstanceID: "wrld_abc:1"})

	o := New(dir, tl, false, nil) // copy
	if n := o.Organize(); n != 1 {
		t.Fatalf("organized = %d, want 1", n)
	}
	dest := filepath.Join(dir, organizedDir, "Club B", "VRChat_CameraPath_x.json")
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("organized file missing: %v", err)
	}
	if _, err := os.Stat(dest + ".meta.json"); err != nil {
		t.Errorf("sidecar missing: %v", err)
	}
}
