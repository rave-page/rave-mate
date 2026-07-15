package vrcperm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/matebridge"
)

// sampleAccessCfg is 1 non-group global entry + 2 groups (codes "1234" and "rave"), used by the
// compose + publish tests. Group members overlap the global entry to exercise union dedup.
func sampleAccessCfg() *config.WorldSyncFeature {
	return &config.WorldSyncFeature{
		Enabled:     true,
		AccessOn:    true,
		AccessRules: config.AccessRulesConfig{InstanceOwner: true},
		AccessUsers: []string{"HostDJ"},
		AccessGroups: []config.AccessGroupConfig{
			{
				ID:    "grp_crew",
				Name:  "Crew",
				Code:  "1234",
				Rules: config.AccessRulesConfig{InstanceOwner: true, Master: true},
				Users: []string{"VJ Kilo", "DJ Nyx", "HostDJ"}, // HostDJ overlaps global
			},
			{
				Name:  "VIP",
				Code:  "rave",
				Rules: config.AccessRulesConfig{Everyone: true},
				Users: []string{"Guest One"},
			},
		},
	}
}

// TestBuildAccessModuleComposesGlobalUnion proves global is auto-composed (base rules + deduped/
// sorted union of non-group + every group's users), per-group codes hash to the pinned vectors, and
// empty instances are omitted.
func TestBuildAccessModuleComposesGlobalUnion(t *testing.T) {
	m := buildAccessModule(sampleAccessCfg())

	if m.V != 1 {
		t.Fatalf("v = %d", m.V)
	}
	// Global rules = the authored base rules (NOT unioned from groups).
	if !m.Global.Rules.InstanceOwner || m.Global.Rules.Master || m.Global.Rules.Everyone {
		t.Fatalf("global rules = %+v", m.Global.Rules)
	}
	// Global users = union of {HostDJ} + crew + VIP, deduped + sorted (HostDJ appears once).
	wantGlobal := []string{"DJ Nyx", "Guest One", "HostDJ", "VJ Kilo"}
	if strings.Join(m.Global.Users, "|") != strings.Join(wantGlobal, "|") {
		t.Fatalf("global users = %v, want %v", m.Global.Users, wantGlobal)
	}
	if len(m.Groups) != 2 {
		t.Fatalf("groups = %d", len(m.Groups))
	}
	// codeHash matches the contract's pinned FNV-1a vectors; plaintext code never present.
	if m.Groups[0].CodeHash != "1fabbdf10314a21d" { // "1234"
		t.Fatalf("crew codeHash = %q", m.Groups[0].CodeHash)
	}
	if m.Groups[1].CodeHash != "6d98261fd1d47ac1" { // "rave"
		t.Fatalf("vip codeHash = %q", m.Groups[1].CodeHash)
	}
	// Group users sorted + deduped; instances omitted when empty.
	if strings.Join(m.Groups[0].Users, "|") != "DJ Nyx|HostDJ|VJ Kilo" {
		t.Fatalf("crew users = %v", m.Groups[0].Users)
	}
	if m.Groups[1].Instances != nil {
		t.Fatalf("empty instances should be nil/omitted: %v", m.Groups[1].Instances)
	}
	// No plaintext secret code leaks into the payload.
	blob, _ := json.Marshal(m)
	for _, code := range []string{`"1234"`, `"rave"`} {
		if strings.Contains(string(blob), code) {
			t.Fatalf("plaintext code %s leaked into payload: %s", code, blob)
		}
	}
}

// TestPublishAccessDirectEnvelope proves direct mode writes an enveloped single-module gist under
// the `access` key carrying the SEQ-GATE keys + the {v,global,groups} payload, diff-only.
func TestPublishAccessDirectEnvelope(t *testing.T) {
	gists := &fakeGists{}
	f := sampleAccessCfg()
	s, saves := newTestService(f, gists, &fakeMembers{})
	s.seq = newFakeSeq()

	s.PublishAccess(context.Background())
	if gists.creates != 1 || f.AccessGistID != "g1" || *saves == 0 {
		t.Fatalf("access not published: creates=%d id=%q saves=%d err=%q", gists.creates, f.AccessGistID, *saves, s.Status("access").Err)
	}
	blob := gists.lastFiles[matebridge.ModuleAccess+".json"]
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &top); err != nil {
		t.Fatalf("access gist not valid JSON: %v", err)
	}
	for _, k := range []string{"schema", "contractVersion", "seq", "updatedAt", matebridge.ModuleAccess} {
		if _, ok := top[k]; !ok {
			t.Fatalf("access envelope missing %q: %s", k, blob)
		}
	}
	var inner matebridge.AccessModule
	if err := json.Unmarshal(top[matebridge.ModuleAccess], &inner); err != nil {
		t.Fatalf("inner access payload undecodable: %v", err)
	}
	if inner.V != 1 || len(inner.Global.Users) != 4 || len(inner.Groups) != 2 {
		t.Fatalf("inner payload lost fields: %+v", inner)
	}
	// Republish identical => diff-only skip (seq must not advance forever from changing seq/updatedAt).
	s.PublishAccess(context.Background())
	if gists.updates != 0 || gists.creates != 1 {
		t.Fatalf("access diff-only violated: creates=%d updates=%d", gists.creates, gists.updates)
	}
	if !s.Status("access").Skipped {
		t.Fatal("want Skipped on identical access")
	}
}

// TestPublishAccessHostedRaw proves hosted mode PUTs the RAW {v,global,groups} payload (no local
// envelope/seq) under the "access" module key, persisting the server-returned pointer.
func TestPublishAccessHostedRaw(t *testing.T) {
	f := sampleAccessCfg()
	f.PublishMode = config.WorldSyncModeHosted
	f.HostedWorldID = "wrld_x"
	s, _ := newTestService(f, &fakeGists{}, &fakeMembers{})
	h := &fakeHosted{ready: true}
	s.hosted = h

	s.PublishAccess(context.Background())
	if h.calls != 1 || h.lastModule != matebridge.ModuleAccess {
		t.Fatalf("hosted access: calls=%d module=%q err=%q", h.calls, h.lastModule, s.Status("access").Err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(h.lastPayload, &top); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	for _, k := range []string{"v", "global", "groups"} {
		if _, ok := top[k]; !ok {
			t.Fatalf("hosted raw payload missing %q: %s", k, h.lastPayload)
		}
	}
	for _, k := range []string{"schema", "seq", "contractVersion", "updatedAt"} {
		if _, ok := top[k]; ok {
			t.Fatalf("hosted payload must NOT be enveloped, found %q", k)
		}
	}
	if f.LiveModules["access"].RawURL == "" || f.LiveModules["access"].Seq != 1 {
		t.Fatalf("LiveModules[access] = %+v", f.LiveModules["access"])
	}
}
