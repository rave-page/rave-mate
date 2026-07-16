package matebridge

import (
	"encoding/json"
	"testing"
)

// TestAccessCodeHashVectors pins the FNV-1a 64 code-hash to the HOSTED_ACCESS_CONTRACT vectors.
// These are FIXED by the contract and asserted identically on the UdonSharp side - do NOT
// recompute-and-trust; a drift here silently breaks group selection across the boundary.
func TestAccessCodeHashVectors(t *testing.T) {
	vectors := map[string]string{
		"":              "cbf29ce484222325",
		"1234":          "1fabbdf10314a21d",
		"rave":          "6d98261fd1d47ac1",
		"DJ-Booth-2026": "7c7469bb7199a8f3",
	}
	for code, want := range vectors {
		if got := AccessCodeHash(code); got != want {
			t.Errorf("AccessCodeHash(%q) = %q, want %q", code, got, want)
		}
	}
}

// TestAccessModuleRoundTrip proves the access payload serializes to the contract shape (v/global/
// groups; rules keys; optional id/instances) and survives a round-trip.
func TestAccessModuleRoundTrip(t *testing.T) {
	m := AccessModule{
		V: AccessSchemaVersion,
		Global: AccessScope{
			Rules: AccessRules{InstanceOwner: true},
			Users: []string{"DisplayNameA", "DisplayNameB"},
		},
		Groups: []AccessGroup{{
			ID:       "grp_1234",
			Name:     "VIP",
			CodeHash: AccessCodeHash("rave"),
			Rules:    AccessRules{InstanceOwner: true, Master: true},
			Users:    []string{"DisplayNameX"},
		}},
	}
	blob, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	// codeHash carries the hash, never the plaintext code.
	if s := string(blob); containsPlain(s, "rave") && !containsPlain(s, "6d98261fd1d47ac1") {
		t.Fatalf("codeHash not the pinned hash: %s", s)
	}
	var got AccessModule
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	if got.V != 1 || !got.Global.Rules.InstanceOwner || len(got.Global.Users) != 2 {
		t.Fatalf("global lost: %+v", got.Global)
	}
	if len(got.Groups) != 1 || got.Groups[0].CodeHash != "6d98261fd1d47ac1" || !got.Groups[0].Rules.Master {
		t.Fatalf("group lost: %+v", got.Groups)
	}
}

func containsPlain(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
