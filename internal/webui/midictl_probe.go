package webui

// MIDI-tab OS-probe cache. The driver ioctls (DriverInstalled + QueryDriverInputs) and the
// winmm port enumeration (InputPorts/Ports) are OS syscalls: a driver open/close each, plus up
// to rmMaxInputs DeviceIoControl round-trips per query, plus a midiIn/Out DevCaps syscall per
// device. Running any of that PER CONFIGURED CONTROLLER on the render goroutine (actWorker) or
// on the ~1 Hz live tick froze the tab. This probes ONCE off-thread behind a short TTL; render +
// tick read the snapshot only and stay pure.

import (
	"slices"
	"sync"
	"time"

	"rave.page/mate/internal/midi"
)

const midiCtlProbeTTL = 1500 * time.Millisecond // driver ioctl + winmm enum: refresh at most ~1/1.5s

// midiCtlProbe caches the last MIDI OS-probe snapshot + its freshness state.
type midiCtlProbe struct {
	mu           sync.Mutex
	inPorts      []string                 // MIDISource.InputPorts() (winmm midiIn enum)
	outPorts     []string                 // MIDIEmit.Ports() (winmm midiOut enum)
	drvInstalled bool                     // midi.DriverInstalled() (kernel open/close)
	oneWay       bool                     // midi.OneWayAvailable()
	virtualAvail bool                     // midi.VirtualAvailable()
	drvInputs    []midi.DriverInputStatus // QueryDriverInputs() (up to rmMaxInputs ioctls); nil unless installed
	drvQueryErr  bool                     // installed but QueryDriverInputs errored (older driver build)
	at           time.Time
	ready        bool
	busy         bool // a background refresh is in flight (prevents stacking on the tick)
}

// midiCtlProbeData is an immutable snapshot handed to one render pass (no locking downstream).
type midiCtlProbeData struct {
	inPorts      []string
	outPorts     []string
	drvInstalled bool
	oneWay       bool
	virtualAvail bool
	drvInputs    []midi.DriverInputStatus
	drvQueryErr  bool
	ready        bool
}

// midiCtlProbeSnapshot returns the cached probe and kicks an off-thread refresh when stale.
// Non-blocking - safe on the render goroutine and the live tick.
func (u *UI) midiCtlProbeSnapshot() midiCtlProbeData {
	u.midiProbe.mu.Lock()
	stale := !u.midiProbe.ready || time.Since(u.midiProbe.at) > midiCtlProbeTTL
	kick := stale && !u.midiProbe.busy
	if kick {
		u.midiProbe.busy = true
	}
	d := midiCtlProbeData{
		inPorts:      u.midiProbe.inPorts,
		outPorts:     u.midiProbe.outPorts,
		drvInstalled: u.midiProbe.drvInstalled,
		oneWay:       u.midiProbe.oneWay,
		virtualAvail: u.midiProbe.virtualAvail,
		drvInputs:    u.midiProbe.drvInputs,
		drvQueryErr:  u.midiProbe.drvQueryErr,
		ready:        u.midiProbe.ready,
	}
	u.midiProbe.mu.Unlock()
	if kick {
		u.bg(u.refreshMidiCtlProbe)
	}
	return d
}

// refreshMidiCtlProbe re-runs the MIDI OS probes off the UI goroutine and caches them. Patches
// the MIDI tab once when the probe first lands or a STRUCTURAL field changes (port lists, driver
// presence, managed-input set/bind state) - deliberately NOT on a bare RetryCount tick, which
// would rebuild the whole tab every ~1.5s while a device is stuck retrying.
func (u *UI) refreshMidiCtlProbe() {
	var inPorts, outPorts []string
	if u.svc.MIDISource != nil {
		inPorts = u.svc.MIDISource.InputPorts()
	}
	if u.svc.MIDIEmit != nil {
		outPorts = u.svc.MIDIEmit.Ports()
	}
	drvInstalled := midi.DriverInstalled()
	virtualAvail := midi.VirtualAvailable()
	var drvInputs []midi.DriverInputStatus
	drvQueryErr := false
	if drvInstalled { // QueryDriverInputs only meaningful when the driver is present
		if sts, err := midi.QueryDriverInputs(); err == nil {
			drvInputs = sts
		} else {
			drvQueryErr = true // older driver build without the config plane
		}
	}

	u.midiProbe.mu.Lock()
	changed := !u.midiProbe.ready ||
		drvInstalled != u.midiProbe.drvInstalled ||
		virtualAvail != u.midiProbe.virtualAvail ||
		drvQueryErr != u.midiProbe.drvQueryErr ||
		!slices.Equal(u.midiProbe.inPorts, inPorts) ||
		!slices.Equal(u.midiProbe.outPorts, outPorts) ||
		midiDrvStructChanged(u.midiProbe.drvInputs, drvInputs)
	u.midiProbe.inPorts = inPorts
	u.midiProbe.outPorts = outPorts
	u.midiProbe.drvInstalled = drvInstalled
	u.midiProbe.oneWay = drvInstalled || virtualAvail // == midi.OneWayAvailable(), no extra probe
	u.midiProbe.virtualAvail = virtualAvail
	u.midiProbe.drvInputs = drvInputs
	u.midiProbe.drvQueryErr = drvQueryErr
	u.midiProbe.at = time.Now()
	u.midiProbe.ready = true
	u.midiProbe.busy = false
	u.midiProbe.mu.Unlock()

	if changed && !u.stopped() && u.activeTab() == "midictl" {
		u.patchMain()
	}
}

// midiDrvStructChanged reports a STRUCTURAL change in the managed-input set (count, id, name,
// bind/feedback, reserved port). Ignores RetryCount so a stuck-retrying device does not force a
// full-tab rebuild on every refresh (the count only renders on interaction-driven patches, as
// before this cache existed).
func midiDrvStructChanged(a, b []midi.DriverInputStatus) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Name != b[i].Name ||
			a[i].Bound != b[i].Bound || a[i].FeedbackBound != b[i].FeedbackBound ||
			a[i].ReservedPortID != b[i].ReservedPortID {
			return true
		}
	}
	return false
}
