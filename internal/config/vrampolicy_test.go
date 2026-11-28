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

// Sender keeper: default ON (nil), explicit false opts out, env overrides both ways.
func TestSenderKeeperDefaultOn(t *testing.T) {
	t.Setenv("RAVE_MATE_SENDER_KEEPER", "")
	var m MediaLinkFeature
	if !m.SenderKeeperEnabled() {
		t.Fatal("unset senderKeeper must be ON")
	}
	off := false
	m.SenderKeeper = &off
	if m.SenderKeeperEnabled() {
		t.Fatal("explicit false must opt out")
	}
	t.Setenv("RAVE_MATE_SENDER_KEEPER", "1")
	if !m.SenderKeeperEnabled() {
		t.Fatal("env 1 must win over config false")
	}
	m.SenderKeeper = nil
	t.Setenv("RAVE_MATE_SENDER_KEEPER", "0")
	if m.SenderKeeperEnabled() {
		t.Fatal("env 0 must win over the default")
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
