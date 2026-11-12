//go:build windows

package featurehost

import "syscall"

// On Windows the default system timer resolution (~15.6 ms) makes Go's time.Timer / time.Sleep wake
// coarsely, so the PTS-paced jitter-buffer playout releases land late/bunched (the "occasional
// stutter"). timeBeginPeriod(1) raises the resolution to 1 ms for this process (the media child),
// tightening every Go timer incl. the playout wait. Restored on child exit.
var (
	winmm               = syscall.NewLazyDLL("winmm.dll")
	procTimeBeginPeriod = winmm.NewProc("timeBeginPeriod")
	procTimeEndPeriod   = winmm.NewProc("timeEndPeriod")
)

func raiseTimerResolution() func() {
	_, _, _ = procTimeBeginPeriod.Call(1)
	return func() { _, _, _ = procTimeEndPeriod.Call(1) }
}
