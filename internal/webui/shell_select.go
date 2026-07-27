package webui

// Shell selection. Three implementations satisfy the 7-method `shell` seam and all THREE coexist in
// one binary:
//   cgo  (default)  the in-proc WebView2 window            - shell_cgo.go / shell_nocgo.go
//   proc            the same window in a supervised child  - shell_proc.go (phase B5)
//   virtual         windowless, for remote-library mirrors - virtualshell.go (not selectable here)
//
// DEFAULT (2026-07-27): the ZIG child under the proc shell, whenever rave-shell.exe is present.
// It is the only host where the rAF surfaces (graphs, Ableton Link phrase bar) were measured
// rendering and updating correctly; the Go proc child stalled them on the dev rig. The ladder is
// zig → cgo, deliberately: if rave-shell.exe is absent the host falls back to the IN-PROCESS Go
// window (the previous default), never to the Go proc child.
//
// Opt-outs, both still honoured: config features.ui.shellImpl="go" or RAVE_MATE_SHELL=cgo pins the
// in-process window; RAVE_MATE_SHELL=proc asks for the proc shell with the Go child (debugging).
// zigShellExe holds the resolved exe path; "" = no Zig child. The daemon-side PSH1 code is
// identical for every host, so the choice only changes who owns the window.

import (
	"os"
	"strings"

	"rave.page/mate/internal/config"
)

// zigShellExe is the resolved Zig window-child exe path ("" = spawn the Go child). Set in New via
// resolveZigShellExe (shell_zig.go, tag zigui; stub returns "") - same pattern as webviewAllowGPU.
var zigShellExe string

// shellKind returns the selected window host ("cgo" | "proc").
func shellKind() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RAVE_MATE_SHELL"))) {
	case "proc", "zig":
		return "proc"
	case "cgo":
		return "cgo"
	}
	if zigShellExe != "" { // shellImpl=zig resolved: the zig child needs the proc shell
		return "proc"
	}
	return "cgo"
}

// zigShellWanted reports the Zig child was asked for - which is now the DEFAULT, so this answers
// yes unless something explicitly pins another host. Resolution may still fall back when the exe is
// absent (resolveZigShellExe), so "wanted" is not "used".
//
// A nil cfg means the caller has no config to consult (tests, early boot); it takes the default too,
// because a default that only applies when a config object happens to exist is not a default.
func zigShellWanted(cfg *config.Config) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RAVE_MATE_SHELL"))) {
	case "zig":
		return true
	case "cgo", "go", "proc": // explicit non-zig host, including "proc with the Go child"
		return false
	}
	return cfg == nil || cfg.Features.UI.ZigShell()
}

func newShell(title string, w, h int, onAction func(string), onReady func()) (shell, bool) {
	if shellKind() == "proc" {
		return newProcShell(title, w, h, onAction, onReady)
	}
	return newNativeShell(title, w, h, onAction, onReady)
}

// webviewInitJS overrides the document-start runtime; "" = the compiled-in runtimeJS. The B5 child
// sets it from the init payload so the injected bytes are the DAEMON's (byte-contracted with the
// renderers), never a copy that could drift.
var webviewInitJS string

// webviewDataDir overrides the WebView2 profile dir; "" = resolve it here. The B5 child receives it
// over the wire because it must not read config.
var webviewDataDir string

// shellInitJS is the exact script injected at document start.
func shellInitJS() string {
	if webviewInitJS != "" {
		return webviewInitJS
	}
	return runtimeJS
}

// shellDataDir resolves the WebView2 profile dir (daemon side; the child gets the answer over PSH1).
func shellDataDir() (string, error) {
	if webviewDataDir != "" {
		return webviewDataDir, nil
	}
	return config.DataPath("webview2")
}
