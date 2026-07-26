//go:build !windows

package encoderscan

// sampleGPU has no vendor-neutral per-process encode-utilization source off Windows → family-only
// protection (OBS/Parsec config still detected). Returns no samples.
func sampleGPU() ([]GPUSample, error) { return nil, nil }

// gpuDiagNote is Windows-only diagnostics; no GPU sampler off Windows.
func gpuDiagNote() string { return "" }

// adapterNames has no DXGI off Windows.
func adapterNames() map[string]string { return nil }

// enumAdapters has no DXGI off Windows → no adapter list, so every device policy degrades to
// PolicyAuto (the engine's own default device).
func enumAdapters() ([]AdapterInfo, error) { return nil, nil }
