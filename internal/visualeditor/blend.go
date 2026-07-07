package visualeditor

import "math"

// BlendMode names a per-pixel color-mixing function (W3C compositing separable modes).
type BlendMode string

const (
	BlendNormal     BlendMode = "normal"
	BlendMultiply   BlendMode = "multiply"
	BlendScreen     BlendMode = "screen"
	BlendOverlay    BlendMode = "overlay"
	BlendDarken     BlendMode = "darken"
	BlendLighten    BlendMode = "lighten"
	BlendAdd        BlendMode = "add"
	BlendSubtract   BlendMode = "subtract"
	BlendDifference BlendMode = "difference"
	BlendSoftLight  BlendMode = "soft-light"
	BlendHardLight  BlendMode = "hard-light"
	BlendColorDodge BlendMode = "color-dodge"
	BlendColorBurn  BlendMode = "color-burn"
)

// BlendModes is the ordered list of supported modes (UI dropdown order).
var BlendModes = []BlendMode{
	BlendNormal, BlendMultiply, BlendScreen, BlendOverlay, BlendDarken, BlendLighten,
	BlendAdd, BlendSubtract, BlendDifference, BlendSoftLight, BlendHardLight,
	BlendColorDodge, BlendColorBurn,
}

// ValidBlend reports whether m is a known mode.
func ValidBlend(m BlendMode) bool {
	for _, b := range BlendModes {
		if b == m {
			return true
		}
	}
	return false
}

// blendFn returns B(cb,cs) for a mode: cb=backdrop, cs=source channel, both in [0,1].
func blendFn(m BlendMode, cb, cs float64) float64 {
	switch m {
	case BlendMultiply:
		return cb * cs
	case BlendScreen:
		return cb + cs - cb*cs
	case BlendOverlay: // = hard-light keyed on backdrop (operands swapped)
		return hardLight(cs, cb)
	case BlendDarken:
		return math.Min(cb, cs)
	case BlendLighten:
		return math.Max(cb, cs)
	case BlendAdd:
		return math.Min(1, cb+cs)
	case BlendSubtract:
		return math.Max(0, cb-cs)
	case BlendDifference:
		return math.Abs(cb - cs)
	case BlendSoftLight:
		return softLight(cb, cs)
	case BlendHardLight: // keyed on source
		return hardLight(cb, cs)
	case BlendColorDodge:
		if cs >= 1 {
			return 1
		}
		return math.Min(1, cb/(1-cs))
	case BlendColorBurn:
		if cs <= 0 {
			return 0
		}
		return 1 - math.Min(1, (1-cb)/cs)
	default: // BlendNormal + unknown
		return cs
	}
}

// hardLight kernel: overlay = hardLight(cb,cs); hard-light = hardLight(cs,cb).
func hardLight(cb, cs float64) float64 {
	if cs <= 0.5 {
		return cb * (2 * cs)
	}
	s := 2*cs - 1
	return cb + s - cb*s // = screen(cb, 2cs-1)
}

func softLight(cb, cs float64) float64 {
	if cs <= 0.5 {
		return cb - (1-2*cs)*cb*(1-cb)
	}
	var d float64
	if cb <= 0.25 {
		d = ((16*cb-12)*cb + 4) * cb
	} else {
		d = math.Sqrt(cb)
	}
	return cb + (2*cs-1)*(d-cb)
}
