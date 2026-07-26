//go:build zigui

package webui

// B6 zig-shell resolution (zigui builds). The Zig window child (`rave-shell.exe`, native/zigui
// src/shell) replaces the Go `rave-mate feature webview` child behind the SAME PSH1 contract.
// Opt-in via features.ui.shellImpl="zig" / RAVE_MATE_SHELL=zig; a missing exe falls back to the
// Go child with a loud log - never a broken UI.

import (
	"os"
	"path/filepath"
	"runtime"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

// zigShellExeEnv overrides where the rave-shell exe is looked for (dev/test convenience).
const zigShellExeEnv = "RAVE_MATE_SHELL_EXE"

// zigShellExeName is the Zig window-child artifact (native/zigui zig-out/bin), shipped beside the
// daemon exe.
func zigShellExeName() string {
	if runtime.GOOS == "windows" {
		return "rave-shell.exe"
	}
	return "rave-shell"
}

// resolveZigShellExe returns the rave-shell exe path when the Zig child is selected AND present;
// "" otherwise (Go child). Search: $RAVE_MATE_SHELL_EXE, else rave-shell[.exe] beside the daemon.
func resolveZigShellExe(cfg *config.Config, log *logbus.Bus) string {
	if !zigShellWanted(cfg) {
		return ""
	}
	var cands []string
	if p := os.Getenv(zigShellExeEnv); p != "" {
		cands = append(cands, p)
	} else if self, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(self), zigShellExeName()))
	}
	for _, p := range cands {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			if log != nil {
				log.Info("webui", "zig shell child selected", map[string]any{"exe": p})
			}
			return p
		}
	}
	if log != nil {
		log.Error("webui", "zig shell exe missing - falling back to the Go webview child", map[string]any{
			"looked": cands, "hint": "build with scripts/build-zig.sh (native/zigui zig-out/bin/" + zigShellExeName() + ") or set " + zigShellExeEnv,
		})
	}
	return ""
}
