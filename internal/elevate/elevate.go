// Package elevate runs privileged file operations that need admin/root (e.g. writing under
// Program Files for the Traktor QML mod). It can't elevate the current process - instead it
// relaunches this same exe elevated (Windows UAC via ShellExecuteEx "runas"), waits, and
// returns the child's exit code. Non-Windows is a stub until a portable path (pkexec/sudo)
// is needed. The caller passes a subcommand + a --result file so the elevated child can
// report a detailed error back across the privilege boundary.
package elevate

import "errors"

var (
	// ErrUnsupported is returned by the no-op driver on platforms without an elevation path.
	ErrUnsupported = errors.New("elevate: not supported on this platform")
	// ErrDeclined means the user dismissed the UAC prompt.
	ErrDeclined = errors.New("elevate: elevation declined by user")
)
