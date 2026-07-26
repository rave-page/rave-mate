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

// The settings UI writes the isolation opt-in through one seam, so the field's representation can
// change without touching the card.
func TestMediaLinkSetSubprocess(t *testing.T) {
	var f MediaLinkFeature
	if f.MediaSubprocess() {
		t.Fatal("zero value must not opt in")
	}
	f.SetSubprocess(true)
	if !f.MediaSubprocess() {
		t.Fatal("SetSubprocess(true) not observed")
	}
	f.SetSubprocess(false)
	if f.MediaSubprocess() {
		t.Fatal("SetSubprocess(false) not observed")
	}
}
