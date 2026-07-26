//go:build zigui

package webui

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Retained-doc delta channel gates (B7 increment ii). What these have to prove, over and above the
// stateless three-way gate:
//
//	1. the two document kinds are mutually unparseable (a delta MUST NOT reach a decoder that
//	   reads "absent" as zero instead of keep, and a full doc must not reach the merge path)
//	2. Go's and Zig's state fingerprints agree - proven by EXECUTION, since a seed only succeeds
//	   when Zig's own hash of the decoded state equals the one Go put in the header
//	3. sequence goldens: N random fixture→fixture delta applications, byte-equal to the stateless
//	   render oracle at EVERY step (not just at the end)
//	4. every decline path declines cleanly and reseeds to convergence, including the desync where
//	   the slot vanishes behind Go's back
//	5. the cap breach is its own status AND its own hysteresis (3 → sticky stateless)

// patchSurface is one retained surface under test, reduced to the four things a gate needs: a
// corpus of states, the encoders, the export and the stateless oracle for a state.
type patchSurface[T any] struct {
	name   string
	msgID  uint16
	states []T
	seed   func(v T, handle uint64, loc uint32) []byte
	delta  func(v, prev T, handle, base uint64, loc uint32) ([]byte, bool)
	hash   func(v T) uint64
	patch  func(doc []byte) (string, zigui.PatchStatus)
	oracle func(v T) string // the stateless render (Go renderer == v1 == v2 by the wire gates)
	full   func(v T) []byte // the stateless RZW1 document for the same state
}

// sortedStates flattens a fixture map deterministically (map order would defeat the fixed seed).
func sortedStates[T any](m map[string]T) []T {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]T, 0, len(names))
	for _, n := range names {
		out = append(out, m[n])
	}
	return out
}

func twFeedStates() []twFeedState {
	var out []twFeedState
	for _, st := range sortedStates(twFixtures()) {
		out = append(out, st.Feed)
	}
	out = append(out, twFeedState{Empty: "no messages yet"}) // the emptied-list arm (Clear)
	return out
}

func midiMonStates() []midiMonLines {
	var out []midiMonLines
	for _, st := range sortedStates(midiCtlFixtures()) {
		out = append(out, st.Mon.Lines)
	}
	out = append(out, midiMonLines{Empty: "no traffic"})
	return out
}

func midiStatStates() []midiPortStat {
	var out []midiPortStat
	for _, st := range sortedStates(midiCtlFixtures()) {
		for _, bl := range st.Ctls.Blocks {
			out = append(out, bl.Stat)
		}
	}
	return out
}

func ceTopbarStates() []ceTopbarSt { return sortedStates(ceTopbarFixtures()) }

func tkLiveStates() []liveTickSt {
	var out []liveTickSt
	for i, st := range sortedStates(liveFixtures()) {
		out = append(out, liveTickSt{Live: st, TC: fmt.Sprintf("0%d:23:45:12", i%10)})
	}
	return out
}

func tkLogsStates() []logsTickSt {
	var out []logsTickSt
	for _, st := range sortedStates(logsFixtures()) {
		out = append(out, logsTickSt{Lines: st.Lines})
	}
	out = append(out, logsTickSt{Lines: wireBenchTail()}) // the 400-line tail: the big one
	return out
}

func twFeedSurface() patchSurface[twFeedState] {
	return patchSurface[twFeedState]{
		name: "TwFeed", msgID: wireMsgTwFeed, states: twFeedStates(),
		seed: seedTwFeed, delta: deltaTwFeed, hash: hashTwFeed, patch: zigui.PatchTwitchFeed,
		oracle: twFeedHTML, full: wireTwFeed,
	}
}

func midiMonSurface() patchSurface[midiMonLines] {
	return patchSurface[midiMonLines]{
		name: "MidiMonLines", msgID: wireMsgMidiMonLines, states: midiMonStates(),
		seed: seedMidiMonLines, delta: deltaMidiMonLines, hash: hashMidiMonLines,
		patch: zigui.PatchMIDIMonRows, oracle: midiMonRowsHTML, full: wireMidiMonLines,
	}
}

func midiStatSurface() patchSurface[midiPortStat] {
	return patchSurface[midiPortStat]{
		name: "MidiPortStat", msgID: wireMsgMidiPortStat, states: midiStatStates(),
		seed: seedMidiPortStat, delta: deltaMidiPortStat, hash: hashMidiPortStat,
		patch: zigui.PatchMIDICtlStat, oracle: midiPortStatHTML, full: wireMidiPortStat,
	}
}

func ceTopbarSurface() patchSurface[ceTopbarSt] {
	return patchSurface[ceTopbarSt]{
		name: "CeTopbar", msgID: wireMsgCeTopbar, states: ceTopbarStates(),
		seed: seedCeTopbar, delta: deltaCeTopbar, hash: hashCeTopbar,
		patch: zigui.PatchCueEditTopbar, oracle: ceTopbarHTMLOf, full: wireCeTopbar,
	}
}

// fragSurface adapts a B3 scheduler surface (RZF1 reply) to the string-shaped gate: ids + HTML
// flattened the same way the stateless fuzz canary does it, so every assertion below applies.
func fragPatch(f func([]byte) ([]zigui.Frag, zigui.PatchStatus)) func([]byte) (string, zigui.PatchStatus) {
	return func(doc []byte) (string, zigui.PatchStatus) {
		frs, st := f(doc)
		if st != zigui.PatchOK {
			return "", st
		}
		return flatFrags(frs), zigui.PatchOK
	}
}

func flatFrags(frs []zigui.Frag) string {
	var b strings.Builder
	for _, f := range frs {
		b.WriteString(f.ID)
		b.WriteByte(0)
		b.WriteString(f.HTML)
		b.WriteByte(0)
	}
	return b.String()
}

func tkLiveSurface() patchSurface[liveTickSt] {
	return patchSurface[liveTickSt]{
		name: "TkLive", msgID: wireMsgTkLive, states: tkLiveStates(),
		seed: seedTkLive, delta: deltaTkLive, hash: hashTkLive,
		patch: fragPatch(zigui.PatchTickLive),
		oracle: func(v liveTickSt) string {
			frs, ok := zigui.TickLive(wireTkLive(v))
			if !ok {
				return "<declined>"
			}
			return flatFrags(frs)
		},
		full: wireTkLive,
	}
}

func tkLogsSurface() patchSurface[logsTickSt] {
	return patchSurface[logsTickSt]{
		name: "TkLogs", msgID: wireMsgTkLogs, states: tkLogsStates(),
		seed: seedTkLogs, delta: deltaTkLogs, hash: hashTkLogs,
		patch: fragPatch(zigui.PatchTickLogs),
		oracle: func(v logsTickSt) string {
			frs, ok := zigui.TickLogs(wireTkLogs(v))
			if !ok {
				return "<declined>"
			}
			return flatFrags(frs)
		},
		full: wireTkLogs,
	}
}

const patchTestLoc uint32 = 9 // a fixed i18n generation: the guard is exercised explicitly below

// runSequence drives one surface through n random fixture→fixture steps, asserting byte-equality
// with the stateless oracle at EVERY step. Returns (seeds, deltas, docBytes, fullBytes).
func runSequence[T any](t *testing.T, s patchSurface[T], rnd *rand.Rand, n int) (int, int, int, int) {
	t.Helper()
	h := zigui.RetainNew(s.msgID)
	if h == 0 {
		t.Fatalf("%s: slot table full", s.name)
	}
	defer zigui.RetainFree(h)

	var seeds, deltas, docB, fullB int
	cur := s.states[rnd.Intn(len(s.states))]
	doc := s.seed(cur, uint64(h), patchTestLoc)
	if len(doc) == 0 {
		t.Fatalf("%s: seed encode failed", s.name)
	}
	got, st := s.patch(doc)
	if st != zigui.PatchOK {
		t.Fatalf("%s: seed declined (%s)", s.name, st)
	}
	seeds, docB, fullB = 1, len(doc), len(s.full(cur))
	assertBytesEqual(t, s.name+" seed", s.oracle(cur), got)

	for i := 0; i < n; i++ {
		next := s.states[rnd.Intn(len(s.states))]
		d, changed := s.delta(next, cur, uint64(h), s.hash(cur), patchTestLoc)
		if !changed {
			// Identical state: nothing to merge. The lib still holds `cur`, so the invariant is
			// that the oracle of next == the oracle of cur (they are the same state).
			assertBytesEqual(t, fmt.Sprintf("%s step%d unchanged", s.name, i), s.oracle(cur), s.oracle(next))
			cur = next
			continue
		}
		if len(d) == 0 {
			t.Fatalf("%s step%d: delta encode failed", s.name, i)
		}
		got, st := s.patch(d)
		if st != zigui.PatchOK {
			t.Fatalf("%s step%d: delta declined (%s)", s.name, i, st)
		}
		deltas++
		docB += len(d)
		fullB += len(s.full(next))
		assertBytesEqual(t, fmt.Sprintf("%s step%d", s.name, i), s.oracle(next), got)
		cur = next
	}
	return seeds, deltas, docB, fullB
}

// TestZigPatchSequenceGoldens is gate 3 (and, by construction, gate 2: a seed or a delta is only
// ACCEPTED when Zig's own fingerprint of the merged state equals the one Go computed, so every
// step here is a hash-parity assertion as well as a render assertion).
func TestZigPatchSequenceGoldens(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	rnd := rand.New(rand.NewSource(0xD317A_5EED)) // fixed: a failing sequence must reproduce
	const steps = 60
	run := func(name string, f func(*rand.Rand) (int, int, int, int)) {
		t.Run(name, func(t *testing.T) {
			seeds, deltas, docB, fullB := f(rand.New(rand.NewSource(rnd.Int63())))
			t.Logf("%d seeds + %d deltas: %d B over the channel vs %d B of full documents (%.1f%%)",
				seeds, deltas, docB, fullB, 100*float64(docB)/float64(fullB))
		})
	}
	run("twfeed", func(r *rand.Rand) (int, int, int, int) { return runSequence(t, twFeedSurface(), r, steps) })
	run("midimon", func(r *rand.Rand) (int, int, int, int) { return runSequence(t, midiMonSurface(), r, steps) })
	run("midistat", func(r *rand.Rand) (int, int, int, int) { return runSequence(t, midiStatSurface(), r, steps) })
	run("cetopbar", func(r *rand.Rand) (int, int, int, int) { return runSequence(t, ceTopbarSurface(), r, steps) })
	run("tklive", func(r *rand.Rand) (int, int, int, int) { return runSequence(t, tkLiveSurface(), r, steps) })
	run("tklogs", func(r *rand.Rand) (int, int, int, int) { return runSequence(t, tkLogsSurface(), r, steps) })
}

// TestZigPatchDocKindsAreMutuallyUnparseable is gate 1. Absent means ZERO on RZW1 and KEEP on
// RZD1, so a document crossing into the other decoder would render silently wrong values - the
// magic check makes it impossible, and this proves it for a real delta and a real full document.
func TestZigPatchDocKindsAreMutuallyUnparseable(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	sts := twFeedStates()
	a, b := sts[0], sts[len(sts)-1]

	h := zigui.RetainNew(wireMsgTwFeed)
	if h == 0 {
		t.Fatal("slot table full")
	}
	defer zigui.RetainFree(h)
	if _, st := zigui.PatchTwitchFeed(seedTwFeed(a, uint64(h), patchTestLoc)); st != zigui.PatchOK {
		t.Fatalf("seed declined (%s)", st)
	}
	dd, changed := deltaTwFeed(b, a, uint64(h), hashTwFeed(a), patchTestLoc)
	if !changed || len(dd) == 0 {
		t.Fatal("no delta between the two fixtures - pick different ones")
	}
	if string(dd[:4]) != "RZD1" {
		t.Fatalf("delta magic = %q", dd[:4])
	}

	// A delta fed to the STATELESS export must decline (it must not merge, and it must not render
	// the zero value for every field the delta omitted).
	if got, ok := zigui.RenderTwitchFeedV2(dd); ok {
		t.Fatalf("stateless export accepted a delta document (%d bytes out)", len(got))
	}
	// A full RZW1 document fed to the PATCH export must decline before any state is touched.
	full := wireTwFeed(b)
	if string(full[:4]) != "RZW1" {
		t.Fatalf("full magic = %q", full[:4])
	}
	if got, st := zigui.PatchTwitchFeed(full); st != zigui.PatchMalformed {
		t.Fatalf("patch export status on a full document = %s (%d bytes out)", st, len(got))
	}
	// ...and the slot survived that: the delta still applies, and converges.
	got, st := zigui.PatchTwitchFeed(dd)
	if st != zigui.PatchOK {
		t.Fatalf("delta after the rejected full document declined (%s)", st)
	}
	assertBytesEqual(t, "converged", twFeedHTML(b), got)
}

// TestZigPatchGuardDeclines walks every guard: a wrong base hash, a moved locale generation, a
// fingerprint Go and Zig disagree on, a stale handle, and a delta onto an unseeded slot. Each must
// decline with PatchDesync (never PatchOK, never a crash) and leave the channel reseedable.
func TestZigPatchGuardDeclines(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	sts := twFeedStates()
	a, b := sts[0], sts[len(sts)-1]
	// The retained channel accounts its declines in PatchCounts, NOT in FallbackCounts: a decline
	// here is not a "the Zig renderer produced nothing" event, and mixing the two would make the
	// stateless gates' exact-delta assertions load-dependent. Both sides are asserted exactly.
	fbBefore := zigui.FallbackCounts()
	h := zigui.RetainNew(wireMsgTwFeed)
	if h == 0 {
		t.Fatal("slot table full")
	}
	defer zigui.RetainFree(h)
	reseed := func() {
		if _, st := zigui.PatchTwitchFeed(seedTwFeed(a, uint64(h), patchTestLoc)); st != zigui.PatchOK {
			t.Fatalf("reseed declined (%s)", st)
		}
	}

	// delta onto an UNSEEDED slot
	d, _ := deltaTwFeed(b, a, uint64(h), hashTwFeed(a), patchTestLoc)
	if _, st := zigui.PatchTwitchFeed(d); st != zigui.PatchDesync {
		t.Fatalf("delta onto an unseeded slot: %s", st)
	}

	reseed()
	bad, _ := deltaTwFeed(b, a, uint64(h), hashTwFeed(a)^0xFF, patchTestLoc)
	if _, st := zigui.PatchTwitchFeed(bad); st != zigui.PatchDesync {
		t.Fatalf("wrong base hash: %s", st)
	}

	reseed()
	other, _ := deltaTwFeed(b, a, uint64(h), hashTwFeed(a), patchTestLoc+1)
	if _, st := zigui.PatchTwitchFeed(other); st != zigui.PatchDesync {
		t.Fatalf("moved locale generation: %s", st)
	}

	reseed()
	// A fingerprint the two sides disagree on: corrupt new_hash in the header (offset 31).
	div := append([]byte(nil), d...)
	binary.LittleEndian.PutUint64(div[31:], binary.LittleEndian.Uint64(div[31:])^0x5A5A)
	if _, st := zigui.PatchTwitchFeed(div); st != zigui.PatchDesync {
		t.Fatalf("fingerprint divergence: %s", st)
	}

	reseed()
	stale, _ := deltaTwFeed(b, a, uint64(h)+1<<32, hashTwFeed(a), patchTestLoc) // another slot index
	if _, st := zigui.PatchTwitchFeed(stale); st != zigui.PatchDesync {
		t.Fatalf("foreign handle: %s", st)
	}

	// every decline left the slot reseedable, and a reseed converges
	got, st := zigui.PatchTwitchFeed(seedTwFeed(b, uint64(h), patchTestLoc))
	if st != zigui.PatchOK {
		t.Fatalf("final reseed declined (%s)", st)
	}
	assertBytesEqual(t, "converged after five declines", twFeedHTML(b), got)
	// Exact accounting on both ledgers: five patch-channel declines here must not have moved a
	// single stateless fallback counter for the same surface.
	assertNoNewFallbacksIn(t, fbBefore, "RenderTwitchFeed", "RenderTwitchFeedV2")
}

// TestZigPatchDesyncReseedsAndConverges is the review's named case: Zig drops the slot behind Go's
// back (a real RetainFree the channel did not perform), so the next delta MUST decline, the state
// machine MUST fall back to Unseeded, and the reseed MUST converge on the stateless render.
func TestZigPatchDesyncReseedsAndConverges(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	sts := twFeedStates()
	a, b := sts[0], sts[len(sts)-1]
	c := newPatchChanForTest()
	defer c.drop()

	got, ok := c.send(a)
	if !ok {
		t.Fatal("seed through the channel failed")
	}
	assertBytesEqual(t, "seed", twFeedHTML(a), got)
	if c.state() != psSeeded {
		t.Fatalf("state after seed = %d", c.state())
	}

	// The slot vanishes without the channel knowing (renderer restart, foreign free, gen bump).
	c.mu.Lock()
	h := c.h
	c.mu.Unlock()
	zigui.RetainFree(h)

	if _, ok := c.send(b); ok {
		t.Fatal("the channel accepted a delta into a slot that no longer exists")
	}
	if c.state() != psUnseeded {
		t.Fatalf("state after the decline = %d, want psUnseeded", c.state())
	}
	// The handle is stale AND the channel is unseeded: the next send must be a full-doc reseed,
	// and it must converge on exactly what the stateless path renders.
	got, ok = c.send(b)
	if !ok {
		t.Fatal("reseed after the desync failed")
	}
	assertBytesEqual(t, "reseeded", twFeedHTML(b), got)
	if c.state() != psSeeded {
		t.Fatalf("state after the reseed = %d", c.state())
	}
}

// newPatchChanForTest builds a bare twitch-feed channel (no UI needed - a patchChan owns nothing
// but its own slot, which is the point of handle-per-target).
func newPatchChanForTest() *patchChan[twFeedState, string] {
	return &patchChan[twFeedState, string]{
		name: "TestPatchTwitchFeed", msgID: wireMsgTwFeed,
		seed: seedTwFeed, delta: deltaTwFeed, hash: hashTwFeed, run: zigui.PatchTwitchFeed,
	}
}

// TestZigPatchUnchangedStateSkipsTheABI: a byte-identical state produces an EMPTY delta, which the
// channel answers from its own last output - no document, no cgo call, no re-render. This is the
// dominant outcome on a ~1 Hz surface and the reason the channel can pay off at all.
func TestZigPatchUnchangedStateSkipsTheABI(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	st := twFeedStates()[0]
	c := newPatchChanForTest()
	defer c.drop()
	first, ok := c.send(st)
	if !ok {
		t.Fatal("seed failed")
	}
	before := zigui.PatchCounts()["TestPatchTwitchFeed"]
	again, ok := c.send(st)
	if !ok {
		t.Fatal("unchanged send declined")
	}
	assertBytesEqual(t, "unchanged", first, again)
	after := zigui.PatchCounts()["TestPatchTwitchFeed"]
	if after.Deltas != before.Deltas || after.Seeds != before.Seeds || after.DeltaBytes != before.DeltaBytes ||
		after.SeedBytes != before.SeedBytes {
		t.Fatalf("an unchanged state still crossed the ABI: %+v → %+v", before, after)
	}
}

// TestZigPatchCapBreachIsItsOwnCounterWithHysteresis: a retained state past the per-slot cap gets
// PatchCapBreach (NOT a generic decline), the counter is separate, and three breaches on one
// surface make it sticky-stateless for the rest of the session.
func TestZigPatchCapBreachIsItsOwnCounterWithHysteresis(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	huge := oversizeLogsTick()
	c := &patchChan[logsTickSt, []zigui.Frag]{
		name: "TestPatchCapBreach", msgID: wireMsgTkLogs,
		seed: seedTkLogs, delta: deltaTkLogs, hash: hashTkLogs, run: zigui.PatchTickLogs,
	}
	defer c.drop()
	for i := 1; i <= patchCapBreachLimit; i++ {
		if _, ok := c.send(huge); ok {
			t.Fatalf("attempt %d: an over-cap state was accepted", i)
		}
		got := zigui.PatchCounts()["TestPatchCapBreach"]
		if got.CapBreach != uint64(i) {
			t.Fatalf("attempt %d: CapBreach = %d, want %d (counters: %+v)", i, got.CapBreach, i, got)
		}
		if got.Errors != 0 || got.Desync != 0 || got.Malformed != 0 {
			t.Fatalf("attempt %d: a cap breach leaked into another counter: %+v", i, got)
		}
	}
	if c.state() != psSticky {
		t.Fatalf("state after %d breaches = %d, want psSticky", patchCapBreachLimit, c.state())
	}
	if got := zigui.PatchCounts()["TestPatchCapBreach"]; got.Sticky != 1 {
		t.Fatalf("Sticky not recorded: %+v", got)
	}
	// Sticky means sticky: even a small state stays on the stateless path now.
	if _, ok := c.send(logsTickSt{Lines: logsLines{Wired: true, NoEntries: "none"}}); ok {
		t.Fatal("a sticky surface accepted a send")
	}
	if n := zigui.PatchCounts()["TestPatchCapBreach"].CapBreach; n != uint64(patchCapBreachLimit) {
		t.Fatalf("a sticky surface kept counting breaches: %d", n)
	}
}

// oversizeLogsTick builds a state whose LOGICAL size exceeds retain.max_slot_bytes (512 KiB).
func oversizeLogsTick() logsTickSt {
	msg := strings.Repeat("x", 512)
	es := make([]logsEntry, 0, 1400)
	for i := 0; i < 1400; i++ {
		es = append(es, logsEntry{Time: "09:15:01.250", Lvl: "INFO", Cls: "INFO", Src: "session",
			Msg: fmt.Sprintf("%d %s", i, msg), Fields: "map[bpm:128]"})
	}
	return logsTickSt{Lines: logsLines{Wired: true, NoEntries: "none", Entries: es}}
}

// TestZigPatchLocaleGenerationForcesReseed: bumping the Go-owned i18n generation must make the
// channel reseed rather than merge onto strings resolved under the old locale. (ii) carries NO
// catalog payload - only the generation - and this is the whole of its i18n contract.
func TestZigPatchLocaleGenerationForcesReseed(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	sts := twFeedStates()
	c := newPatchChanForTest()
	defer c.drop()
	if _, ok := c.send(sts[0]); !ok {
		t.Fatal("seed failed")
	}
	before := zigui.PatchCounts()["TestPatchTwitchFeed"]
	zigui.BumpLocaleGen()
	got, ok := c.send(sts[len(sts)-1])
	if !ok {
		t.Fatal("send after a locale switch declined")
	}
	assertBytesEqual(t, "reseeded under the new locale", twFeedHTML(sts[len(sts)-1]), got)
	after := zigui.PatchCounts()["TestPatchTwitchFeed"]
	if after.Seeds != before.Seeds+1 {
		t.Fatalf("a locale switch did not force a reseed: seeds %d → %d", before.Seeds, after.Seeds)
	}
	if after.Deltas != before.Deltas {
		t.Fatalf("a delta was sent across a locale switch: deltas %d → %d", before.Deltas, after.Deltas)
	}
}

// TestZigPatchDropReleasesSlots: the slot table is bounded, so the hard resync points MUST return
// slots. Claim the whole table through channels, drop them, claim it again.
func TestZigPatchDropReleasesSlots(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	st := twFeedStates()[0]
	live0, seeded0, bytes0 := zigui.RetainStats()
	var cs []*patchChan[twFeedState, string]
	for i := 0; i < 32; i++ {
		c := newPatchChanForTest()
		if _, ok := c.send(st); !ok {
			t.Fatalf("channel %d: seed failed (slot table exhausted?)", i)
		}
		cs = append(cs, c)
	}
	live, seeded, bytes := zigui.RetainStats()
	if live-live0 < 32 || seeded-seeded0 < 32 || bytes <= bytes0 {
		t.Fatalf("stats after 32 seeds: live %d→%d seeded %d→%d bytes %d→%d",
			live0, live, seeded0, seeded, bytes0, bytes)
	}
	for _, c := range cs {
		c.drop()
	}
	// Deltas, not absolutes: other suites in this package drive their own UIs (and therefore their
	// own slots) concurrently with this one - the FallbackCounts lesson, one layer down.
	if live, seeded, _ = zigui.RetainStats(); live != live0 || seeded != seeded0 {
		t.Fatalf("stats after dropping every channel: live %d (want %d) seeded %d (want %d)",
			live, live0, seeded, seeded0)
	}
	// and the table is usable again
	c := newPatchChanForTest()
	defer c.drop()
	if _, ok := c.send(st); !ok {
		t.Fatal("the table did not come back")
	}
}

// TestZigPatchClearedFieldsComeBackToZero pins the clear-tag semantics that make "absent = keep"
// safe: a string emptying, a bool clearing, a list emptying and an optional going null must all
// round-trip through a DELTA to exactly what the stateless renderer produces for that state.
func TestZigPatchClearedFieldsComeBackToZero(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	full := ceTopbarStates()
	var rich ceTopbarSt
	for _, st := range full {
		if st.Show && len(st.Drops) > 0 && st.TipS != nil {
			rich = st
			break
		}
	}
	if !rich.Show {
		t.Skip("no cue-edit topbar fixture with drops + a tooltip")
	}
	stripped := rich
	stripped.Drops = nil      // list → empty
	stripped.TipS = nil       // kOptPtr → null
	stripped.Dirty = false    // bool → false
	stripped.Census = ""      // string → ""
	stripped.Cursor = ""      //
	stripped.Verified = false //
	stripped.NoTag = false    //
	stripped.RceMeta = ""     //
	h := zigui.RetainNew(wireMsgCeTopbar)
	if h == 0 {
		t.Fatal("slot table full")
	}
	defer zigui.RetainFree(h)
	if _, st := zigui.PatchCueEditTopbar(seedCeTopbar(rich, uint64(h), patchTestLoc)); st != zigui.PatchOK {
		t.Fatalf("seed declined (%s)", st)
	}
	d, changed := deltaCeTopbar(stripped, rich, uint64(h), hashCeTopbar(rich), patchTestLoc)
	if !changed {
		t.Fatal("stripping every optional field produced no delta")
	}
	got, st := zigui.PatchCueEditTopbar(d)
	if st != zigui.PatchOK {
		t.Fatalf("clearing delta declined (%s)", st)
	}
	assertBytesEqual(t, "cleared", ceTopbarHTMLOf(stripped), got)
	// and the reverse direction (null → present, empty list → populated) works from there
	back, changed := deltaCeTopbar(rich, stripped, uint64(h), hashCeTopbar(stripped), patchTestLoc)
	if !changed {
		t.Fatal("restoring the fields produced no delta")
	}
	got, st = zigui.PatchCueEditTopbar(back)
	if st != zigui.PatchOK {
		t.Fatalf("restoring delta declined (%s)", st)
	}
	assertBytesEqual(t, "restored", ceTopbarHTMLOf(rich), got)
}

// ── mutation fuzz over (seed, delta*) SEQUENCES ──
//
// The stateless fuzz (zigui_wire_fuzz_test.go) mutates ONE document against a stateless export.
// This mode mutates deltas applied onto LIVE retained state, which is the only place a merge can
// corrupt anything. The canaries had to be adapted to a stateful channel:
//
//	poison-marker  unchanged: every buffer crosses inside a poison-filled allocation
//	determinism    re-SEEDS to the same base before each of the two attempts, so "same input →
//	               same output" is still a falsifiable claim on a stateful export
//	bounded output unchanged: output must stay proportional to the document
//	convergence    after every mutant (accepted or not) a clean reseed must render EXACTLY the
//	               stateless oracle - a corrupted merge cannot leave the channel poisoned
//
// Acceptance is meaningful here: the lib re-hashes what it merged and refuses unless the
// fingerprint equals the one in the document header, so an accepted mutant produced precisely the
// state Go described - which is why an accepted mutant's render is also checked against the oracle.

func fuzzPatchSurface[T any](t *testing.T, s patchSurface[T], rnd *rand.Rand, reps int) (int, int) {
	t.Helper()
	if len(s.states) < 2 {
		t.Fatalf("%s: need at least two states", s.name)
	}
	h := zigui.RetainNew(s.msgID)
	if h == 0 {
		t.Fatalf("%s: slot table full", s.name)
	}
	defer zigui.RetainFree(h)

	seedTo := func(v T) {
		t.Helper()
		if _, st := s.patch(poison(s.seed(v, uint64(h), patchTestLoc))); st != zigui.PatchOK {
			t.Fatalf("%s: reseed declined (%s)", s.name, st)
		}
	}

	var cases, accepted int
	for rep := 0; rep < reps; rep++ {
		base := s.states[rnd.Intn(len(s.states))]
		next := s.states[rnd.Intn(len(s.states))]
		d, changed := s.delta(next, base, uint64(h), s.hash(base), patchTestLoc)
		if !changed {
			continue
		}
		for kind := 0; kind < 10; kind++ {
			m := mutate(rnd, d, kind)
			cases++
			seedTo(base)
			h1, st1 := s.patch(poison(m))
			seedTo(base)
			h2, st2 := s.patch(poison(m))
			if st1 != st2 || h1 != h2 {
				t.Fatalf("%s/%s/kind%d: non-deterministic (%s/%s, %d/%d bytes)",
					s.name, "mut", kind, st1, st2, len(h1), len(h2))
			}
			if st1 != zigui.PatchOK {
				if h1 != "" {
					t.Fatalf("%s kind%d: declined (%s) but returned %d bytes", s.name, kind, st1, len(h1))
				}
			} else {
				accepted++
				if strings.Contains(h1, oobMark) {
					t.Fatalf("%s kind%d: output carries the OOB poison marker", s.name, kind)
				}
				if max := 4096 + 512*len(m); len(h1) > max {
					t.Fatalf("%s kind%d: %d B out of a %d B document (cap %d)", s.name, kind, len(h1), len(m), max)
				}
				// Accepted means the merged state's fingerprint matched the document's, so the
				// render must be the stateless render of THAT state - the delta's target.
				assertBytesEqual(t, fmt.Sprintf("%s kind%d accepted mutant", s.name, kind), s.oracle(next), h1)
			}
			// Convergence: whatever the mutant did, a clean reseed renders the oracle exactly.
			got, st := s.patch(poison(s.seed(next, uint64(h), patchTestLoc)))
			if st != zigui.PatchOK {
				t.Fatalf("%s kind%d: reseed after the mutant declined (%s)", s.name, kind, st)
			}
			assertBytesEqual(t, fmt.Sprintf("%s kind%d convergence", s.name, kind), s.oracle(next), got)
		}
	}
	return cases, accepted
}

func TestZigPatchDeltaSequenceFuzz(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	rnd := rand.New(rand.NewSource(0x5EED_D317A))
	const reps = 24
	var total, acc int
	add := func(name string, c, a int) {
		t.Logf("%s: %d mutated deltas, %d accepted (all equal to the stateless oracle)", name, c, a)
		total += c
		acc += a
	}
	c, a := fuzzPatchSurface(t, twFeedSurface(), rnd, reps)
	add("twfeed", c, a)
	c, a = fuzzPatchSurface(t, midiMonSurface(), rnd, reps)
	add("midimon", c, a)
	c, a = fuzzPatchSurface(t, midiStatSurface(), rnd, reps)
	add("midistat", c, a)
	c, a = fuzzPatchSurface(t, ceTopbarSurface(), rnd, reps)
	add("cetopbar", c, a)
	c, a = fuzzPatchSurface(t, tkLiveSurface(), rnd, reps)
	add("tklive", c, a)
	c, a = fuzzPatchSurface(t, tkLogsSurface(), rnd, reps)
	add("tklogs", c, a)
	if total < 400 {
		t.Fatalf("only %d mutated-delta cases - the gate requires >= 400", total)
	}
	live, seeded, _ := zigui.RetainStats()
	t.Logf("%d cases total, %d accepted; slots after the run: live %d seeded %d", total, acc, live, seeded)
}

// TestZigPatchCrossFedDocumentsDecline: every retained surface must refuse another surface's
// documents (the header's message id), in both kinds, and stay usable afterwards.
func TestZigPatchCrossFedDocumentsDecline(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	type probe struct {
		name  string
		msgID uint16
		patch func([]byte) (string, zigui.PatchStatus)
		docs  func(handle uint64) [][]byte // seed + delta for this surface
	}
	probes := []probe{
		{"TwFeed", wireMsgTwFeed, zigui.PatchTwitchFeed, func(h uint64) [][]byte {
			s := twFeedStates()
			d, _ := deltaTwFeed(s[len(s)-1], s[0], h, hashTwFeed(s[0]), patchTestLoc)
			return [][]byte{seedTwFeed(s[0], h, patchTestLoc), d}
		}},
		{"MidiMonLines", wireMsgMidiMonLines, zigui.PatchMIDIMonRows, func(h uint64) [][]byte {
			s := midiMonStates()
			d, _ := deltaMidiMonLines(s[len(s)-1], s[0], h, hashMidiMonLines(s[0]), patchTestLoc)
			return [][]byte{seedMidiMonLines(s[0], h, patchTestLoc), d}
		}},
		{"CeTopbar", wireMsgCeTopbar, zigui.PatchCueEditTopbar, func(h uint64) [][]byte {
			s := ceTopbarStates()
			d, _ := deltaCeTopbar(s[len(s)-1], s[0], h, hashCeTopbar(s[0]), patchTestLoc)
			return [][]byte{seedCeTopbar(s[0], h, patchTestLoc), d}
		}},
		{"TkLogs", wireMsgTkLogs, fragPatch(zigui.PatchTickLogs), func(h uint64) [][]byte {
			s := tkLogsStates()
			d, _ := deltaTkLogs(s[len(s)-1], s[0], h, hashTkLogs(s[0]), patchTestLoc)
			return [][]byte{seedTkLogs(s[0], h, patchTestLoc), d}
		}},
	}
	for _, dst := range probes {
		h := zigui.RetainNew(dst.msgID)
		if h == 0 {
			t.Fatalf("%s: slot table full", dst.name)
		}
		for _, src := range probes {
			if src.name == dst.name {
				continue
			}
			for i, doc := range src.docs(uint64(h)) {
				if len(doc) == 0 {
					continue
				}
				if got, st := dst.patch(poison(doc)); st == zigui.PatchOK {
					t.Errorf("%s accepted a %s document (kind %d, %d bytes out)", dst.name, src.name, i, len(got))
				}
			}
		}
		// still usable
		if _, st := dst.patch(poison(dst.docs(uint64(h))[0])); st != zigui.PatchOK {
			t.Errorf("%s: unusable after the cross-feed (%s)", dst.name, st)
		}
		zigui.RetainFree(h)
	}
}
