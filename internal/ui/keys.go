package ui

import (
	"image/color"

	"fyne.io/fyne/v2"

	"rave.page/mate/internal/musiclib"
)

// Harmonic key UI: relation→color mapping, key pills, and the Camelot wheel
// panel shown when a track is selected. Compatible keys light up in their
// relation color; dissonant keys stay neutral (uncolored).

// keyRelColor maps a harmonic relation to its brand accent. ok=false → neutral.
func keyRelColor(rel musiclib.KeyRel) (color.Color, bool) {
	switch rel {
	case musiclib.RelSame:
		return colBrandMint, true
	case musiclib.RelRelative:
		return colBrandViol, true
	case musiclib.RelUp:
		return colBrandHot, true
	case musiclib.RelDown:
		return colBrandAmber, true
	}
	return nil, false
}

// keyRelLabel names a relation for legends/tooltips.
func keyRelLabel(rel musiclib.KeyRel) string {
	switch rel {
	case musiclib.RelSame:
		return "same key"
	case musiclib.RelRelative:
		return "relative"
	case musiclib.RelUp:
		return "energy +1"
	case musiclib.RelDown:
		return "energy −1"
	}
	return ""
}

// keyPillColors styles a key pill for k relative to an optional reference key.
// No ref / unparsable / dissonant → neutral surface colors.
func keyPillColors(k musiclib.Key, ref *musiclib.Key) (bg, fg color.Color) {
	if ref != nil {
		if c, ok := keyRelColor(musiclib.KeyRelation(*ref, k)); ok {
			return c, colBackground
		}
	}
	return colSecondary, colMuted
}

// keyLabel renders "8A · Am" (Camelot + musical) for pills and filters.
func keyLabel(k musiclib.Key) string { return k.Camelot() + " · " + k.Name() }

// harmonicLegend is the one-line color key under the wheel.
func harmonicLegend() fyne.CanvasObject {
	mk := func(rel musiclib.KeyRel) fyne.CanvasObject {
		c, _ := keyRelColor(rel)
		return newPill(keyRelLabel(rel), c, colBackground, nil)
	}
	return WrapActions(mk(musiclib.RelSame), mk(musiclib.RelRelative), mk(musiclib.RelUp), mk(musiclib.RelDown))
}
