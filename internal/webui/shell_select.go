package webui

// Shell selection. Three implementations satisfy the 7-method `shell` seam and all THREE coexist in
// one binary:
//   cgo  (default)  the in-proc WebView2 window            - shell_cgo.go / shell_nocgo.go
//   proc            the same window in a supervised child  - shell_proc.go (phase B5)
//   virtual         windowless, for remote-library mirrors - virtualshell.go (not selectable here)
//
// RAVE_MATE_SHELL=proc opts into the child. The DEFAULT STAYS cgo: flipping it is a later decision.

import (
	"os"
	"strings"

	"rave.page/mate/internal/config"
)

// shellKind returns the selected window host ("cgo" | "proc").
func shellKind() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("RAVE_MATE_SHELL")), "proc") {
		return "proc"
	}
	return "cgo"
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
