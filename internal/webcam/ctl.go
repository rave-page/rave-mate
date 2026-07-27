package webcam

// Bus surface (media.cam.*, MEDIALINK_DESIGN.md §5): the instance that owns a camera broadcasts
// its Status; any paired instance renders it and sends a directed Cmd (list devices, start/stop,
// set PTZ/exposure). Mirrors the obscontrol bus pattern.

// Topics + the capability advertised by an instance with the webcam feature enabled.
const (
	TopicStatus = "media.cam.status" // broadcast: one instance's camera state (periodic while enabled)
	TopicCmd    = "media.cam.cmd"    // broadcast w/ Target: start/stop/set/refresh
	CapCam      = "media.cam"
)

// Command actions (Cmd.Action).
const (
	ActStart   = "cam.start"   // start capture (Device/W/H/FPS optional - fall back to config)
	ActStop    = "cam.stop"    // stop capture
	ActSet     = "cam.set"     // set one UVC property (Prop/Value/Auto)
	ActRefresh = "cam.refresh" // re-enumerate devices + re-read properties, then publish
)

// Mode is one capture format a device advertises (parsed from ffmpeg -list_options).
type Mode struct {
	W   int     `json:"w"`
	H   int     `json:"h"`
	FPS float64 `json:"fps"` // max fps at this size
}

// DeviceInfo is one enumerated video capture device.
type DeviceInfo struct {
	Name  string `json:"name"` // dshow friendly name (what ffmpeg's video="…" takes)
	Modes []Mode `json:"modes,omitempty"`
}

// PropState is one UVC property's range + current value (IAMCameraControl / IAMVideoProcAmp).
// Unsupported properties are omitted from Status.Props.
type PropState struct {
	ID      string `json:"id"`    // stable id ("zoom", "focus", "exposure", …; see propCatalog)
	Label   string `json:"label"` // human label
	Min     int32  `json:"min"`
	Max     int32  `json:"max"`
	Step    int32  `json:"step"`
	Default int32  `json:"default"`
	Value   int32  `json:"value"`
	Auto    bool   `json:"auto"`              // currently in auto mode
	CanAuto bool   `json:"canAuto,omitempty"` // device supports auto for this property
}

// Status is one instance's camera state (TopicStatus payload).
type Status struct {
	ID      string       `json:"id"`    // owning node id (Cmd.Target)
	Label   string       `json:"label"` // human label (hostname)
	Enabled bool         `json:"enabled"`
	Running bool         `json:"running"`
	Device  string       `json:"device,omitempty"` // active (or configured) device
	W       int          `json:"w,omitempty"`
	H       int          `json:"h,omitempty"`
	FPS     int          `json:"fps,omitempty"`
	Sender  string       `json:"sender,omitempty"` // Spout sender name while running
	Err     string       `json:"err,omitempty"`    // last error / disabled-reason string
	Devices []DeviceInfo `json:"devices,omitempty"`
	Props   []PropState  `json:"props,omitempty"` // for the active/configured device
	// Capture counters while running. Collected since the camera shipped and rendered NOWHERE - the
	// same blind spot the route panel had: a camera dropping most of its frames looked identical to
	// a healthy one. PoolMiss > 0 means the pixel pool was at its ceiling and frames were allocated.
	Frames   uint64 `json:"frames,omitempty"`
	Dropped  uint64 `json:"dropped,omitempty"`
	PoolMiss uint64 `json:"poolMiss,omitempty"`
}

// Cmd is a TopicCmd payload: an action targeted at one instance (Target = node id; "" = local).
type Cmd struct {
	Target string `json:"target,omitempty"`
	Action string `json:"action"`
	Device string `json:"device,omitempty"` // cam.start: device override
	W      int    `json:"w,omitempty"`      // cam.start: size/rate override
	H      int    `json:"h,omitempty"`
	FPS    int    `json:"fps,omitempty"`
	Prop   string `json:"prop,omitempty"` // cam.set: PropState.ID
	Value  int32  `json:"value,omitempty"`
	Auto   bool   `json:"auto,omitempty"` // cam.set: switch the property to auto
}

// Instance is a Status tagged with its publisher (local or a paired instance).
type Instance struct {
	Node  string `json:"node"`
	Local bool   `json:"local"`
	Status
}

// SenderName is the Spout sender name pattern for a device's stream.
func SenderName(device string) string { return "rave-mate cam " + device }
