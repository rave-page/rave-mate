//go:build !windows

package timecode

import "context"

// Non-Windows audio stub. A portable device-selectable backend is a follow-up; until then the LTC
// sink reports unsupported and stays idle.

// WaveOutDevices returns no devices.
func WaveOutDevices() ([]string, error) { return nil, ErrUnsupported }

// playLTC is unsupported off Windows.
func playLTC(_ context.Context, _ string, _ int, _ func([]int16)) error { return ErrUnsupported }
