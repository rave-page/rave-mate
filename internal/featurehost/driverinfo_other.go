//go:build !windows

package featurehost

// primaryGPUDriverVersion is Windows-only (registry); elsewhere the cache keys on the adapter model
// name alone.
func primaryGPUDriverVersion(string) string { return "" }
