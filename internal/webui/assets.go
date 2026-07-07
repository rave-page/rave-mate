package webui

import "embed"

// assetsFS holds the frontend sources: the rave.page design-system CSS + Orbitron font (copied
// verbatim from rave-page-design-system) and this app's layout CSS. Inlined into one document at
// render time - there is NO asset server (Go drives the webview directly).
//
//go:embed assets
var assetsFS embed.FS
