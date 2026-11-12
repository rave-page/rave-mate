//go:build !windows

package featurehost

// raiseTimerResolution is a no-op off Windows (Linux/macOS Go timers are already high-resolution).
func raiseTimerResolution() func() { return func() {} }
