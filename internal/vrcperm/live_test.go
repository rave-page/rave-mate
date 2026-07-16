package vrcperm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/matebridge"
)

// fakeSeq is an in-memory SeqCounter for tests.
type fakeSeq struct{ m map[string]int64 }

func newFakeSeq() *fakeSeq             { return &fakeSeq{m: map[string]int64{}} }
func (f *fakeSeq) Next(k string) int64 { f.m[k]++; return f.m[k] }
func (f *fakeSeq) Peek(k string) int64 { return f.m[k] }

// TestPublishRosterDiffOnlyAndSeq proves an editor roster publishes flat allow.txt/allow.json, reuses
// one gist per name, advances seq ONLY on a real write, and returns the world-facing raw URLs.
func TestPublishRosterDiffOnlyAndSeq(t *testing.T) {
	gists := &fakeGists{}
	f := &config.WorldSyncFeature{Enabled: true}
	s, saves := newTestService(f, gists, &fakeMembers{})
	s.seq = newFakeSeq()

	id, raw, jsonURL, seq, err := s.PublishRoster(context.Background(), "perm", "Warehouse 9 lineup", []string{"DJ Nyx", "VJ Kilo"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if id != "g1" || seq != 1 {
		t.Fatalf("first publish id=%q seq=%d", id, seq)
	}
	if raw != "https://gist.githubusercontent.com/octo/g1/raw/allow.txt" {
		t.Fatalf("rawURL = %q", raw)
	}
	if jsonURL != "https://gist.githubusercontent.com/octo/g1/raw/allow.json" {
		t.Fatalf("jsonURL = %q", jsonURL)
	}
	if got := gists.lastFiles[FileNames]; got != "DJ Nyx\nVJ Kilo\n" {
		t.Fatalf("flat names = %q", got)
	}
	if f.RosterGists["warehouse-9-lineup"] != "g1" || *saves == 0 {
		t.Fatalf("roster gist not persisted: %+v saves=%d", f.RosterGists, *saves)
	}

	// Unchanged roster (same name, same members) => no write, seq stays.
	_, _, _, seq2, err := s.PublishRoster(context.Background(), "perm", "Warehouse 9 lineup", []string{"VJ Kilo", "DJ Nyx"})
	if err != nil {
		t.Fatal(err)
	}
	if gists.creates != 1 || gists.updates != 0 {
		t.Fatalf("diff-only violated: creates=%d updates=%d", gists.creates, gists.updates)
	}
	if seq2 != 1 {
		t.Fatalf("seq advanced on unchanged roster: %d", seq2)
	}

	// Changed roster => update same gist, seq advances.
	_, _, _, seq3, err := s.PublishRoster(context.Background(), "perm", "Warehouse 9 lineup", []string{"DJ Nyx"})
	if err != nil {
		t.Fatal(err)
	}
	if gists.updates != 1 || seq3 != 2 {
		t.Fatalf("changed roster: updates=%d seq=%d", gists.updates, seq3)
	}
}

// TestPublishRosterRejectsUnknownKind proves only kind=perm is accepted in v1.
func TestPublishRosterRejectsUnknownKind(t *testing.T) {
	s, _ := newTestService(&config.WorldSyncFeature{Enabled: true}, &fakeGists{}, &fakeMembers{})
	s.seq = newFakeSeq()
	if _, _, _, _, err := s.PublishRoster(context.Background(), "bogus", "x", []string{"a"}); err == nil {
		t.Fatal("want error for unknown roster kind")
	}
}

// TestPublishPointerEnvelope proves the pointer gist is an enveloped single-module gist carrying the
// SEQ-GATE keys + instanceOwnerName, diff-only on the inner payload.
func TestPublishPointerEnvelope(t *testing.T) {
	gists := &fakeGists{}
	f := &config.WorldSyncFeature{Enabled: true, PointerOn: true}
	s, _ := newTestService(f, gists, &fakeMembers{})
	s.seq = newFakeSeq()

	p := matebridge.PointerModule{
		Default:           "main",
		InstanceOwnerName: "DJ Nyx",
		ByOperator:        []matebridge.OperatorRef{{Operator: "DJ Nyx", Profile: "main", Priority: 10}},
	}
	s.PublishPointer(context.Background(), p)
	if gists.creates != 1 || f.PointerGistID != "g1" {
		t.Fatalf("pointer not published: creates=%d id=%q err=%q", gists.creates, f.PointerGistID, s.Status("pointer").Err)
	}
	blob := gists.lastFiles[matebridge.ModulePointer+".json"]
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &top); err != nil {
		t.Fatalf("pointer gist not valid JSON: %v", err)
	}
	for _, k := range []string{"schema", "contractVersion", "seq", "updatedAt", matebridge.ModulePointer} {
		if _, ok := top[k]; !ok {
			t.Fatalf("pointer envelope missing %q: %s", k, blob)
		}
	}
	if !strings.Contains(blob, "DJ Nyx") {
		t.Fatalf("instanceOwnerName not stamped: %s", blob)
	}

	// Republish identical pointer => diff-only skip (seq must not advance forever from the changing seq/updatedAt).
	s.PublishPointer(context.Background(), p)
	if gists.updates != 0 || gists.creates != 1 {
		t.Fatalf("pointer diff-only violated: creates=%d updates=%d", gists.creates, gists.updates)
	}
	if !s.Status("pointer").Skipped {
		t.Fatal("want Skipped on identical pointer")
	}
}

// TestMaybePublishPointerGatedOffByFlag proves the refresher skips the pointer unless PointerOn.
func TestMaybePublishPointerGatedOffByFlag(t *testing.T) {
	gists := &fakeGists{}
	f := &config.WorldSyncFeature{Enabled: true, PointerOn: false}
	s, _ := newTestService(f, gists, &fakeMembers{})
	s.seq = newFakeSeq()
	s.SetPointerProvider(func() (matebridge.PointerModule, bool) {
		return matebridge.PointerModule{InstanceOwnerName: "X"}, true
	})
	s.maybePublishPointer(context.Background())
	if gists.creates != 0 {
		t.Fatalf("pointer published while flag off: creates=%d", gists.creates)
	}
	f.PointerOn = true
	s.maybePublishPointer(context.Background())
	if gists.creates != 1 {
		t.Fatalf("pointer not published when enabled: creates=%d", gists.creates)
	}
}
