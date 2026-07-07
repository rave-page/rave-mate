package vrclog

import (
	"testing"
	"time"
)

func TestParserPublicJoin(t *testing.T) {
	var p Parser
	if _, ok := p.Feed("2026.06.30 21:15:00 Log        -  [Behaviour] Entering Room: Club B"); ok {
		t.Fatal("Entering Room should not emit yet")
	}
	loc, ok := p.Feed("2026.06.30 21:15:02 Log        -  [Behaviour] Joining wrld_4432ea9b-729c-46e3-8eaf-846aa0a37fdd:67646~private(usr_x)~nonce(abc)")
	if !ok {
		t.Fatal("Joining wrld_ should emit")
	}
	if loc.WorldID != "wrld_4432ea9b-729c-46e3-8eaf-846aa0a37fdd" {
		t.Errorf("worldID = %q", loc.WorldID)
	}
	if loc.WorldName != "Club B" {
		t.Errorf("worldName = %q", loc.WorldName)
	}
	if loc.InstanceID == "" || loc.IsGroup() {
		t.Errorf("instance=%q group=%v (want non-empty, non-group)", loc.InstanceID, loc.IsGroup())
	}
	want := time.Date(2026, 6, 30, 21, 15, 2, 0, time.Local)
	if !loc.JoinedAt.Equal(want) {
		t.Errorf("joinedAt = %v, want %v", loc.JoinedAt, want)
	}
}

func TestParserGroupJoin(t *testing.T) {
	var p Parser
	p.Feed("2026.06.30 22:00:00 Log        -  [Behaviour] Entering Room: Rave World")
	loc, ok := p.Feed("2026.06.30 22:00:01 Log        -  [Behaviour] Joining wrld_abc:42~group(grp_1234abcd-0000-0000-0000-000000000000)~groupAccessType(members)")
	if !ok {
		t.Fatal("expected emit")
	}
	if !loc.IsGroup() || loc.GroupID != "grp_1234abcd-0000-0000-0000-000000000000" {
		t.Errorf("group parse failed: group=%v id=%q", loc.IsGroup(), loc.GroupID)
	}
}

func TestParserIgnoresNonLocationLines(t *testing.T) {
	var p Parser
	for _, ln := range []string{
		"2026.06.30 21:00:00 Log - [Behaviour] Joining or Creating Room: Club B",
		"2026.06.30 21:00:01 Log - [Behaviour] Joining friend: usr_xyz",
		"2026.06.30 21:00:02 Log - some unrelated line",
	} {
		if _, ok := p.Feed(ln); ok {
			t.Errorf("line should not emit a location: %q", ln)
		}
	}
}

func TestParserEnteringUpdatesNameOnly(t *testing.T) {
	var p Parser
	// two Entering lines before a Joining → the latest name wins
	p.Feed("2026.06.30 21:00:00 Log - [Behaviour] Entering Room: First")
	p.Feed("2026.06.30 21:00:01 Log - [Behaviour] Entering Room: Second")
	loc, ok := p.Feed("2026.06.30 21:00:02 Log - [Behaviour] Joining wrld_abcdef01-0000-0000-0000-000000000000:1")
	if !ok || loc.WorldName != "Second" {
		t.Errorf("name = %q, want Second", loc.WorldName)
	}
}
