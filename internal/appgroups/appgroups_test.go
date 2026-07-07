package appgroups

import (
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/config"
)

// fakeActivity feeds a fixed running-process set (no OS calls).
type fakeActivity struct {
	set map[string]bool
	ok  bool
}

func (f fakeActivity) IdleDuration() (time.Duration, bool)       { return 0, false }
func (f fakeActivity) RunningProcesses() (map[string]bool, bool) { return f.set, f.ok }

func newSvc(act fakeActivity, groups []config.AppGroup) (*Service, *[]string) {
	var launched []string
	s := &Service{
		groups: func() []config.AppGroup { return groups },
		act:    act,
		start: func(path string, _ []string, _ string, _ bool) error {
			launched = append(launched, filepath.Base(path))
			return nil
		},
	}
	return s, &launched
}

func TestLaunchGroupSkipsRunning(t *testing.T) {
	groups := []config.AppGroup{{
		ID:   "rig",
		Name: "DJ rig",
		Apps: []config.AppRef{
			{Path: `C:\SteamVR\vrserver.exe`, MatchName: "vrserver.exe"},
			{Path: `C:\VRChat\VRChat.exe`}, // MatchName empty → basename "VRChat.exe"
			{Path: `C:\Parsec\parsecd.exe`},
		},
	}}
	// vrserver already running (both forms present as sysactivity stores them); VRChat + Parsec not.
	act := fakeActivity{ok: true, set: map[string]bool{"vrserver.exe": true, "vrserver": true}}
	s, launched := newSvc(act, groups)

	started, skipped, err := s.LaunchGroup("rig")
	if err != nil {
		t.Fatalf("LaunchGroup err: %v", err)
	}
	if len(*launched) != 2 {
		t.Fatalf("launched %v, want 2 (VRChat + parsecd)", *launched)
	}
	if len(started) != 2 || len(skipped) != 1 {
		t.Fatalf("started=%v skipped=%v; want 2 started, 1 skipped", started, skipped)
	}
	if skipped[0] != "vrserver.exe" {
		t.Errorf("skipped %q, want vrserver.exe", skipped[0])
	}
}

func TestLaunchGroupFailOpenNoDetection(t *testing.T) {
	// ok=false (non-Windows / detection unavailable) → nil set → launch everything.
	groups := []config.AppGroup{{ID: "rig", Apps: []config.AppRef{
		{Path: `/a.exe`}, {Path: `/b.exe`},
	}}}
	s, launched := newSvc(fakeActivity{ok: false, set: nil}, groups)
	started, _, err := s.LaunchGroup("rig")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(started) != 2 || len(*launched) != 2 {
		t.Fatalf("started=%v launched=%v; want both launched (fail open)", started, *launched)
	}
}

func TestLaunchGroupUnknownID(t *testing.T) {
	s, _ := newSvc(fakeActivity{ok: true, set: map[string]bool{}}, nil)
	if _, _, err := s.LaunchGroup("nope"); err == nil {
		t.Fatal("expected error for unknown group id")
	}
}

func TestLaunchGroupNoDuplicateWithinGroup(t *testing.T) {
	// Same app listed twice → launched once (marked running after first start).
	groups := []config.AppGroup{{ID: "rig", Apps: []config.AppRef{
		{Path: `C:\app\foo.exe`}, {Path: `C:\app\foo.exe`},
	}}}
	s, launched := newSvc(fakeActivity{ok: true, set: map[string]bool{}}, groups)
	started, _, _ := s.LaunchGroup("rig")
	if len(*launched) != 1 || len(started) != 1 {
		t.Fatalf("launched=%v started=%v; want single launch", *launched, started)
	}
}

func TestRunningCount(t *testing.T) {
	g := config.AppGroup{Apps: []config.AppRef{
		{Path: `C:\a\vrchat.exe`},
		{Path: `C:\a\obs.exe`},
		{Path: ``}, // blank ignored
	}}
	s, _ := newSvc(fakeActivity{ok: true, set: map[string]bool{"vrchat.exe": true, "vrchat": true}}, nil)
	running, total := s.RunningCount(g)
	if running != 1 || total != 2 {
		t.Fatalf("running=%d total=%d; want 1/2", running, total)
	}
}
