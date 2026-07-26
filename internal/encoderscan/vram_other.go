//go:build !windows

package encoderscan

// adapterVRAMFree has no DXGI off Windows → free VRAM stays unknown and the planner skips the
// VRAM ceiling (Device.VRAMFree = -1).
func adapterVRAMFree() map[string]float64 { return nil }
