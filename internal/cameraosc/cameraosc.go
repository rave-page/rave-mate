// Package cameraosc drives VRChat's in-game camera over OSC: the /usercamera/* live
// parameters (focal distance, aperture, colour grade, fly/turn/smoothing, dolly photo
// rate + duration) and applies named "look" presets - e.g. right after a dolly path loads.
//
// Scope (VRChat 2025.x): VRChat exposes camera LOOK over OSC, not camera MODE. There is NO
// OSC to switch the camera into Stream mode or toggle Spout (a standing VRChat feature
// request), and the dolly's Path Type / Easing / Looping / Capture / Streaming dropdowns are
// camera UI state VRChat does not persist in the path JSON - so neither we nor a manual
// re-import can restore them. Per-point look (focal/aperture/zoom/exposure/grade) DOES live
// in the path JSON; see vrccampaths for baking a preset into a path.
package cameraosc

import "rave.page/mate/internal/osc"

// Preset is a named set of /usercamera look values. A nil field is left untouched, so a
// preset can set only the params it cares about.
type Preset struct {
	Name          string   `json:"name"`
	FocalDistance *float64 `json:"focalDistance,omitempty"` // DOF focus distance (m)
	Aperture      *float64 `json:"aperture,omitempty"`      // DOF aperture (higher = more blur)
	Hue           *float64 `json:"hue,omitempty"`           // greenscreen grade
	Saturation    *float64 `json:"saturation,omitempty"`
	Lightness     *float64 `json:"lightness,omitempty"`
	FlySpeed      *float64 `json:"flySpeed,omitempty"`
	TurnSpeed     *float64 `json:"turnSpeed,omitempty"`
	Smoothing     *float64 `json:"smoothing,omitempty"` // SmoothingStrength
	PhotoRate     *float64 `json:"photoRate,omitempty"` // dolly photo capture rate
	Duration      *float64 `json:"duration,omitempty"`  // dolly duration (s)
}

// presetAddr maps each settable param to its /usercamera OSC address (read/write,
// confirmed for VRChat 2025.x). Exposure/Zoom are not reliable /usercamera addresses
// across versions, so those go via the path JSON (vrccampaths), not here.
var presetAddr = []struct {
	addr string
	get  func(Preset) *float64
}{
	{"/usercamera/FocalDistance", func(p Preset) *float64 { return p.FocalDistance }},
	{"/usercamera/Aperture", func(p Preset) *float64 { return p.Aperture }},
	{"/usercamera/Hue", func(p Preset) *float64 { return p.Hue }},
	{"/usercamera/Saturation", func(p Preset) *float64 { return p.Saturation }},
	{"/usercamera/Lightness", func(p Preset) *float64 { return p.Lightness }},
	{"/usercamera/FlySpeed", func(p Preset) *float64 { return p.FlySpeed }},
	{"/usercamera/TurnSpeed", func(p Preset) *float64 { return p.TurnSpeed }},
	{"/usercamera/SmoothingStrength", func(p Preset) *float64 { return p.Smoothing }},
	{"/usercamera/PhotoRate", func(p Preset) *float64 { return p.PhotoRate }},
	{"/usercamera/Duration", func(p Preset) *float64 { return p.Duration }},
}

// Apply pushes a preset's set values to VRChat's camera over OSC (empty addr → VRChat default).
func Apply(addr string, p Preset) error {
	c, err := osc.New(addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return apply(c, p)
}

// apply sends the set fields over an existing client (testable without a socket helper).
func apply(c *osc.Client, p Preset) error {
	for _, pa := range presetAddr {
		if v := pa.get(p); v != nil {
			if err := c.Send(pa.addr, float32(*v)); err != nil {
				return err
			}
		}
	}
	return nil
}

// f returns a pointer to v (preset-field literal helper).
func f(v float64) *float64 { return &v }

// BuiltinPresets are starting-point looks pairing with the shipped DJ-event dolly paths.
// Aperture is VRChat's slider (higher = shallower DOF / more bokeh).
func BuiltinPresets() []Preset {
	return []Preset{
		{Name: "Hero - shallow DOF", FocalDistance: f(2.5), Aperture: f(18), Smoothing: f(0.6)},
		{Name: "Crowd - deep focus", FocalDistance: f(8), Aperture: f(4), Smoothing: f(0.5)},
		{Name: "Telephoto compression", FocalDistance: f(6), Aperture: f(22), Smoothing: f(0.8)},
		{Name: "Wide establishing", FocalDistance: f(12), Aperture: f(2), Smoothing: f(0.4)},
		{Name: "Neutral / reset", FocalDistance: f(4), Aperture: f(0), Hue: f(0), Saturation: f(0), Lightness: f(0), Smoothing: f(0.5)},
	}
}

// PresetByName returns a builtin or user preset by name (second return false if absent).
func PresetByName(presets []Preset, name string) (Preset, bool) {
	for _, p := range presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}
