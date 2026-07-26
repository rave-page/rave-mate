package webui

import (
	"sync"

	"rave.page/mate/internal/zigui"
)

// Retained-doc delta channel, Go side (phase B7 increment ii). Design + rationale:
// .devnotes/ZIG_UI_GUIDE.md "Phase B — B7 (ii) retained-doc delta channel".
//
// The stateless RZW1 path (wire.go zigWire) stays the DEFAULT and the FALLBACK. A patchChan is an
// opt-in optimization tier for ONE high-cadence patch site: it holds the state it last sent, asks
// the generated encoder for the changed field trees only, and hands the lib an RZD1 delta. Every
// doubt returns ok=false and the caller renders through the stateless path for that render.
//
// The state machine is explicit ON PURPOSE (review condition): a delta may only ever be built
// from a state the lib is known to hold.
//
//	psUnseeded --send--> seed doc --ok--> psSeeded --send--> delta doc --ok--> psSeeded
//	psSeeded --any decline/NULL--> psUnseeded (prev CLEARED; next send is a full-doc reseed)
//	3 cap breaches on one surface --> psSticky (stateless for the rest of the session)
//
// There is no path from psUnseeded to a delta: `seed := c.st != psSeeded` is the only branch that
// picks the document kind, and every failure funnel calls unseed().

type patchState uint8

const (
	psUnseeded patchState = iota // no retained state in the lib: the next send MUST be a full seed
	psSeeded                     // the lib holds c.prev; deltas are legal
	psSticky                     // gave up on the channel for this process (cap-breach hysteresis)
)

// patchCapBreachLimit is the hysteresis: a surface whose retained state keeps outgrowing the
// per-slot cap (or that keeps finding the slot table full) stops trying. Retrying a breach every
// tick would burn a full encode + a cap check per tick forever.
const patchCapBreachLimit = 3

// patchChan is one (UI × patch target) channel. T is the surface's state type, O what the lib
// returns for it (HTML string, or the B3 scheduler's fragment list).
type patchChan[T any, O any] struct {
	name  string // counter key (zigui.PatchCounts) + log name
	msgID uint16
	seed  func(v T, handle uint64, loc uint32) []byte
	delta func(v, prev T, handle, base uint64, loc uint32) ([]byte, bool)
	hash  func(v T) uint64
	run   func(doc []byte) (O, zigui.PatchStatus)

	mu       sync.Mutex
	st       patchState
	h        zigui.Handle
	prev     T      // the state the lib holds - ONLY valid in psSeeded
	baseHash uint64 // its fingerprint; the lib refuses a delta based on anything else
	loc      uint32 // i18n generation prev was seeded under
	last     O      // the lib's last output, returned when nothing changed (no ABI call at all)
	breaches int
}

// send merges v through the retained channel. ok=false means the caller must render v through the
// stateless path for THIS render; the channel is unseeded and the next send reseeds.
func (c *patchChan[T, O]) send(v T) (O, bool) {
	var zero O
	if c == nil || !zigui.Available() {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st == psSticky {
		return zero, false
	}
	loc := zigui.LocaleGen()
	if c.st == psSeeded && loc != c.loc {
		c.unseed() // locale switched: every retained string is stale → reseed, don't merge
	}
	if c.h == 0 {
		if c.h = zigui.RetainNew(c.msgID); c.h == 0 {
			zigui.NotePatch(c.name, zigui.PatchCapBreach, true, 0)
			c.breach()
			return zero, false
		}
	}

	seed := c.st != psSeeded
	var doc []byte
	if seed {
		doc = c.seed(v, uint64(c.h), loc)
	} else {
		d, changed := c.delta(v, c.prev, uint64(c.h), c.baseHash, loc)
		if !changed {
			// Byte-identical state: the lib already holds v and its output cannot differ, so
			// nothing crosses the ABI and nothing is re-rendered. This is the channel's cheapest
			// and most common outcome on a ~1 Hz surface that mostly idles.
			return c.last, true
		}
		doc = d
	}
	if len(doc) == 0 {
		zigui.NotePatch(c.name, zigui.PatchMalformed, seed, 0) // encoder refused (over-size)
		c.unseed()
		return zero, false
	}

	out, st := c.run(doc)
	zigui.NotePatch(c.name, st, seed, len(doc))
	if st != zigui.PatchOK {
		// The lib already dropped its state on every non-OK status; mirror that here so the next
		// send is a reseed. Never retry in place - a decline is the reseed's trigger, not a loop.
		// The HANDLE goes too: a desync can mean the slot itself is gone (a stale handle would
		// then make the reseed decline as well), and re-claiming one costs a table scan.
		c.unseed()
		c.release()
		if st == zigui.PatchCapBreach {
			c.breach()
		}
		return zero, false
	}
	c.st = psSeeded
	c.prev = v
	c.baseHash = c.hash(v)
	c.loc = loc
	c.last = out
	return out, true
}

// unseed clears the retained-state belief (prev included, so a stale value can never be diffed
// against). The slot stays claimed: the lib's copy is gone, the handle is still ours.
func (c *patchChan[T, O]) unseed() {
	var zt T
	var zo O
	c.st, c.prev, c.baseHash, c.loc, c.last = psUnseeded, zt, 0, 0, zo
}

// release returns this channel's slot to the table (no-op on a stale handle: the lib checks the
// generation, so freeing one that was already reused cannot touch another channel's slot).
func (c *patchChan[T, O]) release() {
	if c.h == 0 {
		return
	}
	zigui.RetainFree(c.h)
	c.h = 0
}

// breach counts a cap refusal and goes sticky at the limit.
func (c *patchChan[T, O]) breach() {
	c.breaches++
	if c.breaches < patchCapBreachLimit {
		return
	}
	c.st = psSticky
	c.release()
	zigui.NotePatchSticky(c.name)
}

// drop releases the slot entirely: called from every hard resync point (patchMain / DOM replace /
// window child restart / UI teardown). Retained state must never outlive the DOM it describes,
// and a hidden tab has no business holding tens of kB of state.
func (c *patchChan[T, O]) drop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unseed()
	c.release()
}

// state reports the channel's machine state (tests + ctl perf).
func (c *patchChan[T, O]) state() patchState {
	if c == nil {
		return psUnseeded
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

// ── the UI's channels ──

// retainedChans holds one channel per surface that is opted IN. Which surfaces those are was
// decided by the per-surface bench (.devnotes/PHASEB_BASELINE.md "Phase B7 (ii) retained-doc
// channel"), not by where the design guessed it would pay - the design's provisional list was
// five surfaces and three of them lose:
//
//	#ce-topbar    ENABLED  -8.9% dispatch, -67.8% B/op: a drag changes 3 readout fields of ~26
//	Live tick     ENABLED  -9.2% / -67.0%: most of the cockpit is static second to second
//	#twitch-feed  stateless +47.6%: a new row prepends, so the row list is replaced WHOLESALE and
//	                        the delta is the full document plus a clone and two hash walks
//	#midi-monitor stateless +27.2%: same wholesale-list shape (and every row's "ago" text moves)
//	#midi-ctlstat stateless +14.0%: 9 flat fields - the fixed cost (clone + fingerprint + the 43 B
//	                        RZD1 header) is bigger than the whole state
//	#log-view tick stateless +53.0%: the 400-line tail is one list that shifts every tick; cloning
//	                        ~55 kB of strings to then replace the list is pure loss
//
// Their exports + generated walkers STAY (the gates cross-feed all six, which is what makes the
// mutual-unparseability and desync proofs strong, and the bench has to stay re-runnable), they are
// simply not wired at a call site. Per-element list splicing is what would flip the list-shaped
// surfaces; that is a later increment, measured first.
type retainedChans struct {
	ceTopbar *patchChan[ceTopbarSt, string]
	tickLive *patchChan[liveTickSt, []zigui.Frag]
}

// retained lazily builds this UI's channels. One set per UI instance - no globals, so a second
// window, a headless mirror and the test suite never alias each other's slots.
func (u *UI) retained() *retainedChans {
	u.rcMu.Lock()
	defer u.rcMu.Unlock()
	if u.rc == nil {
		u.rc = &retainedChans{
			ceTopbar: &patchChan[ceTopbarSt, string]{
				name: "PatchCueEditTopbar", msgID: wireMsgCeTopbar,
				seed: seedCeTopbar, delta: deltaCeTopbar, hash: hashCeTopbar,
				run: zigui.PatchCueEditTopbar,
			},
			tickLive: &patchChan[liveTickSt, []zigui.Frag]{
				name: "PatchTickLive", msgID: wireMsgTkLive,
				seed: seedTkLive, delta: deltaTkLive, hash: hashTkLive,
				run: zigui.PatchTickLive,
			},
		}
	}
	return u.rc
}

// dropRetained releases every retained slot of this UI. Hard resync point - see patchChan.drop.
func (u *UI) dropRetained() {
	u.rcMu.Lock()
	rc := u.rc
	u.rcMu.Unlock()
	if rc == nil {
		return
	}
	rc.ceTopbar.drop()
	rc.tickLive.drop()
}
