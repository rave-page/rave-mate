package medialink

import "sync"

// udpstate.go holds the UDP conn's two bounded receive/send-recovery data structures (Unit 2):
//   - replayWin: an SRTP-style sliding replay window (§2.5 / RFC 3711) over the per-direction
//     datagram seq. It rejects replays + stale datagrams and measures datagram loss (the congestion
//     controller's primary input). Fixed-size bitmap → O(1) memory, never grows with traffic.
//   - dgRing: the fragment-NACK retransmit ring: recently-sent datagrams kept verbatim so a NACK
//     re-sends the exact bytes (same seq → the peer's replay window dedups). Bounded by datagram
//     COUNT and total BYTES (drop-oldest) and by a per-datagram deadline (gc).

// ── replay window + datagram-loss accounting ─────────────────────────────────────

const replayWindowBits = 4096 // datagrams of reorder tolerance (× 1200 B ≈ 4.9 MB in flight)

type acceptStatus int

const (
	stNew acceptStatus = iota // STRICTLY NEWEST seq (advanced the highest) - the ONLY kind allowed
	//                              to rebind the remote / refresh liveness / signal reach (a replayed
	//                              capture can never forge one, so it cannot hijack the send target)
	stFill   // new-but-not-newest (gap-filling retransmit): deliver it, but it must NOT rebind
	stDup    // already received (replay / duplicate retransmit) - drop, change nothing
	stTooOld // below the window - drop, change nothing
)

// replayWin is a fixed sliding window of seen datagram seqs. Safe for the single recvLoop caller;
// lossFraction is read from other goroutines, so the counters it reads are mutex-guarded.
type replayWin struct {
	mu      sync.Mutex
	started bool
	highest uint64
	first   uint64
	bits    []uint64 // replayWindowBits/64 words, indexed by seq % replayWindowBits
	recv    uint64   // unique datagrams accepted (stNew)

	// interval anchors for windowed loss (lossFraction consumes + advances them).
	lastHighest uint64
	lastRecv    uint64
}

func newReplayWin() *replayWin {
	return &replayWin{bits: make([]uint64, replayWindowBits/64)}
}

func (w *replayWin) idx(seq uint64) (word int, mask uint64) {
	b := seq % replayWindowBits
	return int(b / 64), 1 << (b % 64)
}

func (w *replayWin) getBit(seq uint64) bool {
	word, mask := w.idx(seq)
	return w.bits[word]&mask != 0
}

func (w *replayWin) setBit(seq uint64) {
	word, mask := w.idx(seq)
	w.bits[word] |= mask
}

func (w *replayWin) clearBit(seq uint64) {
	word, mask := w.idx(seq)
	w.bits[word] &^= mask
}

// accept classifies seq and records it. A forward jump ≥ the window clears the whole bitmap (every
// slot is stale); a smaller jump clears only the slots it scrolls past.
func (w *replayWin) accept(seq uint64) acceptStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		w.started, w.highest, w.first = true, seq, seq
		w.lastHighest = seq
		w.setBit(seq)
		w.recv++
		return stNew
	}
	switch {
	case seq > w.highest:
		if seq-w.highest >= replayWindowBits {
			for i := range w.bits {
				w.bits[i] = 0
			}
		} else {
			for s := w.highest + 1; s <= seq; s++ {
				w.clearBit(s) // scroll-in slots are not-yet-received
			}
		}
		w.highest = seq
		w.setBit(seq)
		w.recv++
		return stNew
	case w.highest-seq >= replayWindowBits:
		return stTooOld
	case w.getBit(seq):
		return stDup
	default:
		w.setBit(seq) // late / retransmit filling a gap (new, but NOT the newest - must not rebind)
		w.recv++
		return stFill
	}
}

// lossFraction returns datagram loss over the interval since the last call (0..1), advancing the
// anchors. expected = highest advance; received = unique datagrams in that span.
func (w *replayWin) lossFraction() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return 0
	}
	expInt := w.highest - w.lastHighest
	recInt := w.recv - w.lastRecv
	w.lastHighest, w.lastRecv = w.highest, w.recv
	if expInt == 0 || recInt >= expInt {
		return 0
	}
	return float64(expInt-recInt) / float64(expInt)
}

// ── retransmit ring (datagram-level) ─────────────────────────────────────────────

type ringItem struct {
	buf      []byte
	deadline int64
}

// dgRing keeps recently-sent datagrams for fragment-NACK retransmit, keyed by dgKey. Bounded by
// COUNT and BYTES (drop-oldest) and by per-datagram deadline (gc). Safe for concurrent use (the
// send path adds, the recvLoop gets, maintLoop gcs).
type dgRing struct {
	mu        sync.Mutex
	items     map[uint64]*ringItem
	order     []uint64
	bytes     int
	maxFrames int
	maxBytes  int
}

func newDgRing(maxFrames, maxBytes int) *dgRing {
	return &dgRing{items: map[uint64]*ringItem{}, maxFrames: maxFrames, maxBytes: maxBytes}
}

// add stores the datagram bytes under key with a retransmit deadline. The caller (WriteFrame)
// passes a FRESH, never-mutated encode buffer that it also hands to the pacer read-only, so the ring
// shares it rather than copying (both uses are read-only after encode - no per-datagram copy on the
// hot path).
func (r *dgRing) add(key uint64, buf []byte, deadline int64) {
	r.mu.Lock()
	if old, ok := r.items[key]; ok {
		r.bytes -= len(old.buf)
	} else {
		r.order = append(r.order, key)
	}
	r.items[key] = &ringItem{buf: buf, deadline: deadline}
	r.bytes += len(buf)
	for (len(r.items) > r.maxFrames || r.bytes > r.maxBytes) && len(r.order) > 0 {
		k := r.order[0]
		r.order = r.order[1:]
		if it := r.items[k]; it != nil {
			r.bytes -= len(it.buf)
			delete(r.items, k)
		}
	}
	r.mu.Unlock()
}

// get returns the stored datagram for key (a reference - do not mutate), ok=false if evicted.
func (r *dgRing) get(key uint64) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[key]
	if !ok {
		return nil, false
	}
	return it.buf, true
}

// gc drops datagrams past their retransmit deadline (abandoned - the receiver resyncs via the
// router's frame NACK/PLI).
func (r *dgRing) gc(now int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.order[:0]
	for _, k := range r.order {
		it := r.items[k]
		if it == nil {
			continue
		}
		if it.deadline <= now {
			r.bytes -= len(it.buf)
			delete(r.items, k)
			continue
		}
		kept = append(kept, k)
	}
	r.order = kept
}
