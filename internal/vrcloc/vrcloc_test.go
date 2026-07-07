package vrcloc

import (
	"path/filepath"
	"testing"
	"time"
)

func tm(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func TestTimelineAtAndCurrent(t *testing.T) {
	tl := NewTimeline("")
	tl.Record(Location{JoinedAt: tm("2026-06-30T20:00:00Z"), WorldID: "w1", WorldName: "Club A", InstanceID: "w1:1"})
	tl.Record(Location{JoinedAt: tm("2026-06-30T21:00:00Z"), WorldID: "w2", WorldName: "Club B", InstanceID: "w2:1"})

	// before first join → unknown
	if _, ok := tl.At(tm("2026-06-30T19:00:00Z")); ok {
		t.Error("expected unknown before first join")
	}
	// during first instance
	if loc, ok := tl.At(tm("2026-06-30T20:30:00Z")); !ok || loc.WorldID != "w1" {
		t.Errorf("At 20:30 = %+v ok=%v, want w1", loc, ok)
	}
	// exactly at second join
	if loc, ok := tl.At(tm("2026-06-30T21:00:00Z")); !ok || loc.WorldID != "w2" {
		t.Errorf("At 21:00 = %+v, want w2", loc)
	}
	if loc, ok := tl.Current(); !ok || loc.WorldID != "w2" {
		t.Errorf("Current = %+v, want w2", loc)
	}
}

func TestTimelineDedupSameInstance(t *testing.T) {
	tl := NewTimeline("")
	tl.Record(Location{JoinedAt: tm("2026-06-30T20:00:00Z"), WorldID: "w1", InstanceID: "w1:1"})
	tl.Record(Location{JoinedAt: tm("2026-06-30T20:05:00Z"), WorldID: "w1", InstanceID: "w1:1"}) // same → ignored
	if got := len(tl.Entries()); got != 1 {
		t.Errorf("entries = %d, want 1 (dedup same instance)", got)
	}
}

func TestTimelineIgnoresZeroTimeAndEmpty(t *testing.T) {
	tl := NewTimeline("")
	tl.Record(Location{WorldID: "w1", InstanceID: "w1:1"})    // zero JoinedAt → ignored
	tl.Record(Location{JoinedAt: tm("2026-06-30T20:00:00Z")}) // empty world+instance → ignored
	if got := len(tl.Entries()); got != 0 {
		t.Errorf("entries = %d, want 0", got)
	}
}

func TestTimelinePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tl.json")
	tl := NewTimeline(path)
	tl.Record(Location{JoinedAt: tm("2026-06-30T20:00:00Z"), WorldID: "w1", WorldName: "Club A", InstanceID: "w1:1"})
	// reload
	tl2 := NewTimeline(path)
	if loc, ok := tl2.Current(); !ok || loc.WorldName != "Club A" {
		t.Errorf("reloaded Current = %+v ok=%v, want Club A", loc, ok)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{`Club: A/B`, "Club_ A_B"},
		{`bad<>:"/\|?*name`, "bad_________name"},
		{"   ", "FB"},
		{"trailing.. ", "trailing"},
		{"con", "con_"},
		{"normal name", "normal name"},
	}
	for _, c := range cases {
		if got := SanitizeName(c.in, "FB"); got != c.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInstanceDirName(t *testing.T) {
	pub := Location{WorldName: "Club B"}
	if got := InstanceDirName(pub, "2026-06-30"); got != "Club B (2026-06-30)" {
		t.Errorf("public = %q", got)
	}
	grp := Location{WorldName: "Club B", GroupID: "grp_1", GroupName: "Night Owls"}
	if got := InstanceDirName(grp, "2026-06-30"); got != "Night Owls - Club B (2026-06-30)" {
		t.Errorf("group = %q", got)
	}
	// illegal chars in names get sanitized
	bad := Location{WorldName: "A/B", GroupID: "g", GroupName: "X:Y"}
	if got := InstanceDirName(bad, "2026-06-30"); got != "X_Y - A_B (2026-06-30)" {
		t.Errorf("sanitized = %q", got)
	}
}
