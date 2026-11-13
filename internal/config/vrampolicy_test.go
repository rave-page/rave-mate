package config

import "testing"

func TestSetRememberedGeometry(t *testing.T) {
	var m MediaLinkFeature
	m.RememberReceive("sus", "spout:VRC", "")
	if !m.SetRememberedGeometry("sus", "spout:VRC", "VRC", 3840, 2160, 60) {
		t.Fatal("first geometry must persist")
	}
	if m.SetRememberedGeometry("sus", "spout:VRC", "VRC", 3840, 2160, 60) {
		t.Fatal("unchanged geometry must not re-persist")
	}
	if m.SetRememberedGeometry("sus", "spout:VRC", "VRC", 0, 0, 60) {
		t.Fatal("unknown geometry must not overwrite the nominal one")
	}
	if m.SetRememberedGeometry("other", "spout:VRC", "VRC", 1, 1, 1) {
		t.Fatal("not remembered: no change")
	}
	r := m.RememberedReceives[0]
	if r.W != 3840 || r.H != 2160 || r.FPS != 60 || r.Name != "VRC" {
		t.Fatalf("stored %+v", r)
	}
}

func TestVramPolicyAndFallbackDefaults(t *testing.T) {
	t.Setenv("RAVE_MATE_VRAM_POLICY", "")
	t.Setenv("RAVE_MATE_CPU_DECODE_FALLBACK", "")
	var m MediaLinkFeature
	if m.VramPolicyName() != "" || !m.CPUDecodeFallbackEnabled() {
		t.Fatal("defaults: policy unset (=> balanced), CPU fallback ON")
	}
	off := false
	m.CPUDecodeFallback = &off
	m.VramPolicy = "strict"
	if m.VramPolicyName() != "strict" || m.CPUDecodeFallbackEnabled() {
		t.Fatal("explicit config must win")
	}
	t.Setenv("RAVE_MATE_VRAM_POLICY", "permissive")
	if m.VramPolicyName() != "permissive" {
		t.Fatal("env must win")
	}
}
