package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func rgb(c color.Color) (r, g, b, a uint8) {
	r32, g32, b32, a32 := c.RGBA()
	return uint8(r32 >> 8), uint8(g32 >> 8), uint8(b32 >> 8), uint8(a32 >> 8)
}

// TestBrandThemeTokens anchors the canonical rave.page mappings so a stray edit to the
// palette is caught (the native UI can't be screenshot in CI).
func TestBrandThemeTokens(t *testing.T) {
	th := newBrandTheme()
	v := fyne.ThemeVariant(0)

	cases := []struct {
		name     fyne.ThemeColorName
		r, g, b  uint8
		whatItIs string
	}{
		{theme.ColorNameBackground, 0x16, 0x18, 0x1d, "gunmetal page background"},
		{theme.ColorNamePrimary, 0xF7, 0x08, 0x64, "brand-base pink"},
		{theme.ColorNameButton, 0x30, 0x34, 0x3e, "brushed-metal control fill"},
		{theme.ColorNameSuccess, 0x08, 0xF7, 0x9B, "brand mint"},
		{theme.ColorNameWarning, 0xFF, 0xB5, 0x47, "brand amber"},
	}
	for _, c := range cases {
		r, g, b, _ := rgb(th.Color(c.name, v))
		if r != c.r || g != c.g || b != c.b {
			t.Errorf("%s (%s) = #%02x%02x%02x, want #%02x%02x%02x", c.name, c.whatItIs, r, g, b, c.r, c.g, c.b)
		}
	}
}

// TestDenseMetrics pins the dense metric table (stock 2.7.4 → ours) so a stray edit or a
// Fyne default bump can't re-inflate control bulk.
func TestDenseMetrics(t *testing.T) {
	th := newBrandTheme()
	cases := []struct {
		name        fyne.ThemeSizeName
		want, stock float32
	}{
		{theme.SizeNameText, 12, 14},
		{theme.SizeNameCaptionText, 11, 11},
		{theme.SizeNameHeadingText, 18, 24},
		{theme.SizeNameSubHeadingText, 14, 18},
		{theme.SizeNameLineSpacing, 3, 4},
		{theme.SizeNameInlineIcon, 16, 20},
		{theme.SizeNamePadding, 3, 4},
		{theme.SizeNameInnerPadding, 4, 8},
		{theme.SizeNameScrollBar, 10, 12},
		{theme.SizeNameScrollBarSmall, 3, 3},
		{theme.SizeNameSeparatorThickness, 1, 1},
		{theme.SizeNameInputRadius, 8, 5},
	}
	stock := theme.DefaultTheme()
	for _, c := range cases {
		if got := th.Size(c.name); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
		if got := stock.Size(c.name); got != c.stock {
			t.Errorf("stock %s = %v, want %v (Fyne default moved - retune dense table)", c.name, got, c.stock)
		}
	}
}

// TestDenseMetricsShrinkWidgets lays out key widgets on the in-memory test canvas under
// stock vs brand theme: min-sizes must shrink (density) while labels still fit (no clip).
func TestDenseMetricsShrinkWidgets(t *testing.T) {
	a := test.NewApp()
	defer test.NewApp()

	type mins struct{ btn, iconBtn, entry, check fyne.Size }
	measure := func() mins {
		btn := widget.NewButton("Import / Refresh", nil)
		iconBtn := widget.NewButtonWithIcon("Export", theme.DocumentSaveIcon(), nil)
		e := widget.NewEntry()
		e.SetPlaceHolder("Search title / artist…")
		chk := widget.NewCheck("Enabled", nil)
		w := test.NewWindow(container.NewVBox(btn, iconBtn, e, chk))
		defer w.Close()
		w.Resize(fyne.NewSize(400, 300))
		return mins{btn.MinSize(), iconBtn.MinSize(), e.MinSize(), chk.MinSize()}
	}

	a.Settings().SetTheme(theme.DefaultTheme())
	stock := measure()
	a.Settings().SetTheme(newBrandTheme())
	dense := measure()

	shrunk := func(what string, d, s fyne.Size) {
		if d.Height >= s.Height || d.Width > s.Width {
			t.Errorf("%s min %v not denser than stock %v", what, d, s)
		}
	}
	shrunk("button", dense.btn, stock.btn)
	shrunk("icon button", dense.iconBtn, stock.iconBtn)
	shrunk("entry", dense.entry, stock.entry)
	shrunk("check", dense.check, stock.check)

	// Text still fits: min box holds the measured label plus inner padding.
	th := newBrandTheme()
	inner := th.Size(theme.SizeNameInnerPadding)
	txt := fyne.MeasureText("Import / Refresh", th.Size(theme.SizeNameText), fyne.TextStyle{})
	if dense.btn.Width < txt.Width+2*inner || dense.btn.Height < txt.Height+4 {
		t.Errorf("button min %v clips label %v (+inner %v)", dense.btn, txt, inner)
	}
	ptxt := fyne.MeasureText("Search title / artist…", th.Size(theme.SizeNameText), fyne.TextStyle{})
	if dense.entry.Height < ptxt.Height+2*inner {
		t.Errorf("entry min %v clips text %v", dense.entry, ptxt)
	}
}

// TestBrandThemeFonts: headings/brand chrome (bold) are Orbitron; body + italic + mono use
// the base sans/mono (crisper than the display face at small sizes).
func TestBrandThemeFonts(t *testing.T) {
	th := newBrandTheme()
	if got := th.Font(fyne.TextStyle{Bold: true}); got != th.bold {
		t.Error("headings/brand chrome (bold) must be Orbitron Bold")
	}
	if got := th.Font(fyne.TextStyle{}); got != th.base.Font(fyne.TextStyle{}) {
		t.Error("regular body text must be the base sans (not Orbitron)")
	}
	if got := th.Font(fyne.TextStyle{Bold: true, Italic: true}); got == th.bold {
		t.Error("italic must fall back to base (Orbitron has no italic)")
	}
	if got := th.Font(fyne.TextStyle{Monospace: true}); got == th.bold {
		t.Error("monospace (logs) must not be Orbitron")
	}
}
