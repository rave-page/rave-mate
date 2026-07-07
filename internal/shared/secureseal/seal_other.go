//go:build !windows

package secureseal

const available = false

// Seal returns ErrNoSecureStore - no OS secret API on this platform.
func Seal([]byte) ([]byte, error) { return nil, ErrNoSecureStore }

// Unseal returns ErrNoSecureStore - no OS secret API on this platform.
func Unseal([]byte) ([]byte, error) { return nil, ErrNoSecureStore }
