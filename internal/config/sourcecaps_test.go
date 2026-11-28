package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An existing config file (remembered receives, no caps) loads unchanged through Load: caps read 0
// (global settings apply) and re-saving does not write cap keys.
func TestSourceCapsOldConfigLoads(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", dir)
	c := Default()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	// Splice a pre-caps mediaLink section into an otherwise current file (raw JSON, as written then).
	var top map[string]json.RawMessage
	var feats map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil || json.Unmarshal(top["features"], &feats) != nil {
		t.Fatal("fixture: default config JSON shape")
	}
	feats["mediaLink"] = json.RawMessage(`{"bitrateKbps":30000,"rememberedReceives":[{"peer":"n1","sourceId":"spout:VRC","name":"VRC","w":3840,"h":2160,"fps":60}]}`)
	top["features"], _ = json.Marshal(feats)
	old, _ := json.Marshal(top)
	if err := os.WriteFile(filepath.Join(dir, fileName), old, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ml := got.Features.MediaLink
	if len(ml.RememberedReceives) != 1 || ml.RememberedReceives[0].W != 3840 || ml.BitrateKbps != 30000 {
		t.Fatalf("old config not loaded unchanged: %+v", ml)
	}
	if k, f := ml.SourceCaps("n1", "spout:VRC"); k != 0 || f != 0 {
		t.Fatalf("old receive must have no caps, got %d/%d", k, f)
	}
	out, _ := json.Marshal(ml)
	if strings.Contains(string(out), "maxKbps") || strings.Contains(string(out), `"maxFps"`) {
		t.Fatalf("unset caps written back: %s", out)
	}
}

func TestSourceCapsRoundTrip(t *testing.T) {
	var m MediaLinkFeature
	if ch, found := m.SetSourceCaps("n1", "spout:cam", 8000, 30); ch || found {
		t.Fatal("caps on an unremembered source must not add it")
	}
	m.RememberReceive("n1", "spout:cam", "cam")
	m.RememberReceive("n1", "spout:4k", "4k")
	if ch, found := m.SetSourceCaps("n1", "spout:cam", 8000, 30); !ch || !found {
		t.Fatal("first set must change")
	}
	if ch, _ := m.SetSourceCaps("n1", "spout:cam", 8000, 30); ch {
		t.Fatal("same caps must be idempotent")
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back MediaLinkFeature
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if k, f := back.SourceCaps("n1", "spout:cam"); k != 8000 || f != 30 {
		t.Fatalf("round trip caps = %d/%d", k, f)
	}
	if k, f := back.SourceCaps("n1", "spout:4k"); k != 0 || f != 0 {
		t.Fatalf("uncapped source got %d/%d", k, f)
	}
	if ch, _ := back.SetSourceCaps("n1", "spout:cam", 0, 0); !ch {
		t.Fatal("clear must change")
	}
	if k, f := back.SourceCaps("n1", "spout:cam"); k != 0 || f != 0 {
		t.Fatalf("cleared caps = %d/%d", k, f)
	}
}

func TestValidSourceCaps(t *testing.T) {
	for _, tc := range []struct {
		k, f int
		ok   bool
	}{{0, 0, true}, {1000, 1, true}, {80000, 240, true}, {999, 0, false}, {80001, 0, false}, {0, 241, false}, {0, -1, false}} {
		if got := ValidSourceCaps(tc.k, tc.f); got != tc.ok {
			t.Errorf("ValidSourceCaps(%d,%d)=%v want %v", tc.k, tc.f, got, tc.ok)
		}
	}
}
