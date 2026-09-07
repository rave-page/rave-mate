package app

import (
	"testing"
	"time"
)

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

// The disconnect-linger keeps the child alive for mediaPlaneLinger after the last peer drops, so a
// brief flap does not tear down every republish sender.
func TestMediaPlaneDemandLinger(t *testing.T) {
	now := time.Now()
	linger := 2 * time.Minute
	recent := now.Add(-30 * time.Second)
	old := now.Add(-3 * time.Minute)
	for _, tc := range []struct {
		name                             string
		webcamOn, peersOn, peerConnected bool
		lastConnected                    time.Time
		want                             bool
	}{
		{"webcam on ignores linger", true, false, false, time.Time{}, true},
		{"connected", false, true, true, old, true},
		{"disconnected within linger", false, true, false, recent, true},
		{"disconnected past linger", false, true, false, old, false},
		{"peers disabled ignores lastConnected", false, false, false, recent, false},
		{"never connected (zero) no linger", false, true, false, time.Time{}, false},
	} {
		if got := mediaPlaneDemandLinger(tc.webcamOn, tc.peersOn, tc.peerConnected,
			tc.lastConnected, now, linger); got != tc.want {
			t.Errorf("%s: = %v, want %v", tc.name, got, tc.want)
		}
	}
}
