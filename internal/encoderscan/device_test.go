package encoderscan

import "testing"

var (
	nv = AdapterInfo{LUID: "0x00000000_0x0000c34f", Name: "NVIDIA GeForce RTX 4090", VRAMTotalMB: 24576}
	ig = AdapterInfo{LUID: "0x00000000_0x0000d112", Name: "Intel(R) UHD Graphics 770", VRAMTotalMB: 128}
)

func TestNormalizePolicy(t *testing.T) {
	for in, want := range map[string]string{
		"":              PolicyAuto,
		"auto":          PolicyAuto,
		"nonsense":      PolicyAuto,
		" PIN ":         PolicyPin,
		"avoid-busiest": PolicyAvoid,
		"avoid":         PolicyAvoid,
	} {
		if got := NormalizePolicy(in); got != want {
			t.Errorf("NormalizePolicy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveDeviceAuto(t *testing.T) {
	d := ResolveDevice("", "", []AdapterInfo{nv, ig}, Report{})
	if d.Pinned() || d.Index != -1 || d.LUID != "" {
		t.Fatalf("auto must not pin: %+v", d)
	}
}

func TestResolveDevicePin(t *testing.T) {
	ads := []AdapterInfo{nv, ig}
	d := ResolveDevice(PolicyPin, ig.LUID, ads, Report{})
	if !d.Pinned() || d.Index != 1 || d.LUID != ig.LUID || d.Name != ig.Name {
		t.Fatalf("pin iGPU: %+v", d)
	}
	// Index is the DXGI ordinal - adapter 0 is a legal pin.
	if d := ResolveDevice(PolicyPin, nv.LUID, ads, Report{}); !d.Pinned() || d.Index != 0 {
		t.Fatalf("pin adapter 0: %+v", d)
	}
	// Case-insensitive (config may carry an upper-case LUID).
	if d := ResolveDevice(PolicyPin, "0X00000000_0X0000C34F", ads, Report{}); !d.Pinned() || d.Index != 0 {
		t.Fatalf("pin upper-case LUID: %+v", d)
	}
	// A GPU that was removed / renumbered degrades to auto, never to the wrong device.
	if d := ResolveDevice(PolicyPin, "0x00000000_0x0000ffff", ads, Report{}); d.Pinned() {
		t.Fatalf("absent pin must degrade: %+v", d)
	}
	if d := ResolveDevice(PolicyPin, "", ads, Report{}); d.Pinned() {
		t.Fatalf("empty pin must degrade: %+v", d)
	}
	// No adapters at all (non-Windows): auto.
	if d := ResolveDevice(PolicyPin, nv.LUID, nil, Report{}); d.Pinned() {
		t.Fatalf("no adapters: %+v", d)
	}
}

func TestResolveDeviceAvoidBusiest(t *testing.T) {
	ads := []AdapterInfo{nv, ig}
	// NVENC busy with OBS → the iGPU wins.
	r := Report{
		ProtectedAdapter: map[string]bool{nv.LUID: true},
		AdapterEncPct:    map[string]float64{nv.LUID: 62, ig.LUID: 4},
	}
	if d := ResolveDevice(PolicyAvoid, "", ads, r); !d.Pinned() || d.LUID != ig.LUID {
		t.Fatalf("avoid protected NVENC: %+v", d)
	}
	// Nothing protected → least loaded wins (the NVIDIA card here).
	r2 := Report{AdapterEncPct: map[string]float64{nv.LUID: 3, ig.LUID: 40}}
	if d := ResolveDevice(PolicyAvoid, "", ads, r2); !d.Pinned() || d.LUID != nv.LUID {
		t.Fatalf("least loaded: %+v", d)
	}
	// EVERY adapter protected → still returns the least loaded one (a route beats no route).
	r3 := Report{
		ProtectedAdapter: map[string]bool{nv.LUID: true, ig.LUID: true},
		AdapterEncPct:    map[string]float64{nv.LUID: 70, ig.LUID: 12},
	}
	d := ResolveDevice(PolicyAvoid, "", ads, r3)
	if !d.Pinned() || d.LUID != ig.LUID {
		t.Fatalf("all protected: %+v", d)
	}
	// Single GPU: nothing to avoid → auto (no flags, no pointless d3d11 device init).
	if d := ResolveDevice(PolicyAvoid, "", []AdapterInfo{nv}, r); d.Pinned() {
		t.Fatalf("single GPU: %+v", d)
	}
}

func TestLUIDInt64(t *testing.T) {
	v, ok := LUIDInt64("0x00000000_0x0000c34f")
	if !ok || v != 0xc34f {
		t.Fatalf("LUIDInt64 low = %#x ok=%v", v, ok)
	}
	// HighPart is SIGNED in the LUID struct - a negative high part must round-trip.
	v, ok = LUIDInt64("0xffffffff_0x00001234")
	if !ok || v != int64(-1)<<32|0x1234 {
		t.Fatalf("LUIDInt64 negative high = %#x ok=%v", v, ok)
	}
	for _, bad := range []string{"", "0x1", "nope_nope", "0x1_0x2_0x3", "0xzz_0x1"} {
		if _, ok := LUIDInt64(bad); ok {
			t.Errorf("LUIDInt64(%q) accepted", bad)
		}
	}
}

func TestAdapterLoadAndHolders(t *testing.T) {
	r := Report{
		AdapterEncPct: map[string]float64{nv.LUID: 62.4},
		Consumers: []Consumer{
			{Role: "obs", Name: "obs64.exe", Adapter: nv.LUID, EncPct: 60, Critical: true},
			{Role: "parsec", Name: "parsecd.exe", Adapter: nv.LUID, EncPct: 12},
			{Role: "other", Name: "chrome.exe", Adapter: nv.LUID, EncPct: 0.2}, // below threshold
			{Role: "vrchat", Name: "VRChat.exe", Adapter: ig.LUID, EncPct: 20},
		},
	}
	if got := r.AdapterLoad(nv.LUID); got != "enc 62%" {
		t.Fatalf("AdapterLoad = %q", got)
	}
	if got := r.AdapterLoad(ig.LUID); got != "" {
		t.Fatalf("unsampled adapter load = %q, want empty", got)
	}
	if got := r.AdapterHolders(nv.LUID); got != "OBS, Parsec" {
		t.Fatalf("AdapterHolders = %q", got)
	}
	if got := r.AdapterHolders(ig.LUID); got != "VRChat" {
		t.Fatalf("AdapterHolders(igpu) = %q", got)
	}
}

// The selector must never sample for the automatic policy (route opens must not pay PDH).
func TestDeviceSelectorAutoIsFree(t *testing.T) {
	calls := 0
	sel := NewDeviceSelector(func() (string, string) { calls++; return "", "" }, nil)
	d := sel()
	if d.Pinned() || calls != 1 {
		t.Fatalf("auto selector: %+v calls=%d", d, calls)
	}
	// Config is re-read every call, so a settings change lands on the next route.
	if d := sel(); d.Pinned() || calls != 2 {
		t.Fatalf("second call: %+v calls=%d", d, calls)
	}
}
