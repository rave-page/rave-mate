//go:build windows

package selfupdate

import "errors"

// Reexec is unsupported on Windows (no exec(2)); use Relaunch.
func Reexec(string) error { return errors.New("re-exec unsupported on windows; use Relaunch") }
