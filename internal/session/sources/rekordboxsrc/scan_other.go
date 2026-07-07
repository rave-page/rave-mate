//go:build !windows

package rekordboxsrc

import "fmt"

// RunScan is Windows-only (process-memory reads need OpenProcess/ReadProcessMemory).
func RunScan(string) error { return fmt.Errorf("rbxscan is Windows-only") }
