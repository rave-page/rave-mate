//go:build !windows || !encembed

package mfenc

// No embedded encoder child in this build (non-Windows, or built without the encembed
// tag when the zig artifact isn't staged). Extraction no-ops; encExePath falls back to
// the sidecar/dev locations. SHIPPED Windows CI builds always carry the embed.
var embeddedEnc []byte
