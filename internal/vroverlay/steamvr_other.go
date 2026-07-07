//go:build !windows

package vroverlay

// steamvrRunning can't cheaply probe vrserver off Windows; assume up (the VR host is Windows). On
// Linux/macOS VR_Init may still start SteamVR - acceptable for those rare hosts.
func steamvrRunning() bool { return true }
