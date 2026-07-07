// Package fonts embeds the Orbitron brand faces ONCE for every renderer (Fyne theme,
// giokit/Gio, future surfaces). Import this instead of adding per-package TTF copies.
// Accessors return the embedded slice directly - callers must treat it as read-only.
package fonts

import _ "embed"

//go:embed Orbitron-Regular.ttf
var regular []byte

//go:embed Orbitron-Medium.ttf
var medium []byte

//go:embed Orbitron-SemiBold.ttf
var semiBold []byte

//go:embed Orbitron-Bold.ttf
var bold []byte

// Regular returns the Orbitron Regular TTF bytes.
func Regular() []byte { return regular }

// Medium returns the Orbitron Medium TTF bytes.
func Medium() []byte { return medium }

// SemiBold returns the Orbitron SemiBold TTF bytes.
func SemiBold() []byte { return semiBold }

// Bold returns the Orbitron Bold TTF bytes.
func Bold() []byte { return bold }
