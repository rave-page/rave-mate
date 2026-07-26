package encoderscan

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// device.go - WHICH GPU medialink encodes on. `Adapters()` exposes the DXGI enumeration (index =
// the ordinal ffmpeg's d3d11va/-gpu flags take, LUID = the key PDH's GPU-Engine counters use);
// `ResolveDevice` turns the user's policy into one concrete adapter; `NewDeviceSelector` wraps both
// in a TTL-cached callback the media plane calls at route open (never on a render lane).

// Device policies (config MediaLink.devicePolicy; "" == PolicyAuto).
const (
	PolicyAuto  = "auto"          // engine/driver picks (adapter 0) - emits NO device flags
	PolicyPin   = "pin"           // always the configured adapter
	PolicyAvoid = "avoid-busiest" // least-loaded adapter, protected ones (OBS/Parsec) excluded
)

// NormalizePolicy maps a config value to a known policy ("" / junk → PolicyAuto).
func NormalizePolicy(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case PolicyPin:
		return PolicyPin
	case PolicyAvoid, "avoid", "avoidbusiest":
		return PolicyAvoid
	default:
		return PolicyAuto
	}
}

// AdapterInfo is one enumerated GPU adapter.
type AdapterInfo struct {
	LUID        string  // "0xHIGH_0xLOW" lowercased - matches parseGPUEngineInstance's luid key
	Name        string  // adapter description ("NVIDIA GeForce RTX 3060", "AMD Radeon ...")
	VRAMTotalMB float64 // dedicated video memory MB (0 = unknown / software adapter)
}

// Adapters returns the machine's hardware GPU adapters in DXGI order. Empty off Windows or when
// DXGI fails - callers then fall back to PolicyAuto (no device pinning).
func Adapters() []AdapterInfo {
	a, _ := enumAdapters()
	return a
}

// DeviceChoice is a resolved encode-device preference. LUID "" / Index -1 = engine default: the
// caller emits no device flags at all (byte-identical to the pre-device-selection behaviour).
type DeviceChoice struct {
	LUID   string // adapter LUID key ("0xHIGH_0xLOW"); "" = engine default
	Index  int    // DXGI ordinal for CLI encoders; -1 = none/unknown
	Name   string // adapter description (diagnostic/UI)
	Reason string // why this adapter (log/UI)
}

// Pinned reports whether a concrete adapter was resolved (i.e. device flags should be emitted).
func (d DeviceChoice) Pinned() bool { return d.LUID != "" && d.Index >= 0 }

// ResolveDevice picks the encode adapter for a policy. adapters = Adapters(); r = a live Scan
// (only read for PolicyAvoid, so callers may pass a zero Report otherwise). Never errors: an
// unresolvable preference degrades to the engine default with the reason recorded.
func ResolveDevice(policy, pin string, adapters []AdapterInfo, r Report) DeviceChoice {
	auto := func(reason string) DeviceChoice { return DeviceChoice{Index: -1, Reason: reason} }
	switch NormalizePolicy(policy) {
	case PolicyPin:
		key := strings.ToLower(strings.TrimSpace(pin))
		if key == "" {
			return auto("no adapter pinned")
		}
		for i, a := range adapters {
			if strings.EqualFold(a.LUID, key) {
				return DeviceChoice{LUID: a.LUID, Index: i, Name: a.Name, Reason: "pinned by the user"}
			}
		}
		return auto("pinned adapter " + key + " is not present")
	case PolicyAvoid:
		if len(adapters) < 2 {
			return auto("only one GPU - nothing to avoid")
		}
		best, bestIdx, bestLoad := AdapterInfo{}, -1, 0.0
		for i, a := range adapters {
			if r.ProtectedAdapter[a.LUID] {
				continue // OBS/Parsec live on it - never contend
			}
			load := r.AdapterEncPct[a.LUID]
			if bestIdx < 0 || load < bestLoad {
				best, bestIdx, bestLoad = a, i, load
			}
		}
		if bestIdx < 0 { // every adapter is protected: take the least-loaded one anyway
			for i, a := range adapters {
				load := r.AdapterEncPct[a.LUID]
				if bestIdx < 0 || load < bestLoad {
					best, bestIdx, bestLoad = a, i, load
				}
			}
			return DeviceChoice{LUID: best.LUID, Index: bestIdx, Name: best.Name,
				Reason: fmt.Sprintf("every GPU is busy with a protected encoder - least loaded (%.0f%%)", bestLoad)}
		}
		return DeviceChoice{LUID: best.LUID, Index: bestIdx, Name: best.Name,
			Reason: fmt.Sprintf("least video-encode load (%.0f%%)", bestLoad)}
	}
	return auto("automatic")
}

// deviceTTL bounds how often a selector re-samples. Route opens are rare; PDH costs ~300 ms, so the
// cache exists to keep a burst of routes from paying it N times, not to hide staleness.
const deviceTTL = 5 * time.Second

// NewDeviceSelector returns a cached resolver for the media plane: cfg supplies the LIVE policy +
// pinned adapter (read every call, so a settings change applies to the next route), obsEnc is the
// OBS surface for PolicyAvoid's protection set (may be nil). Safe from any goroutine; never called
// on a UI lane.
func NewDeviceSelector(cfg func() (policy, pin string), obsEnc OBSEncoderFunc) func() DeviceChoice {
	var mu sync.Mutex
	var (
		adapters []AdapterInfo
		report   Report
		at       time.Time
	)
	return func() DeviceChoice {
		policy, pin := PolicyAuto, ""
		if cfg != nil {
			policy, pin = cfg()
		}
		p := NormalizePolicy(policy)
		if p == PolicyAuto {
			return DeviceChoice{Index: -1, Reason: "automatic"}
		}
		mu.Lock()
		defer mu.Unlock()
		if at.IsZero() || time.Since(at) > deviceTTL {
			adapters = Adapters()
			if p == PolicyAvoid {
				report = Detect(obsEnc)
			}
			at = time.Now()
		}
		return ResolveDevice(p, pin, adapters, report)
	}
}

// LUIDInt64 packs an adapter LUID key ("0xHIGH_0xLOW", as produced by enumAdapters and PDH) into the
// int64 the native encoder shim compares against DXGI_ADAPTER_DESC1.AdapterLUID. ok=false on junk.
func LUIDInt64(key string) (int64, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(key)), "_")
	if len(parts) != 2 {
		return 0, false
	}
	hi, err := strconv.ParseUint(strings.TrimPrefix(parts[0], "0x"), 16, 32)
	if err != nil {
		return 0, false
	}
	lo, err := strconv.ParseUint(strings.TrimPrefix(parts[1], "0x"), 16, 32)
	if err != nil {
		return 0, false
	}
	return int64(int32(uint32(hi)))<<32 | int64(uint32(lo)), true
}

// AdapterLoad renders an adapter's live encode load for the UI ("enc 42%", "" when unsampled).
func (r Report) AdapterLoad(luid string) string {
	pct, ok := r.AdapterEncPct[luid]
	if !ok {
		return ""
	}
	return fmt.Sprintf("enc %.0f%%", pct)
}

// AdapterHolders names the detected consumers encoding on an adapter ("OBS", "Parsec, VRChat"),
// "" when nothing was detected there - the UI's "who's on it" column.
func (r Report) AdapterHolders(luid string) string {
	var names []string
	seen := map[string]bool{}
	for _, c := range r.Consumers {
		if c.Adapter != luid || c.EncPct < encThreshold {
			continue
		}
		n := c.Role
		switch c.Role {
		case "obs":
			n = "OBS"
		case "parsec":
			n = "Parsec"
		case "vrchat":
			n = "VRChat"
		case "other":
			n = c.Name
		}
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}
