package app

import "testing"

// WP-7: the media child spawns on DEMAND (webcam, or peers feature + a connected peer) -
// never just because the peers feature is enabled on an idle rig.
func TestMediaPlaneDemand(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		webcamOn, peersOn, peerLinkedUp bool
		want                            bool
	}{
		{"all off", false, false, false, false},
		{"peers enabled, nobody connected - the old always-spawn case", false, true, false, false},
		{"peers enabled + peer connected", false, true, true, true},
		{"webcam alone", true, false, false, true},
		{"connection without the peers feature (stale state) stays off", false, false, true, false},
		{"webcam + peers connected", true, true, true, true},
	} {
		if got := mediaPlaneDemand(tc.webcamOn, tc.peersOn, tc.peerLinkedUp); got != tc.want {
			t.Errorf("%s: mediaPlaneDemand(%v,%v,%v) = %v, want %v",
				tc.name, tc.webcamOn, tc.peersOn, tc.peerLinkedUp, got, tc.want)
		}
	}
}
