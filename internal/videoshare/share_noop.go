//go:build !spout

package videoshare

// No backend: no shared texture to hand anyone. Zero-copy consumers keep the readback path.
func senderShare(string) (uint64, uint32, int, int, bool) { return 0, 0, 0, 0, false }
