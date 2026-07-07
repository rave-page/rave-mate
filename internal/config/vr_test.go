package config

import "testing"

// Subprocess mode defaults ON for vr builds; InProc opts out; non-vr builds always in-proc.
func TestVRSubprocessEnabled(t *testing.T) {
	var f VROverlayFeature
	if !f.SubprocessEnabled(true) {
		t.Fatal("vr build default should be subprocess")
	}
	if f.SubprocessEnabled(false) {
		t.Fatal("non-vr build must stay in-proc")
	}
	f.InProc = true
	if f.SubprocessEnabled(true) {
		t.Fatal("InProc opt-out ignored")
	}
}
