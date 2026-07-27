//go:build !windows || !shellembed

package webui

// No embedded Zig window child in this build (non-Windows, or built without the shellembed tag
// because the zig artifact was not staged). Extraction no-ops and resolveZigShellExe falls back to
// the sidecar/dev locations, then to the in-process Go window. SHIPPED Windows CI builds always
// carry the embed.
var embeddedShell []byte
