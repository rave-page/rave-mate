//go:build !spout

package videoshare

// No backend: no frame counter to read.
func senderFrame(string) (int64, float64, bool) { return 0, 0, false }
