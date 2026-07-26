package videoshare

import (
	"sync"
	"time"
)

// scan.go - the sender-registry scan, cached. mediaroute re-scans every 2 s and asked the backend
// for the name list plus one size per name; each of those built and released a Spout runtime object
// (1+2N COM objects per scan, forever, on the machine that is already busy encoding). Now ONE
// backend call fetches names+dimensions together and a short TTL folds the per-name size lookups
// of a single scan pass into it.

const (
	scanTTL     = time.Second // cache lifetime; mediaroute scans every 2 s → one backend call per scan
	scanMaxKeep = 256         // bound on cached entries (Spout's registry ceiling)
)

// SenderInfo is one registered video-share sender: name + its current dimensions (0 when the
// registry has no size for it yet).
type SenderInfo struct {
	Name string
	W, H int
}

// senderScan is the TTL cache around the backend registry query.
type senderScan struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	fetch func() []SenderInfo

	list  []SenderInfo
	at    time.Time
	fresh bool
	hits  uint64 // backend calls (telemetry + test assertion)
}

// all returns the cached list, refreshing when stale.
func (s *senderScan) all() []SenderInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allLocked()
}

func (s *senderScan) allLocked() []SenderInfo {
	if s.fresh && s.now().Sub(s.at) < s.ttl {
		return s.list
	}
	return s.refreshLocked()
}

func (s *senderScan) refreshLocked() []SenderInfo {
	l := s.fetch()
	if len(l) > scanMaxKeep {
		l = l[:scanMaxKeep]
	}
	s.list, s.at, s.fresh = l, s.now(), true
	s.hits++
	return l
}

// size looks a name's dimensions up in the cache; an unknown name forces ONE refresh (a sender
// that just appeared must resolve immediately - the route opens off this call).
func (s *senderScan) size(name string) (int, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, h, ok := find(s.allLocked(), name); ok {
		return w, h, true
	}
	return find(s.refreshLocked(), name)
}

func find(l []SenderInfo, name string) (int, int, bool) {
	for _, si := range l {
		if si.Name == name {
			return si.W, si.H, si.W > 0 && si.H > 0
		}
	}
	return 0, 0, false
}

var senders = &senderScan{ttl: scanTTL, now: time.Now, fetch: scanSenders}

// Senders returns every registered sender with its dimensions (cached ≤ scanTTL).
func Senders() []SenderInfo { return senders.all() }

// ListSenders returns the currently registered video-share sender names (nil without a backend).
func ListSenders() []string {
	l := senders.all()
	if len(l) == 0 {
		return nil
	}
	out := make([]string, 0, len(l))
	for _, si := range l {
		out = append(out, si.Name)
	}
	return out
}

// SenderSize returns a named sender's current dimensions (ok=false when absent / no backend).
func SenderSize(name string) (w, h int, ok bool) { return senders.size(name) }
