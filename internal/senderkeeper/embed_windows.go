//go:build windows && encembed

package senderkeeper

import _ "embed"

// Embedded keeper (native/zigkeeper, staged by scripts/build-zig.sh). Same reason as the encoder
// child (mfenc/embed_windows.go): the self-updater swaps only rave-mate.exe, so a sidecar-only
// keeper never reaches updated installs and "default on" would be a no-op there.
//
//go:embed embedded/rave-mate-keeper.exe
var embeddedKeeper []byte
