package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/encoderscan"
)

func TestEncodeDeviceCurrentMapping(t *testing.T) {
	for _, tc := range []struct {
		policy, adapter, want string
	}{
		{"", "", ""},
		{"auto", "0xdead_0xbeef", ""}, // auto ignores a stale adapter
		{"avoid-busiest", "", deviceAvoidValue},
		{"pin", "0x00000000_0x0000c34f", "0x00000000_0x0000c34f"},
		{"junk", "0x1_0x2", ""},
	} {
		mf := &config.MediaLinkFeature{DevicePolicy: tc.policy, EncoderDevice: tc.adapter}
		if got := encodeDeviceCurrent(mf); got != tc.want {
			t.Errorf("policy %q adapter %q → %q, want %q", tc.policy, tc.adapter, got, tc.want)
		}
	}
}

func TestApplyEncodeDevice(t *testing.T) {
	var mf config.MediaLinkFeature
	applyEncodeDevice(&mf, "0x00000000_0x0000c34f")
	if p, a := mf.DevicePref(); encoderscan.NormalizePolicy(p) != encoderscan.PolicyPin || a != "0x00000000_0x0000c34f" {
		t.Fatalf("pin: %q %q", p, a)
	}
	applyEncodeDevice(&mf, deviceAvoidValue)
	if p, a := mf.DevicePref(); encoderscan.NormalizePolicy(p) != encoderscan.PolicyAvoid || a != "" {
		t.Fatalf("avoid must clear the adapter: %q %q", p, a)
	}
	applyEncodeDevice(&mf, "")
	if p, a := mf.DevicePref(); p != "" || a != "" {
		t.Fatalf("auto must clear both: %q %q", p, a)
	}
	// Round trip through the picker's current-value mapping.
	for _, v := range []string{"", deviceAvoidValue, "0x00000000_0x0000c34f"} {
		applyEncodeDevice(&mf, v)
		if got := encodeDeviceCurrent(&mf); got != v {
			t.Errorf("round trip %q → %q", v, got)
		}
	}
}

// The picker always offers automatic + avoid-busiest, and keeps a pinned GPU that is no longer
// present as an option so the setting never silently reads as "automatic".
func TestEncodeDeviceOptions(t *testing.T) {
	u := probeUI("settings")
	u.probes.mu.Lock()
	u.probes.gpus = []gpuAdapterRow{
		{LUID: "0x00000000_0x0000c34f", Label: "NVIDIA GeForce RTX 4090 · enc 62% · OBS"},
		{LUID: "0x00000000_0x0000d112", Label: "Intel(R) UHD Graphics 770"},
	}
	u.probes.mu.Unlock()

	mf := &config.MediaLinkFeature{}
	opts := u.encodeDeviceOptions(mf)
	if len(opts) != 4 || opts[0][0] != "" || opts[1][0] != deviceAvoidValue {
		t.Fatalf("options = %v", opts)
	}
	if !strings.Contains(opts[2][1], "enc 62%") || !strings.Contains(opts[2][1], "OBS") {
		t.Fatalf("adapter label lost its load/holder: %q", opts[2][1])
	}
	mf.DevicePolicy, mf.EncoderDevice = "pin", "0x00000000_0x0000ffff" // GPU removed
	opts = u.encodeDeviceOptions(mf)
	if len(opts) != 5 || opts[4][0] != "0x00000000_0x0000ffff" {
		t.Fatalf("absent pin dropped: %v", opts)
	}
	// A present pin must NOT be duplicated.
	mf.EncoderDevice = "0x00000000_0x0000d112"
	if opts = u.encodeDeviceOptions(mf); len(opts) != 4 {
		t.Fatalf("present pin duplicated: %v", opts)
	}
}

// The card body must carry both groups, each picker, and the isolation toggle - and the sender/
// receiver split notes, because a knob set on the wrong PC silently does nothing.
func TestMediaLinkBlocks(t *testing.T) {
	u := probeUI("settings")
	mf := &config.MediaLinkFeature{}
	blocks := u.mediaLinkBlocks(mf)
	// Selects carry their act as the smart-select id (":" → "-"); toggles carry it verbatim.
	var acts []string
	notes := 0
	for _, b := range blocks {
		if b.Sel != nil {
			acts = append(acts, b.Sel.ID)
		}
		if b.Tgl != nil {
			acts = append(acts, b.Tgl.Act)
		}
		if b.K == "note" {
			notes++
		}
		for _, k := range b.Kids {
			if k.Sel != nil {
				acts = append(acts, k.Sel.ID)
			}
		}
	}
	joined := strings.Join(acts, ",")
	for _, want := range []string{"set-ml-device", "set-ml-encoder", "set-ml-maxfps", "set-ml-maxheight",
		"set:ml-swonly", "set-ml-codec", "set-ml-bitrate", "set:ml-subprocess"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s missing from the card (acts: %s)", want, joined)
		}
	}
	if notes < 3 {
		t.Errorf("notes = %d, want >=3 (sender group, receiver group, card note)", notes)
	}
	// Sender knobs must come before the receiver group - the card is read top-down.
	if strings.Index(joined, "set-ml-maxfps") > strings.Index(joined, "set-ml-codec") {
		t.Errorf("sender knobs must precede the receiver group: %s", joined)
	}
}
