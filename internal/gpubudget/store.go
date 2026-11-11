package gpubudget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// CostStore holds VRAM costs LEARNED from real opens, keyed by adapter+driver+op+codec+geometry
// bucket, persisted to a bounded, versioned JSON file in the app data dir. A measured cost overrides
// the computed estimate for the same key. Safe if the file is corrupt/absent (starts empty) and
// bounded (costFileMaxEntries, drop-smallest-churn on overflow) so it can never grow without limit.
type CostStore struct {
	mu   sync.Mutex
	path string
	logf func(string)
	data map[string]costEntry // key -> learned VRAM cost (MB)
	tput map[string]costEntry // key -> measured throughput (pixels/sec), from the micro-benchmark
}

// costEntry is one learned cost (EMA-smoothed MB + the sample count behind it).
type costEntry struct {
	MB      float64 `json:"mb"`
	Samples int     `json:"n"`
}

// costFile is the on-disk shape (versioned for forward migration).
type costFile struct {
	Version     int                  `json:"version"`
	Costs       map[string]costEntry `json:"costs"`
	Throughputs map[string]costEntry `json:"throughputs,omitempty"` // measured pixels/sec per key
}

const (
	costFileVersion    = 1
	costFileMaxEntries = 256 // bounded: a rig has a handful of (adapter,op,codec,bucket) tuples
	costEMAWeight      = 0.3 // new sample weight (smooth driver/measurement jitter)
	costSaneMinMB      = 2   // reject absurd measurements (noise / negative deltas)
	costSaneMaxMB      = 8192
	tputSaneMinPPS     = 1e6  // reject absurd throughput (< 1 Mpps ~ sub-1-fps at any useful size)
	tputSaneMaxPPS     = 2e10 // ~ 8K240 ceiling; above this is a measurement artefact
)

// NewCostStore loads path (safe on corrupt/absent). logf (nil-safe) reports a corrupt file once.
func NewCostStore(path string, logf func(string)) *CostStore {
	s := &CostStore{path: path, logf: logf, data: map[string]costEntry{}, tput: map[string]costEntry{}}
	s.load()
	return s
}

// costKey builds the stable map key. Unknown adapter/driver degrade to "?" so a rig with no DXGI
// identity still shares one bucket rather than scattering keys.
func costKey(a Adapter, op Op, g Geom) string {
	vendor := strings.ToLower(strings.TrimSpace(a.Vendor))
	if vendor == "" {
		vendor = "?"
	}
	driver := strings.TrimSpace(a.Driver)
	if driver == "" {
		driver = "?"
	}
	codec := strings.ToLower(strings.TrimSpace(g.Codec))
	if codec == "" {
		codec = "?"
	}
	return vendor + "|" + driver + "|" + string(op) + "|" + codec + "|" + g.Bucket()
}

// Cost returns the learned cost for (a,op,g) when present, else the computed estimate.
func (s *CostStore) Cost(a Adapter, op Op, g Geom) Cost {
	if s != nil {
		s.mu.Lock()
		e, ok := s.data[costKey(a, op, g)]
		s.mu.Unlock()
		if ok && e.MB > 0 {
			return Cost{MB: e.MB, Measured: true, Source: "measured"}
		}
	}
	return Cost{MB: EstimateCostMB(op, g), Measured: false, Source: "estimate"}
}

// Observe records a measured cost (MB) for (a,op,g): EMA-folds it, bounds the table, persists. Absurd
// values are dropped. Returns true if the table changed (and was persisted).
func (s *CostStore) Observe(a Adapter, op Op, g Geom, mb float64) bool {
	if s == nil || mb < costSaneMinMB || mb > costSaneMaxMB {
		return false
	}
	key := costKey(a, op, g)
	s.mu.Lock()
	e, ok := s.data[key]
	if !ok {
		e = costEntry{MB: mb, Samples: 1}
	} else {
		e.MB = e.MB*(1-costEMAWeight) + mb*costEMAWeight
		e.Samples++
	}
	s.data[key] = e
	s.evictLocked()
	snap := s.snapshotLocked()
	s.mu.Unlock()
	s.save(snap)
	return true
}

// Snapshot returns the learned costs as a sorted slice for explainability (ctl).
func (s *CostStore) Snapshot() []LearnedCost {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := make([]LearnedCost, 0, len(s.data))
	for k, e := range s.data {
		out = append(out, LearnedCost{Key: k, MB: e.MB, Samples: e.Samples})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// LearnedCost is one persisted cost row (ctl / diagnostics).
type LearnedCost struct {
	Key     string
	MB      float64
	Samples int
}

// evictLocked bounds the cost table to costFileMaxEntries.
func (s *CostStore) evictLocked() { evictFewest(s.data) }

// evictFewest bounds m to costFileMaxEntries, dropping the fewest-sampled keys first.
func evictFewest(m map[string]costEntry) {
	if len(m) <= costFileMaxEntries {
		return
	}
	type kv struct {
		k string
		n int
	}
	ks := make([]kv, 0, len(m))
	for k, e := range m {
		ks = append(ks, kv{k, e.Samples})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].n < ks[j].n })
	for i := 0; i < len(ks) && len(m) > costFileMaxEntries; i++ {
		delete(m, ks[i].k)
	}
}

// Throughput returns the measured pixels/sec for (a,op,g), ok=false when unmeasured.
func (s *CostStore) Throughput(a Adapter, op Op, g Geom) (float64, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	e, ok := s.tput[costKey(a, op, g)]
	s.mu.Unlock()
	if ok && e.MB > 0 {
		return e.MB, true
	}
	return 0, false
}

// ObserveThroughput records a measured throughput (pixels/sec) for (a,op,g). EMA-folds + persists.
// Rejects absurd rates. Returns true if the table changed.
func (s *CostStore) ObserveThroughput(a Adapter, op Op, g Geom, pps float64) bool {
	if s == nil || pps < tputSaneMinPPS || pps > tputSaneMaxPPS {
		return false
	}
	key := costKey(a, op, g)
	s.mu.Lock()
	e, ok := s.tput[key]
	if !ok {
		e = costEntry{MB: pps, Samples: 1}
	} else {
		e.MB = e.MB*(1-costEMAWeight) + pps*costEMAWeight
		e.Samples++
	}
	s.tput[key] = e
	evictFewest(s.tput)
	snap := s.snapshotLocked()
	s.mu.Unlock()
	s.save(snap)
	return true
}

func (s *CostStore) snapshotLocked() costFile {
	cp := make(map[string]costEntry, len(s.data))
	for k, v := range s.data {
		cp[k] = v
	}
	var tp map[string]costEntry
	if len(s.tput) > 0 {
		tp = make(map[string]costEntry, len(s.tput))
		for k, v := range s.tput {
			tp[k] = v
		}
	}
	return costFile{Version: costFileVersion, Costs: cp, Throughputs: tp}
}

// load reads the file; a missing/corrupt/overflowing file leaves an empty table (safe).
func (s *CostStore) load() {
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return // absent is normal (first run)
	}
	var cf costFile
	if err := json.Unmarshal(b, &cf); err != nil || cf.Version != costFileVersion {
		if s.logf != nil {
			s.logf("gpubudget: cost file unreadable or wrong version - starting fresh")
		}
		return
	}
	for k, e := range cf.Costs {
		if e.MB >= costSaneMinMB && e.MB <= costSaneMaxMB {
			s.data[k] = e
		}
		if len(s.data) >= costFileMaxEntries {
			break
		}
	}
	for k, e := range cf.Throughputs {
		if e.MB >= tputSaneMinPPS && e.MB <= tputSaneMaxPPS {
			s.tput[k] = e
		}
		if len(s.tput) >= costFileMaxEntries {
			break
		}
	}
}

// save writes the table atomically (temp + rename). Best-effort: a write failure never blocks a route.
func (s *CostStore) save(cf costFile) {
	if s.path == "" {
		return
	}
	b, err := json.Marshal(cf)
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		if s.logf != nil {
			s.logf("gpubudget: cost file write failed: " + err.Error())
		}
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		if dir := filepath.Dir(s.path); dir != "" {
			_ = dir // rename failure (cross-device) is rare on the same dir; drop the temp and move on
		}
	}
}

// MeasureDeltaMB turns a before/after per-process dedicated-VRAM reading (MB) into a learned cost.
// ok=false when the delta is non-positive or absurd (the open did not visibly allocate / noise).
func MeasureDeltaMB(beforeMB, afterMB uint64) (float64, bool) {
	if afterMB <= beforeMB {
		return 0, false
	}
	d := float64(afterMB - beforeMB)
	if d < costSaneMinMB || d > costSaneMaxMB {
		return 0, false
	}
	return d, true
}
