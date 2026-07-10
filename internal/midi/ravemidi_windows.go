//go:build windows

package midi

// ravemidi is rave-mate's OWN kernel-mode virtual MIDI driver (in development - see
// driver/ravemidi). Once installed it is the PREFERRED one-way-port backend; the
// teVirtualMIDI DLL (loopMIDI's driver) stays as the fallback so users without the
// ravemidi driver keep the feature. The control device is opened by absolute NT path.

import (
	"errors"
	"os"
)

// raveMIDICtlPath is the driver's control-device path (created by driver/ravemidi).
const raveMIDICtlPath = `\\.\RaveMidiCtl`

// errRaveMIDIUnavailable marks the ravemidi driver as not installed/running.
var errRaveMIDIUnavailable = errors.New("ravemidi driver not installed")

// raveMIDIAvailable reports whether the ravemidi control device exists (driver installed
// + running). Cheap open-probe; safe for UI gating.
func raveMIDIAvailable() bool {
	f, err := os.OpenFile(raveMIDICtlPath, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// openRaveMIDIOut creates a one-way (apps-see-input-only) port via the ravemidi driver.
// Placeholder until driver/ravemidi ships: the IOCTL protocol lands with the driver.
func openRaveMIDIOut(name string) (OutPort, error) {
	return nil, errRaveMIDIUnavailable
}
