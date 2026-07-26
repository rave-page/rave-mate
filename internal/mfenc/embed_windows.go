//go:build windows && encembed

package mfenc

import _ "embed"

// Embedded encoder child (native/zigenc, staged by scripts/build-zig.sh). Shipping it
// INSIDE rave-mate.exe is what makes self-updated installs work: the self-updater swaps
// only the main exe, so a sidecar-only child never reaches updated installs (field
// crash #166) and could version-skew even when present. Extraction: stagedChildExe.
//
//go:embed embedded/rave-mate-enc.exe
var embeddedEnc []byte
