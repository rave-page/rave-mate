// Package branding is the rave.page corporate identity as pure data: palette,
// fonts, naming. Single source of brand truth across the native apps (rave-mate's
// Fyne chrome, rave-app's webview shell + loading/error pages). Mirrors the web
// `@theme` block; no deps, stdlib-only consumers.
package branding

// Naming.
const (
	AppName = "rave.page"
	Tagline = "events · music · community"
)

// Palette (web @theme). Dark-first; hex strings for both Go-color parsing and CSS.
const (
	ColorBg          = "#0a0a0a"
	ColorFg          = "#fafafa"
	ColorBrandBase   = "#F70864"
	ColorBrandHot    = "#FF3E8A"
	ColorBrandMint   = "#08F79B" // success / live
	ColorBrandViolet = "#7C3AED" // navigate / info
	ColorBrandAmber  = "#FFB547" // warning
)

// Geometry + type. Orbitron = display/brand; body/mono mirror the web stack.
const (
	RadiusPx    = 8
	FontDisplay = "Orbitron"
	FontBody    = "Inter, system-ui, sans-serif"
	FontMono    = "ui-monospace, SFMono-Regular, Menlo, monospace"
)
