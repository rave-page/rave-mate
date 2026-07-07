//go:build !windows

package webui

import (
	"context"
	"errors"
)

// studio.Picker stubs - native dialogs are Windows-only for now (pickers_windows.go); other
// platforms degrade gracefully. Cancel semantics elsewhere: ("", nil).
var errNoPicker = errors.New("native file picker not available on this platform yet")

func (u *UI) PickDirectory(context.Context) (string, error)                  { return "", errNoPicker }
func (u *UI) PickFile(context.Context) (string, error)                       { return "", errNoPicker }
func (u *UI) ChooseSavePath(context.Context, string, string) (string, error) { return "", errNoPicker }
