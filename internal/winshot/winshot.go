// Package winshot captures an OS window by title to a PNG - used to grab the SteamVR "VR View"
// (headset-mirror) window so rave-mate can screenshot what the headset sees. Pure stdlib syscall
// (user32/gdi32 via PrintWindow) on Windows, mirroring internal/sysactivity; a stub elsewhere.
// The title-matching logic lives here (build-tag-free) so it is unit-testable on any platform.
package winshot

import "strings"

// vrViewTitleHints are lowercase substrings the SteamVR VR-View / headset-mirror window matches.
// "vr view" is the current title; older/runtime variants used "headset window" / "openvr"; bare
// "steamvr" is the weak catch-all (also matches other SteamVR windows).
var vrViewTitleHints = []string{"vr view", "headset window", "openvr", "steamvr"}

// MatchVRView reports whether a window title looks like the SteamVR VR-View / headset-mirror window.
func MatchVRView(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return false
	}
	for _, h := range vrViewTitleHints {
		if strings.Contains(t, h) {
			return true
		}
	}
	return false
}
