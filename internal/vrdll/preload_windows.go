//go:build windows

package vrdll

import "syscall"

// Preload loads openvr_api.dll by ABSOLUTE path so the backend's bare LoadLibraryA(VRLIB_NAME)
// then resolves to the already-loaded module. Required in a featurehost child: the child runs from
// a per-role hardlink in the proc cache dir, so neither its application directory nor the managed
// bin dir is on the default DLL search path, and the load only ever succeeded via the inherited
// current directory (#49). Best-effort - a miss leaves the backend reporting VR unavailable, which
// is the documented no-DLL behaviour.
func Preload() {
	st := Probe()
	if !st.Installed {
		return
	}
	_, _ = syscall.LoadLibrary(st.Path)
}
