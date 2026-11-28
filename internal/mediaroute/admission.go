package mediaroute

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/gpubudget"
)

// Admission is one VRAM admission verdict as mediaroute consumes it (built by featurehost from the
// unified gpubudget decision). Attempt = admitted only as a verified attempt: the allocation runs
// (sender in this media child behind the interop probe, decoder in its own child), then headroom is
// re-read and a collapse backs it out (gpubudget.VerifyAttempt).
type Admission struct {
	Allow      bool
	Attempt    bool
	Reason     string
	Policy     string
	FreeMB     uint64
	NeedMB     uint64
	RequiredMB uint64
	Mode       string // receive decision: "hw" | "cpu" (light path: CPU decode into the destination) | ""
}

// Hold is the visible HELD state of one receive that cannot open (ctl routes / encoder-scan / Peers).
type Hold struct {
	Sender      string    `json:"sender"`
	Peer        string    `json:"peer,omitempty"`
	SourceID    string    `json:"sourceId,omitempty"`
	Since       time.Time `json:"since"`
	FreeMB      uint64    `json:"freeMB"`
	NeedMB      uint64    `json:"needMB"`
	RequiredMB  uint64    `json:"requiredMB"`
	Policy      string    `json:"policy"`
	Reason      string    `json:"reason"`
	Attempts    int       `json:"attempts,omitempty"`    // verified attempts that failed/backed out in this hold
	LastAttempt time.Time `json:"lastAttempt,omitempty"` // zero = none
	LastResult  string    `json:"lastResult,omitempty"`
	NextRetry   time.Time `json:"nextRetry"`
}

// Text renders one hold row.
func (h Hold) Text(now time.Time) string {
	s := fmt.Sprintf("HELD %q for %s: free %d MB, needs %d MB (required %d MB incl. margin), policy %s - %s",
		h.Sender, now.Sub(h.Since).Round(time.Second), h.FreeMB, h.NeedMB, h.RequiredMB, h.Policy, h.Reason)
	if !h.LastAttempt.IsZero() {
		s += fmt.Sprintf("; last attempt %s ago: %s (%d failed)", now.Sub(h.LastAttempt).Round(time.Second), h.LastResult, h.Attempts)
	}
	if d := h.NextRetry.Sub(now); d > 0 {
		s += fmt.Sprintf("; next check in %s", d.Round(time.Second))
	} else {
		s += "; re-checking every 2s and on headroom change"
	}
	return s
}

// FormatHolds renders holds for ctl (empty string = none).
func FormatHolds(hs []Hold, now time.Time) string {
	if len(hs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("held receives (VRAM - no offer sent, no CPU fallback):\n")
	for _, h := range hs {
		b.WriteString("  " + h.Text(now) + "\n")
	}
	return b.String()
}

// HoldReporter is implemented by receive controls that track VRAM holds (daemon-side wrapper).
type HoldReporter interface {
	Holds() []Hold
}

// holdTag separates the human message from the machine-readable hold in a held error: errors are the
// only thing that crosses the media-child RPC, so the hold rides inside the string.
const holdTag = " |hold="

// heldError builds the pre-offer hold error carrying h.
func heldError(desc string, h Hold) error {
	j, err := json.Marshal(h)
	if err != nil {
		j = []byte("{}")
	}
	return fmt.Errorf("%s %q: VRAM budget refuses the receive (%s, free %d MB, required %d MB, policy %s) - no offer sent%s%s",
		heldPrefix, desc, h.Reason, h.FreeMB, h.RequiredMB, h.Policy, holdTag, j)
}

// ParseHeld extracts the Hold from a held StartReceive error (ok=false: not a hold / no payload).
func ParseHeld(err error) (Hold, bool) {
	if !IsHeld(err) {
		return Hold{}, false
	}
	s := err.Error()
	i := strings.LastIndex(s, holdTag)
	if i < 0 {
		return Hold{}, false
	}
	var h Hold
	if json.Unmarshal([]byte(s[i+len(holdTag):]), &h) != nil {
		return Hold{}, false
	}
	return h, true
}

// attempt bounds: after a failed verified attempt, the next one waits attemptCooldown unless free VRAM
// rose by at least the need since (a real headroom change) - so a full card never churns a sender
// (each attempt that backs out is one Resolume follow), and freed VRAM is used at once.
const (
	attemptCooldown    = 60 * time.Second
	attemptMaxPerCycle = 3 // consecutive failures before only a headroom rise re-arms attempts
)

// attemptState is per-sender verified-attempt bookkeeping (Manager.mu).
type attemptState struct {
	last     time.Time
	failFree uint64 // free VRAM when the last attempt failed
	fails    int
	result   string
}

// gateAttempt applies the attempt bounds to an admission that asks for an attempt. Returns the
// (possibly converted-to-hold) admission. Caller holds m.mu.
func (m *Manager) gateAttempt(sender string, a Admission, now time.Time) Admission {
	if !a.Allow || !a.Attempt {
		return a
	}
	st := m.attempts[sender]
	if st == nil || st.fails == 0 {
		return a
	}
	rose := a.FreeMB >= st.failFree+a.NeedMB
	if rose {
		return a
	}
	if st.fails >= attemptMaxPerCycle || now.Sub(st.last) < attemptCooldown {
		a.Allow = false
		a.Reason = fmt.Sprintf("last verified attempt failed (%s); waiting for headroom to rise by %d MB or %s cooldown",
			st.result, a.NeedMB, attemptCooldown)
	}
	return a
}

// noteAttempt records a verified attempt's outcome. ok=true clears the record.
func (m *Manager) noteAttempt(sender string, ok bool, freeMB uint64, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		delete(m.attempts, sender)
		return
	}
	st := m.attempts[sender]
	if st == nil {
		if len(m.attempts) >= parkCap*2 { // bounded: one entry per distinct receive sender
			for k := range m.attempts {
				delete(m.attempts, k)
				break
			}
		}
		st = &attemptState{}
		m.attempts[sender] = st
	}
	st.last, st.failFree, st.result = m.now(), freeMB, result
	st.fails++
}

// hold release hysteresis: a held receive resumes only once free VRAM has stayed holdReleaseRiseMB above
// what the refusal required for holdReleaseDwell. Incident 2026-11-28: free hovered around the hard
// floor (admit at 338 MB, refuse at 217 MB) and two routes were offered + torn down every ~30 s - each
// cycle a sender capture/encoder start and a Resolume source blink.
const (
	holdReleaseRiseMB = 128
	holdReleaseDwell  = 15 * time.Second
)

// releaseState is per-sender hold-release bookkeeping (Manager.mu).
type releaseState struct {
	requiredMB uint64    // free VRAM the last refusal required (its free reading when unknown)
	okSince    time.Time // admission has passed with the rise since (zero = not yet)
}

// gateRelease applies the release hysteresis: records refusals, and converts an admission that
// follows a hold back into a hold until the rise has held for the dwell. Unknown headroom (FreeMB 0)
// passes: admission failed open. Caller holds m.mu.
func (m *Manager) gateRelease(sender string, a Admission, now time.Time) Admission {
	st := m.release[sender]
	if !a.Allow {
		if st == nil {
			if len(m.release) >= parkCap*2 { // bounded: one entry per held receive sender
				for k := range m.release {
					delete(m.release, k)
					break
				}
			}
			st = &releaseState{}
			m.release[sender] = st
		}
		st.okSince = time.Time{}
		if st.requiredMB = a.RequiredMB; st.requiredMB == 0 {
			st.requiredMB = a.FreeMB
		}
		return a
	}
	if st == nil {
		return a
	}
	if a.FreeMB == 0 {
		delete(m.release, sender)
		return a
	}
	want := st.requiredMB + holdReleaseRiseMB
	if a.FreeMB < want {
		st.okSince = time.Time{}
		a.Allow = false
		a.Reason = fmt.Sprintf("headroom recovering: free %d MB < %d MB (required %d + %d rise) - staying held", a.FreeMB, want, st.requiredMB, holdReleaseRiseMB)
		a.RequiredMB = want
		return a
	}
	if st.okSince.IsZero() {
		st.okSince = now
	}
	if d := now.Sub(st.okSince); d < holdReleaseDwell {
		a.Allow = false
		a.Reason = fmt.Sprintf("headroom recovering: free %d MB >= %d MB for %s of %s - staying held", a.FreeMB, want, d.Round(time.Second), holdReleaseDwell)
		a.RequiredMB = want
		return a
	}
	delete(m.release, sender)
	return a
}

// holdFor builds the Hold record for a refused admission (caller holds m.mu for attempt state).
func (m *Manager) holdFor(sender, peer, sourceID string, a Admission, now time.Time) Hold {
	h := Hold{Sender: sender, Peer: peer, SourceID: sourceID, Since: now, FreeMB: a.FreeMB, NeedMB: a.NeedMB,
		RequiredMB: a.RequiredMB, Policy: a.Policy, Reason: a.Reason, NextRetry: now.Add(2 * time.Second)}
	if prev, ok := m.holds[sender]; ok {
		h.Since = prev.Since
	}
	if st := m.attempts[sender]; st != nil {
		h.Attempts, h.LastAttempt, h.LastResult = st.fails, st.last, st.result
		if st.fails > 0 && st.fails < attemptMaxPerCycle {
			if nx := st.last.Add(attemptCooldown); nx.After(h.NextRetry) {
				h.NextRetry = nx
			}
		}
	}
	return h
}

// Holds lists the current VRAM holds of this manager, oldest first.
func (m *Manager) Holds() []Hold {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Hold, 0, len(m.holds))
	for _, h := range m.holds {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// verifyAttempt re-reads headroom after a tight-band allocation. ok=false => caller backs out.
func (m *Manager) verifyAttempt() (bool, uint64, string) {
	if m.headroom == nil {
		return true, 0, "no headroom source - kept"
	}
	h := m.headroom()
	ok, why := gpubudget.VerifyAttempt(h)
	return ok, h.FreeMB, why
}
