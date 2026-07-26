package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The WP-3 device-preference fields are ADDITIVE: a config written before them must load unchanged
// and read as "automatic / negotiate", and an untouched feature must not write them back out.
func TestMediaLinkDevicePrefAdditive(t *testing.T) {
	var f MediaLinkFeature
	if err := json.Unmarshal([]byte(`{"shareVideo":true,"preferCodec":"hevc","maxFps":30}`), &f); err != nil {
		t.Fatal(err)
	}
	if !f.ShareVideo || f.PreferCodec != "hevc" || f.FPSCap() != 30 {
		t.Fatalf("legacy keys lost: %+v", f)
	}
	policy, adapter := f.DevicePref()
	if policy != "" || adapter != "" || f.PinnedEncoder() != "" {
		t.Fatalf("device pref must default to empty: %q %q %q", policy, adapter, f.PinnedEncoder())
	}
	out, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"devicePolicy", "encoderDevice", `"encoder"`} {
		if strings.Contains(string(out), k) {
			t.Fatalf("%s written for an unset preference: %s", k, out)
		}
	}
}

func TestMediaLinkDevicePrefRoundTrip(t *testing.T) {
	in := MediaLinkFeature{DevicePolicy: "pin", EncoderDevice: "0x00000000_0x0000c34f", Encoder: " h264_mf_native "}
	if p, a := in.DevicePref(); p != "pin" || a != "0x00000000_0x0000c34f" {
		t.Fatalf("DevicePref = %q %q", p, a)
	}
	if got := in.PinnedEncoder(); got != "h264_mf_native" {
		t.Fatalf("PinnedEncoder = %q (must be trimmed)", got)
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var back MediaLinkFeature
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != in {
		t.Fatalf("round trip: %+v != %+v", back, in)
	}
}

// The settings UI writes the isolation opt-in through ONE seam, and always explicitly: an opt-out
// must persist rather than fall back to the (now on-by-default) unset state.
func TestMediaLinkSetSubprocess(t *testing.T) {
	var f MediaLinkFeature
	if f.Subprocess != nil {
		t.Fatal("zero value must leave the tri-state unset")
	}
	f.SetSubprocess(false)
	if f.Subprocess == nil || f.MediaSubprocess() {
		t.Fatal("SetSubprocess(false) must write an explicit false")
	}
	f.SetSubprocess(true)
	if f.Subprocess == nil || !f.MediaSubprocess() {
		t.Fatal("SetSubprocess(true) must write an explicit true")
	}
}

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
