package midi

import "sync/atomic"

// Managed-input config types for the ravemidi driver (portable; the Windows wire
// codec lives in ravemidi_config_windows.go).

// driverSyncErr holds the last SetDriverConfig outcome ("" = ok) so both sync
// paths (app boot + webui apply) surface the same persistent UI hint.
var driverSyncErr atomic.Value

// DriverSyncErr returns the last driver config-sync failure ("" = last sync ok).
func DriverSyncErr() string {
	if v, ok := driverSyncErr.Load().(string); ok {
		return v
	}
	return ""
}

// Filter bits (RAVEMIDI_FILTER_*): message classes dropped on the fan-out path.
const (
	FilterChanPressure = 0x01 // Dn keybed aftertouch
	FilterPolyPressure = 0x02 // An polyphonic aftertouch
	FilterPitchBend    = 0x04 // En pitch bend
	FilterActiveSense  = 0x08 // FE active sensing
	FilterClock        = 0x10 // F8/F9 timing tick
)

// DriverSentinel is the config/UI THRU value selecting driver-managed forwarding:
// the ravemidi driver binds the hardware itself (forwarding survives rave-mate exit
// + reboot); rave-mate reads the reserved per-input port instead of the device.
const DriverSentinel = "@rave-mate:ravemidi-managed"

// ReservedPortName is the driver's per-input rave-mate port (managed.cpp naming).
// Protocol v3 makes it INTERNAL: hidden from every app's MIDI list (incl. winmm
// enumeration here) — read via IOCTL (ravemidi_reader_windows.go), never midiInOpen.
func ReservedPortName(name string) string { return name + " (rave-mate)" }

// DJPortName is the driver fan-out port DJ software should select.
func DJPortName(name string) string { return name + " THRU" }

// FilterKeys maps config filter keys to RAVEMIDI_FILTER_* bits (UI chip order).
var FilterKeys = []struct {
	Key string
	Bit uint32
}{
	{"aftertouch", FilterChanPressure},
	{"poly", FilterPolyPressure},
	{"bend", FilterPitchBend},
	{"sense", FilterActiveSense},
	{"clock", FilterClock},
}

// DefaultDriverFilter drops mapping-hostile clutter (aftertouch/sense/clock);
// pitch bend + poly pressure pass (tempo sliders / pad pressure may be mapped).
func DefaultDriverFilter() []string { return []string{"aftertouch", "sense", "clock"} }

// FilterMask folds config filter keys into the driver mask.
func FilterMask(keys []string) uint32 {
	var m uint32
	for _, k := range keys {
		for _, f := range FilterKeys {
			if f.Key == k {
				m |= f.Bit
			}
		}
	}
	return m
}

// ManagedInput describes one driver-managed controller (config-decoupled input
// to ManagedCfgs; shared by the app's boot sync + the webui's apply sync).
type ManagedInput struct {
	Name        string   // stable id + port base name
	SourceMatch string   // hardware device FriendlyName substring (config Port)
	Filter      []string // FilterKeys keys; nil = DefaultDriverFilter(), empty = none
}

// ManagedCfgs maps managed inputs to driver input configs (thru+feedback on,
// one DJ-facing fan-out per input, capped at the driver's 8-input limit).
func ManagedCfgs(ins []ManagedInput) []DriverInputCfg {
	out := make([]DriverInputCfg, 0, len(ins))
	for _, m := range ins {
		fl := m.Filter
		if fl == nil {
			fl = DefaultDriverFilter()
		}
		out = append(out, DriverInputCfg{
			ID: m.Name, Name: m.Name, SourceMatch: m.SourceMatch,
			Thru: true, Feedback: true, Filter: FilterMask(fl),
			OutNames: []string{DJPortName(m.Name)},
		})
		if len(out) == 8 { // RAVEMIDI_MAX_INPUTS
			break
		}
	}
	return out
}

// DriverInputCfg mirrors RAVEMIDI_INPUT_CFG (protocol v3).
type DriverInputCfg struct {
	ID          string
	Name        string
	SourceMatch string // substring vs device FriendlyName
	SourceIface string // exact KS symlink ("" = use SourceMatch)
	Thru        bool   // device capture → out ports
	Feedback    bool   // app render on reserved/fan-out ports → device render pin
	Filter      uint32 // Filter* mask, fan-outs only (reserved port unfiltered)
	OutNames    []string
}

// TraceEntry mirrors RAVEMIDI_TRACE_ENTRY (per-port wire-diagnosis ring).
type TraceEntry struct {
	Seq       uint64
	Time100ns uint64
	Dir       uint32 // TraceDir*
	Len       uint32 // full event length (Bytes holds the first ≤12)
	Bytes     []byte
}

// Trace directions (RAVEMIDI_TRACE_DIR).
const (
	TraceDirTapRaw      = 0 // raw KS read off the tapped device (pre-parse)
	TraceDirToApp       = 1 // pushed toward the app capture pin
	TraceDirReadPop     = 2 // handed to portcls via miniport Read
	TraceDirFromApp     = 3 // app wrote on the render pin
	TraceDirFeedbackOut = 4 // framed message written to the device render pin
	TraceDirLoopDrop    = 5 // loopback self-echo suppressed
)

// DriverInputStatus mirrors RAVEMIDI_INPUT_STATUS.
type DriverInputStatus struct {
	ID, Name       string
	Bound          bool
	FeedbackBound  bool
	RetryCount     uint32
	BoundIface     string
	ReservedPortID uint32
	OutPortIDs     []uint32
}
