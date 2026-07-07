//go:build vr

package vroverlay

// BuiltWithVR reports whether this binary was compiled with the `vr` tag (OpenVR backend present).
// True here; the !vr stub returns false. Lets the UI tell "non-vr build" apart from "vr build but
// SteamVR/openvr_api.dll not ready".
func BuiltWithVR() bool { return true }
