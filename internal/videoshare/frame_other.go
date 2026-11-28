//go:build !windows

package videoshare

func senderFrameCount(string) (int64, bool) { return 0, false }
