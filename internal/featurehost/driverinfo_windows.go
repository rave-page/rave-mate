//go:build windows

package featurehost

import (
	"strings"

	reg "golang.org/x/sys/windows/registry" // aliased: package-level `registry` map exists in runtime.go
)

// primaryGPUDriverVersion reads the display-adapter class DriverVersion from the registry (best
// effort, "" when unavailable). Cheap: one registry walk, no D3D/DXGI/vendor driver entry. It picks
// the adapter subkey whose DriverDesc vendor-matches adapterName, else the first enumerated adapter.
// Used only to discriminate the throughput/cost cache key across driver updates.
func primaryGPUDriverVersion(adapterName string) string {
	const classKey = `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`
	k, err := reg.OpenKey(reg.LOCAL_MACHINE, classKey, reg.READ)
	if err != nil {
		return ""
	}
	defer k.Close()
	subs, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return ""
	}
	wantVendor := gpuVendorToken(adapterName)
	var first string
	for _, s := range subs {
		if len(s) != 4 { // adapter subkeys are "0000","0001",... (skip "Properties" etc.)
			continue
		}
		sk, err := reg.OpenKey(k, s, reg.READ)
		if err != nil {
			continue
		}
		ver, _, _ := sk.GetStringValue("DriverVersion")
		desc, _, _ := sk.GetStringValue("DriverDesc")
		sk.Close()
		if ver == "" {
			continue
		}
		if first == "" {
			first = ver
		}
		if wantVendor != "" && strings.Contains(strings.ToLower(desc), wantVendor) {
			return ver
		}
	}
	return first
}

// gpuVendorToken returns a lowercase vendor substring to match a DriverDesc against ("" if unknown).
func gpuVendorToken(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "nvidia"), strings.Contains(n, "geforce"), strings.Contains(n, "rtx"):
		return "nvidia"
	case strings.Contains(n, "amd"), strings.Contains(n, "radeon"):
		return "amd"
	case strings.Contains(n, "intel"), strings.Contains(n, "arc"):
		return "intel"
	}
	return ""
}
