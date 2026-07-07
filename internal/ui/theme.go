// Package ui holds the Fyne presentation layer: the corporate-identity theme, the tray,
// the main window, and per-tab views. Brand identity (palette, Orbitron, dark-first)
// matches the rave.page web app's @theme - see app/src/index.css in the parent repo.
package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"rave.page/mate/internal/ui/fonts"
)

// Brand palette - canonical rave.page design tokens (rave-page-design-system/
// colors_and_type.css + app/src/index.css @theme). Dark-first.
var (
	// Winamp-flavoured gunmetal: cool metallic greys (classic skin chassis) carrying the
	// rave.page brand accents. The classic green LCD readout maps to our brand mint.
	colBackground = hexRGB(0x16, 0x18, 0x1d) // gunmetal page - cool dark metal
	colSurface    = hexRGB(0x23, 0x26, 0x2e) // raised metallic panel (card/menu/overlay)
	colSecondary  = hexRGB(0x30, 0x34, 0x3e) // brushed-metal control fill (buttons, inputs)
	colForeground = hexRGB(0xe6, 0xe8, 0xee) // cool off-white
	colMuted      = hexRGB(0xa6, 0xab, 0xb6) // muted cool grey
	colBorder     = hexRGB(0x3c, 0x41, 0x4d) // brighter bevel edge (metallic rim)
	colLCD        = hexRGB(0x0c, 0x14, 0x12) // dark LCD well (behind mint readouts)
	colBrandBase  = hexRGB(0xF7, 0x08, 0x64) // --color-brand-base: primary CTA / focus / live glow
	colBrandHot   = hexRGB(0xFF, 0x3E, 0x8A) // --color-brand-hot: hover step / gradient mid
	colBrandDeep  = hexRGB(0xA1, 0x13, 0x8E) // --color-brand-deep: gradient mid / ambient glow
	colBrandMint  = hexRGB(0x08, 0xF7, 0x9B) // --color-brand-mint: success / live / confirm
	colBrandViol  = hexRGB(0x7C, 0x3A, 0xED) // --color-brand-violet: navigate / explore
	colBrandAmber = hexRGB(0xFF, 0xB5, 0x47) // --color-brand-amber: warning
	colInfo       = hexRGB(0xA7, 0x8B, 0xFA) // --color-info: info text / banners
	colError      = hexRGB(0xEF, 0x44, 0x44) // distinct red for error UX (brand pink == primary)
)

// _ keeps the not-yet-consumed brand accents referenced so they stay documented + linted.
var _ = []color.Color{colBrandDeep, colBrandViol, colInfo, colLCD}

// brandTheme implements fyne.Theme with the rave.page identity. Dark-only by design
// (the desktop companion mirrors the app's dark-first surface).
type brandTheme struct {
	bold fyne.Resource
	base fyne.Theme
}

func newBrandTheme() *brandTheme {
	return &brandTheme{
		bold: fyne.NewStaticResource("Orbitron-Bold.ttf", fonts.Bold()),
		base: theme.DefaultTheme(),
	}
}

func (t *brandTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return colBackground
	case theme.ColorNameForeground, theme.ColorNameForegroundOnPrimary:
		return colForeground
	case theme.ColorNamePrimary:
		return colBrandBase
	case theme.ColorNameHover:
		return withAlpha(colBrandHot, 0x22)
	case theme.ColorNameFocus:
		return withAlpha(colBrandBase, 0x55)
	case theme.ColorNameSelection:
		return withAlpha(colBrandBase, 0x40)
	case theme.ColorNameButton:
		return colSecondary
	case theme.ColorNameDisabledButton:
		return withAlpha(colSecondary, 0x80)
	case theme.ColorNameInputBackground:
		return colSecondary
	case theme.ColorNameInputBorder, theme.ColorNameSeparator:
		return colBorder
	case theme.ColorNamePlaceHolder, theme.ColorNameDisabled:
		return colMuted
	case theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground:
		return colSurface
	case theme.ColorNameSuccess:
		return colBrandMint
	case theme.ColorNameWarning:
		return colBrandAmber
	case theme.ColorNameError:
		return colError
	case theme.ColorNameScrollBar:
		return withAlpha(colForeground, 0x18)
	case theme.ColorNameShadow:
		return withAlpha(color.Black, 0xaa)
	default:
		return t.base.Color(name, theme.VariantDark)
	}
}

// Font: Orbitron Bold is the brand display face - used for the bold text Fyne renders for
// headings, card titles, nav/tab chrome and emphasis. Body, values and inputs (regular
// weight) use the base sans, which is hinted + far crisper than a geometric display face at
// small UI sizes (the display font read as soft/"low-DPI" as body). Monospace (logs) +
// italic (Orbitron has no italic) fall back to the base font.
func (t *brandTheme) Font(style fyne.TextStyle) fyne.Resource {
	if style.Monospace || style.Symbol {
		return t.base.Font(style)
	}
	if style.Bold && !style.Italic {
		return t.bold
	}
	return t.base.Font(style)
}

func (t *brandTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

// Size: dense metric set (stock Fyne 2.7.4 → ours). Every value that drives control
// bulk is pinned here so a Fyne default bump can't re-inflate the layout. Guarded by
// TestDenseMetrics* in theme_test.go.
func (t *brandTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 12 // body (stock 14) - denser, sharper layout
	case theme.SizeNameCaptionText:
		return 11 // helper/caption (stock 11) - pinned; also kitButton label size
	case theme.SizeNameHeadingText:
		return 18 // card titles / section headings (stock 24)
	case theme.SizeNameSubHeadingText:
		return 14 // stock 18
	case theme.SizeNameLineSpacing:
		return 3 // stock 4 - tighter multi-line labels/logs
	case theme.SizeNameInlineIcon:
		return 16 // stock 20 - shrinks buttons-with-icons, checks, list glyphs
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 8 // --radius
	case theme.SizeNamePadding:
		return 3 // outer padding / layout gap (stock 4)
	case theme.SizeNameInnerPadding:
		return 4 // widget internal padding (stock 8) - the big bulk win on buttons/entries/rows
	case theme.SizeNameScrollBar:
		return 10 // stock 12 - slimmer expanded scrollbar
	case theme.SizeNameScrollBarSmall:
		return 3 // stock 3 - pinned (collapsed rail)
	case theme.SizeNameSeparatorThickness:
		return 1 // stock 1 - pinned
	default:
		return t.base.Size(name)
	}
}

func hexRGB(r, g, b uint8) color.Color { return color.NRGBA{R: r, G: g, B: b, A: 0xff} }

func withAlpha(c color.Color, a uint8) color.Color {
	r, g, b, _ := c.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: a}
}
