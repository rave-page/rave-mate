package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The media plane is isolated BY DEFAULT (#44 / WP-6). Tri-state semantics: key absent = on,
// explicit false = legacy in-proc, explicit true = on.
func TestMediaSubprocessDefaultsOn(t *testing.T) {
	if !(MediaLinkFeature{}).MediaSubprocess() {
		t.Error("zero-value config must run the media plane in the isolated child")
	}
	if !Default().Features.MediaLink.MediaSubprocess() {
		t.Error("Default() must run the media plane in the isolated child")
	}
	f := false
	if (MediaLinkFeature{Subprocess: &f}).MediaSubprocess() {
		t.Error("explicit false must keep the legacy in-proc plane")
	}
	tr := true
	if !(MediaLinkFeature{Subprocess: &tr}).MediaSubprocess() {
		t.Error("explicit true must stay on")
	}
}

// Migration proof: an OLD config file decides by what it literally contains. The pre-flip schema was
// `Subprocess bool` WITH omitempty, so a saved file could only ever carry `true` or nothing.
func TestMediaSubprocessLoadsOldConfigs(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"pre-flip file, never opted in (no key)", `{}`, true},
		{"pre-flip file, opted in", `{"subprocess":true}`, true},
		{"hand-edited opt-out", `{"subprocess":false}`, false},
		{"other keys only", `{"shareVideo":true,"maxFps":30}`, true},
	} {
		var f MediaLinkFeature
		if err := json.Unmarshal([]byte(tc.raw), &f); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := f.MediaSubprocess(); got != tc.want {
			t.Errorf("%s: MediaSubprocess()=%v, want %v", tc.name, got, tc.want)
		}
	}
	// A whole config file with no mediaLink section at all must still default to isolated.
	cfg := Default()
	if err := json.Unmarshal([]byte(`{"version":1,"features":{}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Features.MediaLink.MediaSubprocess() {
		t.Error("config without a mediaLink section must default to the isolated child")
	}
}

// false must SURVIVE a save/load round-trip (a plain bool + omitempty would drop it and silently
// re-enable the child); unset must stay absent so the default can move again later.
func TestMediaSubprocessRoundTrip(t *testing.T) {
	raw, err := json.Marshal(MediaLinkFeature{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "subprocess") {
		t.Errorf("unset must be omitted, got %s", raw)
	}
	f := false
	raw, err = json.Marshal(MediaLinkFeature{Subprocess: &f})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"subprocess":false`) {
		t.Fatalf("explicit false must persist, got %s", raw)
	}
	var back MediaLinkFeature
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.MediaSubprocess() {
		t.Error("explicit false must survive the round-trip")
	}
}
