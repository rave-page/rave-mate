package webui

import (
	"strconv"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/midi"
)

// LED-feedback test: exercises the (otherwise never-driven) feedback path end to
// end - app IOCTL write → reserved-port Feedback tee → kernel drain → hardware
// device render pin. Burst = note-on velocity ramp over notes 36..51 (~50ms apart)
// then note-offs, hard-capped <2s; afterwards the reserved port's wire trace is
// diffed for new FeedbackOut entries (each = one framed message the kernel wrote
// to the device render pin) and the count vs messages sent is reported.

const (
	fbtNoteLo = 36 // pad/keybed range most controllers light
	fbtNoteHi = 51
	fbtOnGap  = 50 * time.Millisecond  // ramp pacing (visible sweep)
	fbtOffGap = 5 * time.Millisecond   // note-off flush pacing
	fbtBudget = 2 * time.Second        // hard cap on the whole burst
	fbtSettle = 300 * time.Millisecond // kernel feedback-drain grace before the trace diff
)

// fbtResult is one finished (or running) test outcome for the driver card.
type fbtResult struct{ variant, line string }

func init() {
	onPrefix("midi-fbtest:", func(u *UI, m actMsg) {
		u.midiFBTest(uint32(atoiSafe(m.arg("midi-fbtest:"))))
	})
}

// midiFBTest starts one bounded test toward reserved port portID (no-op while busy).
func (u *UI) midiFBTest(portID uint32) {
	if portID == 0 {
		return
	}
	u.fbtMu.Lock()
	if u.fbtBusy {
		u.fbtMu.Unlock()
		u.toast(i18n.T("midictl.drv.fbBusy"))
		return
	}
	u.fbtBusy = true
	u.fbtMu.Unlock()
	u.setFBTResult(portID, fbtResult{"warning", i18n.T("midictl.drv.fbRunning")})
	u.patchMain()
	go u.midiFBTestRun(portID) // bounded: ≤ fbtBudget+fbtSettle, then exits
}

// midiFBTestRun sends the burst, diffs the trace, reports (goroutine body).
func (u *UI) midiFBTestRun(portID uint32) {
	defer func() {
		u.fbtMu.Lock()
		u.fbtBusy = false
		u.fbtMu.Unlock()
	}()
	base := fbtBaseSeq(portID)
	sent := 0
	deadline := time.Now().Add(fbtBudget)
	var fail error
	for n := fbtNoteLo; n <= fbtNoteHi && time.Now().Before(deadline); n++ {
		vel := byte(32 + (n-fbtNoteLo)*6) // 32..122 brightness ramp on velocity-LED devices
		if fail = midi.WriteDriverPort(portID, []byte{0x90, byte(n), vel}); fail != nil {
			break
		}
		sent++
		time.Sleep(fbtOnGap)
	}
	for n := fbtNoteLo; n <= fbtNoteHi && fail == nil && time.Now().Before(deadline); n++ {
		if fail = midi.WriteDriverPort(portID, []byte{0x80, byte(n), 0}); fail != nil {
			break
		}
		sent++
		time.Sleep(fbtOffGap)
	}
	if fail != nil && sent == 0 {
		msg := i18n.T("midictl.drv.fbWriteErr", i18n.A{"err": fail.Error()})
		u.setFBTResult(portID, fbtResult{"error", msg})
		u.toast(msg)
		u.patchMain()
		return
	}
	time.Sleep(fbtSettle)
	out := fbtCount(fbtTrace(portID), base)
	res := i18n.T("midictl.drv.fbResult",
		i18n.A{"out": strconv.Itoa(out), "sent": strconv.Itoa(sent)})
	variant := "success"
	if out < sent {
		variant = "warning"
	}
	u.setFBTResult(portID, fbtResult{variant, res})
	u.toast(res)
	u.patchMain()
}

func (u *UI) setFBTResult(portID uint32, r fbtResult) {
	u.fbtMu.Lock()
	if u.fbtRes == nil {
		u.fbtRes = map[uint32]fbtResult{}
	}
	u.fbtRes[portID] = r
	u.fbtMu.Unlock()
}

// fbtResultFor returns the port's last outcome ("" line = none yet).
func (u *UI) fbtResultFor(portID uint32) fbtResult {
	u.fbtMu.Lock()
	defer u.fbtMu.Unlock()
	return u.fbtRes[portID]
}

// fbtTrace snapshots the port trace (nil on error - counts as zero entries).
func fbtTrace(portID uint32) []midi.TraceEntry {
	es, err := midi.QueryDriverTrace(portID)
	if err != nil {
		return nil
	}
	return es
}

// fbtBaseSeq returns the newest trace Seq before the burst (0 = empty ring).
func fbtBaseSeq(portID uint32) uint64 {
	es := fbtTrace(portID)
	if len(es) == 0 {
		return 0
	}
	return es[len(es)-1].Seq // ring is oldest-first
}

// fbtCount counts FeedbackOut entries newer than base.
func fbtCount(es []midi.TraceEntry, base uint64) int {
	n := 0
	for _, e := range es {
		if e.Dir == midi.TraceDirFeedbackOut && e.Seq > base {
			n++
		}
	}
	return n
}
