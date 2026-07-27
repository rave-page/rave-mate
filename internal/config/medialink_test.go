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

// ── zigmedia increment 5: zero-copy capture is the DEFAULT ──

// TestZeroCopyCaptureDefaultsOn pins the flip and, more importantly, its MIGRATION semantics. The
// key is omitempty on a *bool, so a config written before the flip carries either `true` (someone
// opted in) or no key at all - nobody can be silently pinned to the readback by a stale config,
// which is the same argument MediaSubprocess records for its own flip.
func TestZeroCopyCaptureDefaultsOn(t *testing.T) {
	t.Setenv("RAVE_MATE_ZIGMEDIA_CAPTURE", "")
	var zero MediaLinkFeature
	if !zero.ZeroCopyCapture() {
		t.Error("a zero-valued MediaLinkFeature must default to zero-copy capture")
	}
	// A pre-flip config that never mentioned the key.
	var old MediaLinkFeature
	if err := json.Unmarshal([]byte(`{"shareVideo":true,"maxFps":60}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.ZigCapture != nil {
		t.Fatalf("the absent key materialised as %v", *old.ZigCapture)
	}
	if !old.ZeroCopyCapture() {
		t.Error("a config written before the flip must pick up the new default")
	}
	// An EXPLICIT opt-out must survive it - that is the whole point of the tri-state.
	var off MediaLinkFeature
	if err := json.Unmarshal([]byte(`{"zigCapture":false}`), &off); err != nil {
		t.Fatal(err)
	}
	if off.ZeroCopyCapture() {
		t.Error("an explicit zigCapture:false was overridden by the new default")
	}
	// The env override still wins both ways (soak + gates depend on it).
	t.Setenv("RAVE_MATE_ZIGMEDIA_CAPTURE", "0")
	if zero.ZeroCopyCapture() {
		t.Error("RAVE_MATE_ZIGMEDIA_CAPTURE=0 did not turn the default off")
	}
	t.Setenv("RAVE_MATE_ZIGMEDIA_CAPTURE", "1")
	if !off.ZeroCopyCapture() {
		t.Error("RAVE_MATE_ZIGMEDIA_CAPTURE=1 did not override an explicit false")
	}
}

// TestZeroCopyOptOutRoundTrips: SetZeroCopyCapture(false) must PERSIST, or the opt-out evaporates on
// the next save and the user silently gets the default back. omitempty on a *bool keeps `false`
// (the pointer is non-nil), which is exactly why the field is a pointer.
func TestZeroCopyOptOutRoundTrips(t *testing.T) {
	t.Setenv("RAVE_MATE_ZIGMEDIA_CAPTURE", "")
	var f MediaLinkFeature
	f.SetZeroCopyCapture(false)
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"zigCapture":false`) {
		t.Fatalf("the opt-out was not persisted: %s", b)
	}
	var back MediaLinkFeature
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ZeroCopyCapture() {
		t.Error("the opt-out did not survive a save/load cycle")
	}
}

// TestZeroCopyDecodeDefaultsOn: the receive side flipped too. Its justification is a MEASUREMENT
// rather than a soak - on the field rig the ffmpeg frame path republished ~13.5 distinct frames/s at
// 4K while the source encoded at 37, because the CPU SendImage upload of 33 MB/frame is the capacity
// ceiling. Leaving the old default in place preserved a measured 3x frame loss, so "off" was not the
// safe choice it looked like.
func TestZeroCopyDecodeDefaultsOn(t *testing.T) {
	t.Setenv("RAVE_MATE_ZIGMEDIA_DECODE", "")
	var f MediaLinkFeature
	if !f.ZeroCopyDecode() {
		t.Error("native GPU-resident decode must default ON")
	}
	var off MediaLinkFeature
	if err := json.Unmarshal([]byte(`{"zigDecode":false}`), &off); err != nil {
		t.Fatal(err)
	}
	if off.ZeroCopyDecode() {
		t.Error("an explicit zigDecode:false was overridden by the new default")
	}
	t.Setenv("RAVE_MATE_ZIGMEDIA_DECODE", "0")
	if f.ZeroCopyDecode() {
		t.Error("RAVE_MATE_ZIGMEDIA_DECODE=0 did not turn the default off - that is the escape hatch")
	}
}

// TestAffinityStaysOff records the one deliberate ASYMMETRY of the increment-5 flip: capture and
// decode are promoted, adapter-affinity is not, because its gate cannot be satisfied on the hardware
// that verified the rest - the re-place is live-verified only between two IDENTICAL GPUs, so a
// heterogeneous iGPU+dGPU rig (where the re-placed adapter may have a much worse encoder or none) is
// unexercised. Leaving it off costs a VISIBLE downgrade to a working readback, not a ceiling.
// A future agent flipping it should have to delete this test and say why.
func TestAffinityStaysOff(t *testing.T) {
	t.Setenv("RAVE_MATE_ZIGMEDIA_AFFINITY", "")
	var f MediaLinkFeature
	if f.ZeroCopyAffinity() {
		t.Error("adapter affinity is default ON: the re-place is live-verified only between two IDENTICAL GPUs")
	}
}
