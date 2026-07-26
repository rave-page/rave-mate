package webui

// Shell selection. Three implementations satisfy the 7-method `shell` seam and all THREE coexist in
// one binary:
//   cgo  (default)  the in-proc WebView2 window            - shell_cgo.go / shell_nocgo.go
//   proc            the same window in a supervised child  - shell_proc.go (phase B5)
//   virtual         windowless, for remote-library mirrors - virtualshell.go (not selectable here)
//
// RAVE_MATE_SHELL=proc opts into the child. The DEFAULT STAYS cgo: flipping it is a later decision.
//
// B6 child seam: under the proc shell the CHILD EXE is selectable too - "go" (default, `rave-mate
// feature webview`) or "zig" (the rave-shell exe, zigui builds only; shell_zig.go). Selection:
// config features.ui.shellImpl="zig" or RAVE_MATE_SHELL=zig (either also implies proc). zigShellExe
// holds the resolved exe path; "" = Go child. The daemon-side PSH1 code is identical for both.

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

// zigShellWanted reports the Zig child was ASKED for (env or config) - resolution may still fall
// back when the exe is absent.
func zigShellWanted(cfg *config.Config) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("RAVE_MATE_SHELL")), "zig") {
		return true
	}
	return cfg != nil && cfg.Features.UI.ZigShell()
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
