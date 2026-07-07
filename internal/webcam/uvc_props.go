package webcam

// UVC property catalog: the DirectShow properties we surface, split across the two control
// interfaces. Indices are the KSPROPERTY_CAMERACONTROL_* / VideoProcAmp_* enum values. Pure -
// clamp/lookup logic is unit-tested; the COM calls live in uvc_windows.go.

// control interface selector
type uvcIface uint8

const (
	ifaceCamCtl  uvcIface = iota // IAMCameraControl
	ifaceProcAmp                 // IAMVideoProcAmp
)

// KS flags (identical values on both interfaces).
const (
	uvcFlagAuto   int32 = 0x1
	uvcFlagManual int32 = 0x2
)

// propDef is one catalog entry.
type propDef struct {
	ID    string
	Label string
	Iface uvcIface
	Index int32
}

// propCatalog is the surfaced property set, in UI order. Devices lacking a property simply omit
// it from Status.Props (graceful no-op).
var propCatalog = []propDef{
	{"pan", "Pan", ifaceCamCtl, 0},
	{"tilt", "Tilt", ifaceCamCtl, 1},
	{"zoom", "Zoom", ifaceCamCtl, 3},
	{"focus", "Focus", ifaceCamCtl, 6},
	{"exposure", "Exposure", ifaceCamCtl, 4},
	{"brightness", "Brightness", ifaceProcAmp, 0},
	{"contrast", "Contrast", ifaceProcAmp, 1},
	{"saturation", "Saturation", ifaceProcAmp, 3},
	{"whiteBalance", "White balance", ifaceProcAmp, 7},
	{"gain", "Gain", ifaceProcAmp, 9},
}

// propByID looks a catalog entry up by its stable id.
func propByID(id string) (propDef, bool) {
	for _, p := range propCatalog {
		if p.ID == id {
			return p, true
		}
	}
	return propDef{}, false
}

// clampProp snaps v onto the device's grid: clamped to [min,max], then aligned to the nearest
// step from min (drivers reject off-step values). Degenerate ranges/steps pass v through clamped.
func clampProp(v, min, max, step int32) int32 {
	if min > max {
		return v
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	if step <= 1 {
		return v
	}
	off := v - min
	snapped := min + (off+step/2)/step*step
	if snapped > max {
		snapped -= step
	}
	if snapped < min {
		snapped = min
	}
	return snapped
}
