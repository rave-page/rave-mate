package winshot

import "testing"

func TestMatchVRView(t *testing.T) {
	match := []string{
		"VR View",
		"vr view",
		"SteamVR",
		"SteamVR Status",
		"Headset Window",
		"OpenVR",
		"  VR View  ",
		"Compositor (OpenVR)",
	}
	for _, s := range match {
		if !MatchVRView(s) {
			t.Errorf("MatchVRView(%q) = false, want true", s)
		}
	}
	noMatch := []string{
		"",
		"   ",
		"Google Chrome",
		"rave-mate",
		"Discord",
		"VRChat", // the game window is not the SteamVR mirror
	}
	for _, s := range noMatch {
		if MatchVRView(s) {
			t.Errorf("MatchVRView(%q) = true, want false", s)
		}
	}
}
