//go:build !windows

package winshot

import "errors"

// CaptureVRView is Windows-only (user32/gdi32 PrintWindow); the stub always errors elsewhere.
func CaptureVRView(string) error {
	return errors.New("winshot: VR-View capture is Windows-only")
}
