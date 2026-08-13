// Package videoedit models the Editor tab's video mode: a persisted project
// (source, aspect reframe, pan keyframes, effect chain, export target) and the
// pure ffmpeg filter builders that realize it. No process spawning here - the
// transcode/vfx workers run the args.
package videoedit

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"rave.page/mate/internal/transcode"
)

// SchemaVersion is bumped on breaking Project shape changes.
const SchemaVersion = 1

// Aspect is a reframe target ratio. W/H 0 = keep source ("orig").
type Aspect struct {
	Key  string
	W, H int
}

// Aspects are the selectable reframe targets, UI order.
var Aspects = []Aspect{
	{Key: "orig"},
	{Key: "9x16", W: 9, H: 16},
	{Key: "4x5", W: 4, H: 5},
	{Key: "1x1", W: 1, H: 1},
	{Key: "16x9", W: 16, H: 9},
}

// AspectByKey returns the aspect for key ("orig" fallback).
func AspectByKey(key string) Aspect {
	for _, a := range Aspects {
		if a.Key == key {
			return a
		}
	}
	return Aspects[0]
}

// PanKey is one crop-window keyframe: T = source-media seconds, X = normalized
// window position along the free axis (0 = left/top edge, 1 = right/bottom).
// Y is the cross-axis offset from center (-0.5..0.5, 0 = centered) - only
// meaningful when Zoom > 1 opens slack on the second axis.
type PanKey struct {
	T float64 `json:"t"`
	X float64 `json:"x"`
	Y float64 `json:"y,omitempty"`
}

// Zoom bounds: 1 = the maximal aspect window, 4 = tightest punch-in.
const (
	ZoomMin = 1.0
	ZoomMax = 4.0
)

// EffectInst is one entry of the effect chain (hosted by the vfx worker).
type EffectInst struct {
	Kind   string             `json:"kind"` // frei0r|isf
	Ref    string             `json:"ref"`  // plugin/shader file base name
	Off    bool               `json:"off,omitempty"`
	Params map[string]float64 `json:"params,omitempty"`
	Blend  string             `json:"blend,omitempty"` // blend mode over the stage input ("" = normal/replace)
	Mix    *float64           `json:"mix,omitempty"`   // stage opacity 0..1; nil = 1 (fully applied)
}

// Clone deep-copies a project: undo snapshots must not alias the live keyframe /
// effect slices or a param map (an in-place param edit would rewrite history).
func (p Project) Clone() Project {
	out := p
	if p.PanKF != nil {
		out.PanKF = append([]PanKey(nil), p.PanKF...)
	}
	if p.Effects != nil {
		out.Effects = make([]EffectInst, len(p.Effects))
		for i, e := range p.Effects {
			c := e
			if e.Params != nil {
				c.Params = make(map[string]float64, len(e.Params))
				for k, v := range e.Params {
					c.Params[k] = v
				}
			}
			if e.Mix != nil {
				m := *e.Mix
				c.Mix = &m
			}
			out.Effects[i] = c
		}
	}
	return out
}

// MixOr resolves the stage opacity (nil = fully applied), clamped to 0..1.
func (e EffectInst) MixOr() float64 {
	if e.Mix == nil {
		return 1
	}
	if *e.Mix < 0 {
		return 0
	}
	if *e.Mix > 1 {
		return 1
	}
	return *e.Mix
}

// Project is the persisted video-mode state.
type Project struct {
	Schema    int          `json:"schema"`
	Source    string       `json:"source,omitempty"`
	Aspect    string       `json:"aspect,omitempty"` // Aspects key; ""/orig = no reframe
	Layout    string       `json:"layout,omitempty"` // crop = zoom-fill (default); fit = original inside + styled background fill
	BGBlur    float64      `json:"bgBlur"`           // fit layout: background gaussian blur 0..1 (0 = sharp)
	Pan       float64      `json:"pan"`              // static window position (used without keyframes)
	Pan2      float64      `json:"pan2,omitempty"`   // static cross-axis offset from center, -0.5..0.5 (zoomed)
	Zoom      float64      `json:"zoom,omitempty"`   // crop zoom ≥1 (0/absent = 1 = maximal window)
	PanKF     []PanKey     `json:"panKf,omitempty"`
	Effects   []EffectInst `json:"effects,omitempty"`
	PresetKey string       `json:"presetKey,omitempty"` // ExportPresets key
	OutPath   string       `json:"outPath,omitempty"`
}

// Normalize folds defaults + clamps into the project.
func (p *Project) Normalize() {
	p.Schema = SchemaVersion
	if p.Aspect == "" {
		p.Aspect = "orig"
	}
	if p.Layout != "fit" {
		p.Layout = "crop"
	}
	p.BGBlur = clamp01(p.BGBlur)
	if p.Pan < 0 || p.Pan > 1 {
		p.Pan = 0.5
	}
	p.Pan2 = clampOff(p.Pan2)
	if p.Zoom < ZoomMin { // also folds absent (0) to 1
		p.Zoom = ZoomMin
	}
	if p.Zoom > ZoomMax {
		p.Zoom = ZoomMax
	}
	if p.PresetKey == "" {
		p.PresetKey = "reel"
	}
	for i := range p.PanKF {
		p.PanKF[i].X = clamp01(p.PanKF[i].X)
		p.PanKF[i].Y = clampOff(p.PanKF[i].Y)
		if p.PanKF[i].T < 0 {
			p.PanKF[i].T = 0
		}
	}
	sort.SliceStable(p.PanKF, func(i, j int) bool { return p.PanKF[i].T < p.PanKF[j].T })
}

// Marshal serializes the project (stable, indented for diffable autosaves).
func (p Project) Marshal() ([]byte, error) { return json.MarshalIndent(p, "", "  ") }

// Unmarshal parses + normalizes a project.
func Unmarshal(data []byte) (Project, error) {
	var p Project
	if err := json.Unmarshal(data, &p); err != nil {
		return Project{}, err
	}
	p.Normalize()
	return p, nil
}

// ── crop geometry ──

// CropSize returns the crop window for reframing srcW×srcH to aspect (even
// dimensions, h264-safe) + the free pan axis ("x", "y" or "" when the source
// already matches). Zero on unusable input.
func CropSize(srcW, srcH int, a Aspect) (cw, ch int, freeAxis string) {
	if srcW <= 0 || srcH <= 0 || a.W <= 0 || a.H <= 0 {
		return 0, 0, ""
	}
	target := float64(a.W) / float64(a.H)
	src := float64(srcW) / float64(srcH)
	switch {
	case src > target+1e-9: // wider than target: full height, pan horizontally
		cw, ch, freeAxis = int(float64(srcH)*target), srcH, "x"
	case src < target-1e-9: // taller: full width, pan vertically
		cw, ch, freeAxis = srcW, int(float64(srcW)/target), "y"
	default:
		return srcW - srcW%2, srcH - srcH%2, ""
	}
	cw -= cw % 2
	ch -= ch % 2
	if cw < 2 || ch < 2 {
		return 0, 0, ""
	}
	return cw, ch, freeAxis
}

// CropSizeZoom is CropSize shrunk by zoom (≥1) on both axes - zoom > 1 opens
// pan slack on the second axis too. Unlike CropSize it also handles the
// "orig"/matching-aspect case (zoom pans within the source aspect); freeAxis
// stays the aspect-mismatch axis ("" when none).
func CropSizeZoom(srcW, srcH int, a Aspect, zoom float64) (cw, ch int, freeAxis string) {
	if zoom < ZoomMin {
		zoom = ZoomMin
	}
	if zoom > ZoomMax {
		zoom = ZoomMax
	}
	var bw, bh int
	var axis string
	if a.W > 0 && a.H > 0 {
		bw, bh, axis = CropSize(srcW, srcH, a)
		if bw == 0 {
			return 0, 0, ""
		}
	} else {
		if srcW < 2 || srcH < 2 {
			return 0, 0, ""
		}
		bw, bh = srcW-srcW%2, srcH-srcH%2
	}
	if zoom > 1 {
		bw = int(float64(bw) / zoom)
		bh = int(float64(bh) / zoom)
		bw -= bw % 2
		bh -= bh % 2
		if bw < 2 || bh < 2 {
			return 0, 0, ""
		}
	}
	return bw, bh, axis
}

// PanAt evaluates the window position at source time t: static pan without
// keyframes, clamped piecewise-linear between them (numeric twin of panExpr).
func (p Project) PanAt(t float64) float64 {
	keys := p.PanKF
	if len(keys) == 0 {
		return clamp01(p.Pan)
	}
	if t < keys[0].T {
		return clamp01(keys[0].X)
	}
	for i := 0; i+1 < len(keys); i++ {
		a, b := keys[i], keys[i+1]
		if t < b.T {
			if b.T <= a.T {
				return clamp01(b.X)
			}
			f := (t - a.T) / (b.T - a.T)
			return clamp01(a.X + (b.X-a.X)*f)
		}
	}
	return clamp01(keys[len(keys)-1].X)
}

// crossKeys maps keyframes onto the cross axis (position = 0.5 + Y offset).
func crossKeys(keys []PanKey) []PanKey {
	out := make([]PanKey, len(keys))
	for i, k := range keys {
		out[i] = PanKey{T: k.T, X: clamp01(0.5 + k.Y)}
	}
	return out
}

// Pan2At evaluates the cross-axis window position (0..1, 0.5 = centered) at t.
func (p Project) Pan2At(t float64) float64 {
	if len(p.PanKF) == 0 {
		return clamp01(0.5 + p.Pan2)
	}
	return Project{Pan: clamp01(0.5 + p.Pan2), PanKF: crossKeys(p.PanKF)}.PanAt(t)
}

// panExpr builds the crop-offset ffmpeg expression along the free axis.
// maxOff is the pan range in px; keys are output-timebase (t already shifted by
// the trim start). Static pan (0/1 keys) yields a plain number.
func panExpr(maxOff float64, static float64, keys []PanKey) string {
	off := func(x float64) string { return trimF(maxOff * clamp01(x)) }
	if len(keys) == 0 {
		return off(static)
	}
	if len(keys) == 1 {
		return off(keys[0].X)
	}
	// piecewise lerp: if(lt(t,T0),X0, if(lt(t,T1),lerp01, ... Xn))
	expr := off(keys[len(keys)-1].X)
	for i := len(keys) - 2; i >= 0; i-- {
		a, b := keys[i], keys[i+1]
		seg := off(a.X)
		if b.T > a.T {
			seg = fmt.Sprintf("%s+(%s-%s)*(t-%s)/%s",
				off(a.X), off(b.X), off(a.X), trimF(a.T), trimF(b.T-a.T))
		}
		expr = fmt.Sprintf("if(lt(t,%s),%s,%s)", trimF(b.T), seg, expr)
	}
	return fmt.Sprintf("if(lt(t,%s),%s,%s)", trimF(keys[0].T), off(keys[0].X), expr)
}

// CropFilter builds the ffmpeg crop filter for the project against a probed
// source size. trimStart shifts keyframe times into the output timebase (-ss
// before -i resets t to 0). "" = no reframe needed/possible.
func (p Project) CropFilter(srcW, srcH int, trimStart float64) string {
	a := AspectByKey(p.Aspect)
	cw, ch, axis := CropSizeZoom(srcW, srcH, a, p.Zoom)
	if cw == 0 {
		return ""
	}
	slackX, slackY := srcW-cw, srcH-ch
	if slackX <= 0 && slackY <= 0 {
		if cw != srcW || ch != srcH { // odd-dim trim only
			return fmt.Sprintf("crop=%d:%d:0:0", cw, ch)
		}
		return ""
	}
	keys := make([]PanKey, 0, len(p.PanKF))
	for _, k := range p.PanKF {
		keys = append(keys, PanKey{T: math.Max(k.T-trimStart, 0), X: k.X, Y: k.Y})
	}
	// primary axis follows Pan/X keys; the cross axis (zoom slack) Pan2/Y keys
	prim := axis
	if prim == "" {
		prim = "x"
	}
	axisExpr := func(slack float64, static float64, ks []PanKey) string {
		if slack <= 0 {
			return "0"
		}
		return panExpr(slack, static, ks)
	}
	var x, y string
	if prim == "x" {
		x = axisExpr(float64(slackX), p.Pan, keys)
		y = axisExpr(float64(slackY), clamp01(0.5+p.Pan2), crossKeys(keys))
	} else {
		y = axisExpr(float64(slackY), p.Pan, keys)
		x = axisExpr(float64(slackX), clamp01(0.5+p.Pan2), crossKeys(keys))
	}
	return fmt.Sprintf("crop=%d:%d:%s:%s", cw, ch, quoteExpr(x), quoteExpr(y))
}

// quoteExpr wraps a non-numeric filter expression in single quotes (commas in
// if() would split the -vf chain otherwise).
func quoteExpr(e string) string {
	if strings.ContainsAny(e, "(,") {
		return "'" + e + "'"
	}
	return e
}

// ── export targets ──

// ExportPreset is a platform-shaped export target for the reframed clip.
type ExportPreset struct {
	Key      string
	LabelKey string // i18n key under editor.video.preset.*
	W, H     int    // 0 = keep source size
	CRF      int
	AudioK   int
}

// ExportPresets are the selectable targets, UI order. Reel/Story share 9:16
// 1080×1920; TikTok is the same frame - one preset covers them.
var ExportPresets = []ExportPreset{
	{Key: "reel", LabelKey: "reel", W: 1080, H: 1920, CRF: 18, AudioK: 192},
	{Key: "port45", LabelKey: "port45", W: 1080, H: 1350, CRF: 18, AudioK: 192},
	{Key: "square", LabelKey: "square", W: 1080, H: 1080, CRF: 18, AudioK: 192},
	{Key: "land", LabelKey: "land", W: 1920, H: 1080, CRF: 18, AudioK: 192},
	{Key: "src", LabelKey: "src", CRF: 18, AudioK: 192},
}

// ExportPresetByKey returns the preset for key (first entry fallback).
func ExportPresetByKey(key string) ExportPreset {
	for _, e := range ExportPresets {
		if e.Key == key {
			return e
		}
	}
	return ExportPresets[0]
}

// Preset realizes the export target as a transcode preset (H.264 + AAC mp4,
// hardware-resolvable via Accel auto).
func (e ExportPreset) Preset() transcode.Preset {
	return transcode.Preset{
		ID: "videoedit-" + e.Key, Label: "Editor " + e.Key,
		Container: "mp4", VideoCodec: "h264", Accel: "auto",
		CRF: e.CRF, Width: e.W, Height: e.H, GOPSeconds: 2, SpeedPreset: "medium",
		AudioCodec: "aac", AudioBitrateK: e.AudioK,
	}
}

// ── helpers ──

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// clampOff clamps a cross-axis center offset to [-0.5, 0.5].
func clampOff(f float64) float64 {
	if f < -0.5 {
		return -0.5
	}
	if f > 0.5 {
		return 0.5
	}
	return f
}

// trimF formats a float with minimal digits (max 2 decimals - crop px + secs).
func trimF(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
