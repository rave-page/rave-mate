package giokit

import (
	"image/color"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/text"
	"gioui.org/unit"

	"rave.page/mate/internal/ui/fonts"
)

// Brand palette - canonical rave.page web tokens (app/src/index.css @theme), dark-first.
// Single source of brand truth for Gio surfaces; never hardcode hex in widgets.
var (
	ColBg        = rgb(0x0a0a0a) // page background
	ColSurface   = rgb(0x161619) // raised panel (bars, cards)
	ColControl   = rgb(0x232329) // control fill (buttons, inputs, track)
	ColFg        = rgb(0xfafafa) // foreground text
	ColMuted     = rgb(0xa1a1aa) // secondary text
	ColBorder    = rgb(0x2e2e35) // hairlines / separators
	ColBrandBase = rgb(0xF70864) // --color-brand-base: primary / focus / live
	ColBrandHot  = rgb(0xFF3E8A) // --color-brand-hot: hover step
	ColMint      = rgb(0x08F79B) // --color-brand-mint: success / live
	ColViolet    = rgb(0x7C3AED) // --color-brand-violet: navigate / info
	ColAmber     = rgb(0xFFB547) // --color-brand-amber: warning
	ColError     = rgb(0xEF4444) // error (brand pink == primary)
)

// Orbitron is the brand display typeface name registered in the Theme shaper.
const Orbitron font.Typeface = "Orbitron"

// Theme carries the brand palette + density-first metrics + text shaper for all giokit
// widgets. Metrics are the migration's raison d'être: ~24–26px controls, 4–6px padding,
// 12–13sp text - far denser than Fyne/material defaults.
type Theme struct {
	// Palette (copies of the Col* tokens so a theme instance is self-contained).
	Bg, Surface, Control, Fg, Muted, Border            color.NRGBA
	BrandBase, BrandHot, Mint, Violet, Amber, ErrorCol color.NRGBA

	// Density-first metrics.
	TextSize      unit.Sp // body: 13
	CaptionSize   unit.Sp // captions / status: 12
	DisplaySize   unit.Sp // Orbitron display text: 15
	ControlHeight unit.Dp // buttons / sliders / inputs: 24
	PadX          unit.Dp // horizontal control padding: 6
	PadY          unit.Dp // vertical control padding: 4
	Gap           unit.Dp // default inter-widget gap: 4
	Radius        unit.Dp // --radius: 8
	Hairline      unit.Dp // separators / borders: 1
	ToolStripH    unit.Dp // toolbar height: 32
	StatusStripH  unit.Dp // bottom status bar height: 24
	ListRowH      unit.Dp // uniform dense list row: 22

	// Text.
	Shaper  *text.Shaper
	Sans    font.Font // body (Go sans - hinted, crisp at small sizes)
	Display font.Font // Orbitron Medium - brand chrome / headings
	Strong  font.Font // Orbitron Bold - emphasis
}

// NewTheme builds the brand theme: shaper with the Go font collection (body) + the
// shared embedded Orbitron faces (display), density metrics per the migration spec.
func NewTheme() *Theme {
	coll := gofont.Collection()
	for _, f := range []struct {
		data   []byte
		weight font.Weight
	}{
		{fonts.Regular(), font.Normal},
		{fonts.Medium(), font.Medium},
		{fonts.SemiBold(), font.SemiBold},
		{fonts.Bold(), font.Bold},
	} {
		face, err := opentype.Parse(f.data)
		if err != nil {
			continue // embedded + parse-tested; never expected at runtime
		}
		coll = append(coll, font.FontFace{Font: font.Font{Typeface: Orbitron, Weight: f.weight}, Face: face})
	}
	return &Theme{
		Bg: ColBg, Surface: ColSurface, Control: ColControl, Fg: ColFg, Muted: ColMuted, Border: ColBorder,
		BrandBase: ColBrandBase, BrandHot: ColBrandHot, Mint: ColMint, Violet: ColViolet, Amber: ColAmber, ErrorCol: ColError,

		TextSize:      13,
		CaptionSize:   12,
		DisplaySize:   15,
		ControlHeight: 24,
		PadX:          6,
		PadY:          4,
		Gap:           4,
		Radius:        8,
		Hairline:      1,
		ToolStripH:    32,
		StatusStripH:  24,
		ListRowH:      22,

		Shaper:  text.NewShaper(text.WithCollection(coll)),
		Sans:    font.Font{},
		Display: font.Font{Typeface: Orbitron, Weight: font.Medium},
		Strong:  font.Font{Typeface: Orbitron, Weight: font.Bold},
	}
}

// WithAlpha returns c at alpha a (for hover/focus overlays).
func WithAlpha(c color.NRGBA, a uint8) color.NRGBA { c.A = a; return c }

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}
