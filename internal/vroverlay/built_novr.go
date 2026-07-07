//go:build !vr

package vroverlay

// BuiltWithVR reports whether this binary was compiled with the `vr` tag. False on the stub build.
func BuiltWithVR() bool { return false }
