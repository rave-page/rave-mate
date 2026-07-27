//go:build windows && shellembed

package webui

import _ "embed"

// Embedded Zig window child (native/zigui, staged by scripts/build-zig.sh). Shipping it INSIDE
// rave-mate.exe is what makes the Zig shell a REAL default on updated installs: the self-updater
// swaps only the main exe and consumes no feed assets (internal/updater has no asset handling at
// all), so a sidecar-only rave-shell.exe would reach fresh installer runs and nothing else - the
// same trap the encoder child hit in field crash #166. Embedding also removes version skew: the
// PSH1 wire is a private contract between THIS daemon and ITS child, and extraction stamps the
// filename with the embed's content hash so a self-updated exe never runs the old child.
//
//go:embed embedded/rave-shell.exe
var embeddedShell []byte
