package gpubudget

import (
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/gpumem"
)

// DefaultSelfNames are the process-image substrings that belong to rave-mate (daemon + supervised
// children: media featurehost, the zigenc encode/decode child, the vfx child, the Zig window shell,
// the probe worker). Matched case-insensitively against each process name in the GPU sweep.
func DefaultSelfNames() []string {
	return []string{"rave-mate", "rave-shell", "rave-probe"}
}

// SelfFootprint samples rave-mate's OWN GPU footprint (sum of dedicated VRAM across its processes, on
// every adapter) via the expensive per-process gpumem sweep, ASYNC-refreshed on a TTL so the
// admission path never blocks on a sweep. Off Windows the sweep is a no-op => footprint unknown
// (SelfKnown=false), which fails open on the self-cap axis. Safe for concurrent use.
type SelfFootprint struct {
	s     gpumem.Sampler
	names []string
	ttl   time.Duration

	mu         sync.Mutex
	mb         uint64
	known      bool
	at         time.Time
	refreshing bool
	now        func() time.Time
}

// NewSelfFootprint builds the sampler. names default to DefaultSelfNames; ttl defaults to 30s.
func NewSelfFootprint(s gpumem.Sampler, names []string, ttl time.Duration) *SelfFootprint {
	if len(names) == 0 {
		names = DefaultSelfNames()
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &SelfFootprint{s: s, names: names, ttl: ttl, now: time.Now}
}

// Get returns the last sampled footprint (MB, known) and kicks an async refresh when stale. It never
// blocks on the sweep: the first calls return (0,false) until the first background sweep lands.
func (f *SelfFootprint) Get() (uint64, bool) {
	f.mu.Lock()
	mb, known, stale := f.mb, f.known, f.now().Sub(f.at) > f.ttl
	start := stale && !f.refreshing && f.s != nil
	if start {
		f.refreshing = true
	}
	f.mu.Unlock()
	if start {
		go f.refresh()
	}
	return mb, known
}

// refresh runs one sweep and folds the result (the only place the expensive SampleProcesses runs).
func (f *SelfFootprint) refresh() {
	defer func() {
		f.mu.Lock()
		f.refreshing = false
		f.mu.Unlock()
	}()
	aps, err := f.s.SampleProcesses(selfSweepTopN)
	if err != nil {
		return
	}
	mb := sumSelf(aps, f.names)
	f.mu.Lock()
	f.mb, f.known, f.at = mb, true, f.now()
	f.mu.Unlock()
}

// selfSweepTopN is generous so rave-mate's children are not missed below the top consumers.
const selfSweepTopN = 24

// sumSelf totals the MB of every process whose name matches one of names, across all adapters.
func sumSelf(aps []gpumem.AdapterProcs, names []string) uint64 {
	var total uint64
	for _, ap := range aps {
		for _, p := range ap.Procs {
			if matchesName(p.Name, names) {
				total += p.MB
			}
		}
	}
	return total
}

func matchesName(name string, names []string) bool {
	n := strings.ToLower(name)
	for _, want := range names {
		if strings.Contains(n, strings.ToLower(want)) {
			return true
		}
	}
	return false
}
