package selfupdate

import (
	"os"
	"strconv"
	"time"
)

// RelaunchCooldownEnv hands a relaunched process a boot pause (seconds). A GPU-fault relaunch
// that immediately re-runs the identical failing GL/VR init just re-crashes into the recovery
// loop; the pause gives the display driver time to settle. Consumed once via TakeRelaunchCooldown.
const RelaunchCooldownEnv = "RAVE_RELAUNCH_COOLDOWN"

// relaunchCooldownMax bounds the honored cooldown - a bogus env value must not become a sleep bomb.
const relaunchCooldownMax = 60 * time.Second

// Relaunch launches the (possibly freshly-swapped) binary detached from this dying process; the
// caller must exit promptly. Per-OS mechanics in relaunch_windows.go / relaunch_other.go.
func Relaunch() error { return relaunch(nil) }

// RelaunchWithCooldown is Relaunch plus RelaunchCooldownEnv, so the fresh instance paces its boot
// (GPU-fault recovery path).
func RelaunchWithCooldown(d time.Duration) error {
	return relaunch([]string{RelaunchCooldownEnv + "=" + strconv.Itoa(int(d/time.Second))})
}

// TakeRelaunchCooldown reads + clears the boot cooldown handed to this process; 0 when absent or
// invalid, capped at relaunchCooldownMax.
func TakeRelaunchCooldown() time.Duration {
	v := os.Getenv(RelaunchCooldownEnv)
	_ = os.Unsetenv(RelaunchCooldownEnv) // one-shot; never leaks to feature subprocesses
	s, err := strconv.Atoi(v)
	if err != nil || s <= 0 {
		return 0
	}
	if d := time.Duration(s) * time.Second; d <= relaunchCooldownMax {
		return d
	}
	return relaunchCooldownMax
}
