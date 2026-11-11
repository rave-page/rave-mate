package gpubudget

import (
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/gpumem"
)

func hr(free, budget uint64) gpumem.Headroom {
	return gpumem.Headroom{Present: true, FreeMB: free, BudgetMB: budget}
}

func TestEstimateCostOrdering(t *testing.T) {
	g1080 := Geom{W: 1920, H: 1080, Codec: "h264"}
	g2160 := Geom{W: 3840, H: 2160, Codec: "h264"}
	// 4K costs more than 1080p for every op.
	for _, op := range []Op{OpDecode, OpSender, OpEncode} {
		if EstimateCostMB(op, g2160) <= EstimateCostMB(op, g1080) {
			t.Fatalf("%s: 4K cost %.0f !> 1080p cost %.0f", op, EstimateCostMB(op, g2160), EstimateCostMB(op, g1080))
		}
	}
	// A decode pipeline (DPB pool) costs more than a lone sender texture at the same geometry.
	if EstimateCostMB(OpDecode, g2160) <= EstimateCostMB(OpSender, g2160) {
		t.Fatalf("decode %.0f should exceed sender %.0f at 4K", EstimateCostMB(OpDecode, g2160), EstimateCostMB(OpSender, g2160))
	}
	// Degenerate geometry still yields a usable (1080p-class) number.
	if EstimateCostMB(OpDecode, Geom{}) <= 0 {
		t.Fatal("degenerate geometry must still cost >0")
	}
}

func TestReserveGeometryAware(t *testing.T) {
	pol := Policy{}
	if r := Reserve(OpDecode, Geom{W: 1920, H: 1080}, 12000, pol); r != DefaultReserveFloorMB {
		t.Fatalf("1080p reserve = %d, want %d", r, DefaultReserveFloorMB)
	}
	r1080 := Reserve(OpDecode, Geom{W: 1920, H: 1080}, 12000, pol)
	r2160 := Reserve(OpDecode, Geom{W: 3840, H: 2160}, 12000, pol)
	if r2160 <= r1080 {
		t.Fatalf("4K reserve %d must exceed 1080p reserve %d", r2160, r1080)
	}
	// Configured floor raises the baseline but geometry can still raise it further.
	rc := Reserve(OpDecode, Geom{W: 1280, H: 720}, 12000, Policy{ReserveCfgMB: 1000})
	if rc != 1000 {
		t.Fatalf("configured reserve floor = %d, want 1000", rc)
	}
	rc4k := Reserve(OpDecode, Geom{W: 3840, H: 2160}, 12000, Policy{ReserveCfgMB: 1000})
	if rc4k < 1000 {
		t.Fatalf("4K reserve %d must not drop below configured floor 1000", rc4k)
	}
}

func TestSelfCapAuto(t *testing.T) {
	// 12 GB card: 12% = 1440 < 1536 => 1440.
	if c := SelfCapMB(12000, Policy{}); c != 1440 {
		t.Fatalf("12GB self-cap = %d, want 1440", c)
	}
	// 24 GB card: 12% = 2880 > 1536 => 1536 absolute cap wins.
	if c := SelfCapMB(24000, Policy{}); c != autoCapAbsMB {
		t.Fatalf("24GB self-cap = %d, want %d", c, autoCapAbsMB)
	}
	// Unknown budget => absolute cap.
	if c := SelfCapMB(0, Policy{}); c != autoCapAbsMB {
		t.Fatalf("unknown-budget self-cap = %d, want %d", c, autoCapAbsMB)
	}
	// Explicit policy wins.
	if c := SelfCapMB(12000, Policy{HardCapMB: 500}); c != 500 {
		t.Fatalf("explicit self-cap = %d, want 500", c)
	}
}

func TestAdmitFailOpenWhenHeadroomAbsent(t *testing.T) {
	d := Admit(Inputs{}, OpSender, Geom{W: 1920, H: 1080}, Cost{MB: 50}, Policy{})
	if !d.Admit {
		t.Fatalf("absent headroom must fail open, got deny: %s", d.Reason)
	}
}

func TestAdmitReserveGate(t *testing.T) {
	g := Geom{W: 3840, H: 2160, Codec: "h264"}
	cost := Cost{MB: 300}
	// free 1200, reserve for 4K = 1152: 1200-300=900 < 1152 => deny.
	if d := Admit(Inputs{Headroom: hr(1200, 12000)}, OpDecode, g, cost, Policy{}); d.Admit {
		t.Fatalf("expected deny on tight headroom: %s", d.Reason)
	}
	// free 2000: 2000-300=1700 >= 1152 => admit (self unknown, cap axis skipped).
	if d := Admit(Inputs{Headroom: hr(2000, 12000)}, OpDecode, g, cost, Policy{}); !d.Admit {
		t.Fatalf("expected admit on ample headroom: %s", d.Reason)
	}
}

func TestAdmitSelfCapGate(t *testing.T) {
	g := Geom{W: 1920, H: 1080, Codec: "h264"}
	cost := Cost{MB: 200}
	// Ample free, but self already at 1400 on a 12GB card (cap 1440): 1400+200>1440 => deny.
	in := Inputs{Headroom: hr(8000, 12000), SelfUsedMB: 1400, SelfKnown: true}
	if d := Admit(in, OpEncode, g, cost, Policy{}); d.Admit {
		t.Fatalf("expected deny on self-cap breach: %s", d.Reason)
	}
	// Same, but self unknown => cap axis skipped => admit.
	in2 := Inputs{Headroom: hr(8000, 12000), SelfUsedMB: 1400, SelfKnown: false}
	if d := Admit(in2, OpEncode, g, cost, Policy{}); !d.Admit {
		t.Fatalf("self unknown must skip cap axis: %s", d.Reason)
	}
}

func TestCostStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gpucost.json")
	a := Adapter{Vendor: "nvidia", Driver: "32.0.16.1088"}
	g := Geom{W: 3840, H: 2160, Codec: "h264"}

	s := NewCostStore(path, nil)
	// Before any observation: estimate.
	if c := s.Cost(a, OpDecode, g); c.Measured {
		t.Fatal("unmeasured key must return an estimate")
	}
	if !s.Observe(a, OpDecode, g, 420) {
		t.Fatal("observe should accept a sane value")
	}
	if c := s.Cost(a, OpDecode, g); !c.Measured || c.MB != 420 {
		t.Fatalf("first measured cost = %+v, want 420 measured", c)
	}
	// Absurd values rejected.
	if s.Observe(a, OpDecode, g, 0) || s.Observe(a, OpDecode, g, 99999) {
		t.Fatal("absurd measurements must be rejected")
	}
	// Reload from disk keeps the learned value.
	s2 := NewCostStore(path, nil)
	if c := s2.Cost(a, OpDecode, g); !c.Measured || c.MB != 420 {
		t.Fatalf("reloaded cost = %+v, want 420 measured", c)
	}
}

func TestCostStoreCorruptSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gpucost.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logged string
	s := NewCostStore(path, func(m string) { logged = m })
	if logged == "" {
		t.Fatal("corrupt file should log once")
	}
	// Still usable (empty table => estimates), and a new observation persists cleanly.
	a := Adapter{Vendor: "amd"}
	g := Geom{W: 1920, H: 1080}
	if c := s.Cost(a, OpSender, g); c.Measured {
		t.Fatal("corrupt load must leave an empty table")
	}
	if !s.Observe(a, OpSender, g, 60) {
		t.Fatal("observe after corrupt load must work")
	}
	if c := NewCostStore(path, nil).Cost(a, OpSender, g); !c.Measured {
		t.Fatal("rewritten file must be valid on reload")
	}
}

func TestCostStoreBounded(t *testing.T) {
	s := NewCostStore("", nil) // no path: pure in-memory
	g := Geom{W: 1920, H: 1080}
	for i := 0; i < costFileMaxEntries+50; i++ {
		s.Observe(Adapter{Vendor: "v", Driver: string(rune('a'+i%26)) + itoa(i)}, OpEncode, g, 100)
	}
	if n := len(s.Snapshot()); n > costFileMaxEntries {
		t.Fatalf("store grew unbounded: %d entries", n)
	}
}

func TestThroughputStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gpucost.json")
	a := Adapter{Vendor: "nvidia", Driver: "x"}
	g := Geom{W: 3840, H: 2160, Codec: "h264"}
	s := NewCostStore(path, nil)
	if _, ok := s.Throughput(a, OpEncode, g); ok {
		t.Fatal("unmeasured throughput must be absent")
	}
	if !s.ObserveThroughput(a, OpEncode, g, 900e6) {
		t.Fatal("sane throughput must be accepted")
	}
	if s.ObserveThroughput(a, OpEncode, g, 1) { // < 1 Mpps rejected
		t.Fatal("absurd throughput must be rejected")
	}
	// Survives reload AND does not clobber costs.
	s.Observe(a, OpEncode, g, 300)
	s2 := NewCostStore(path, nil)
	if v, ok := s2.Throughput(a, OpEncode, g); !ok || v != 900e6 {
		t.Fatalf("reloaded throughput = %v ok=%v", v, ok)
	}
	if c := s2.Cost(a, OpEncode, g); !c.Measured || c.MB != 300 {
		t.Fatalf("cost clobbered by throughput: %+v", c)
	}
}

func TestMeasureDelta(t *testing.T) {
	if mb, ok := MeasureDeltaMB(1000, 1420); !ok || mb != 420 {
		t.Fatalf("delta = %.0f ok=%v, want 420 true", mb, ok)
	}
	if _, ok := MeasureDeltaMB(1420, 1000); ok {
		t.Fatal("negative delta must be rejected")
	}
	if _, ok := MeasureDeltaMB(1000, 1001); ok {
		t.Fatal("sub-threshold delta must be rejected")
	}
}

func TestBudgetWiring(t *testing.T) {
	free := uint64(900)
	selfMB := uint64(0)
	b := New(Options{
		Headroom: func() gpumem.Headroom { return hr(free, 12000) },
		Self:     func() (uint64, bool) { return selfMB, true },
		Adapter:  func() Adapter { return Adapter{Vendor: "nvidia", Driver: "x"} },
		Store:    NewCostStore("", nil),
	})
	// 4K decode on 900 free => deny (reserve 1152).
	if d := b.AdmitDecode(3840, 2160, 60, "h264"); d.Admit {
		t.Fatalf("expected deny at 900 free: %s", d.Reason)
	}
	// Headroom recovers => admit, and Report reflects the last decision.
	free = 4000
	if d := b.AdmitDecode(3840, 2160, 60, "h264"); !d.Admit {
		t.Fatalf("expected admit at 4000 free: %s", d.Reason)
	}
	rep := b.Report()
	if rep == "" || !contains(rep, "vram budget:") {
		t.Fatalf("report missing budget line:\n%s", rep)
	}
}

// --- tiny helpers (avoid strconv/strings imports leaking into the file's intent) ---

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
