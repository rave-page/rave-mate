//go:build windows

package service

import (
	"errors"
	"fmt"

	"rave.page/mate/internal/elevate"
)

// interactive runs fn, re-execing `rave-mate <cmd>` elevated (UAC) first when not already
// admin - the SCM CreateService/DeleteService calls need it. The elevated child re-enters
// this path already-elevated and runs fn in-process.
func interactive(cmd string, fn func() error) error {
	if elevate.IsElevated() {
		return fn()
	}
	code, err := elevate.RunSelfElevated([]string{cmd})
	if err != nil {
		if errors.Is(err, elevate.ErrDeclined) {
			return errors.New("elevation declined (admin rights needed)")
		}
		return fmt.Errorf("elevation failed: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("elevated %s exited with code %d", cmd, code)
	}
	return nil
}
