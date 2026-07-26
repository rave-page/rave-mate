//go:build !zigui

package webui

import (
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

// resolveZigShellExe: untagged builds never select the Zig window child; asking for it logs the
// fallback loudly (parity with the tagged resolver's missing-exe path).
func resolveZigShellExe(cfg *config.Config, log *logbus.Bus) string {
	if zigShellWanted(cfg) && log != nil {
		log.Error("webui", "zig shell requested but this build lacks the zigui tag - using the Go webview child", nil)
	}
	return ""
}
