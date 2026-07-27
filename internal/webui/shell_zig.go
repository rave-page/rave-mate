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
// "" otherwise (in-process Go window).
//
// Search order, most authoritative first:
//  1. $RAVE_MATE_SHELL_EXE - explicit override for dev/test.
//  2. the EMBEDDED child, extracted to the cache dir (shellembed builds). Ahead of the sidecar
//     because it is the only copy guaranteed to match this exe: the self-updater replaces the exe
//     and nothing else, so a sidecar left behind by an older installer can be stale.
//  3. rave-shell[.exe] beside the daemon - installer-bundled, and the dev-tree path for builds
//     without the embed.
func resolveZigShellExe(cfg *config.Config, log *logbus.Bus) string {
	if !zigShellWanted(cfg) {
		return ""
	}
	var cands []string
	if p := os.Getenv(zigShellExeEnv); p != "" {
		cands = append(cands, p)
	} else {
		if p, err := stagedShellExe(); err != nil {
			if log != nil {
				log.Warn("webui", "could not extract the embedded zig shell - trying the sidecar copy",
					map[string]any{"error": err.Error()})
			}
		} else if p != "" {
			cands = append(cands, p)
		}
		if self, err := os.Executable(); err == nil {
			cands = append(cands, filepath.Join(filepath.Dir(self), zigShellExeName()))
		}
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
		// The Zig child is the default window host, so its absence is a BUILD gap, not a user
		// choice: a shipped Windows build carries the embed and can always stage it. Loud on
		// purpose - the UI still works (in-process Go window), so nothing else would say so.
		log.Error("webui", "zig shell exe missing - using the in-process Go window instead", map[string]any{
			"looked": cands, "embedded": hasEmbeddedShell(),
			"hint": "build with scripts/build-zig.sh (stages native/zigui zig-out/bin/" + zigShellExeName() +
				" into internal/webui/embedded for -tags shellembed) or set " + zigShellExeEnv,
		})
	}
	return ""
}
