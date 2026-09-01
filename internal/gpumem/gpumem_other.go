//go:build !windows

package gpumem

// stubSampler reports no adapters: the watchdog stays inert off Windows (fail open). D3DKMT
// is Windows-only; a macOS/Linux VRAM path (Metal/DRM) is out of scope for the live-set rig.
type stubSampler struct{}

func newSampler() Sampler { return stubSampler{} }

func (stubSampler) Sample() ([]AdapterUsage, error) { return nil, nil }

func (stubSampler) SampleProcesses(int) ([]AdapterProcs, error) { return nil, nil }
