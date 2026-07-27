//go:build windows && cgo

package mfenc

// Telemetry gates. The AMD box has no repo, no Go toolchain and no remote-exec: the ONLY diagnosis
// channel is the app's log stream + rendered stats. So the five facts a remote diagnosis needs must
// be emitted on the HEALTHY path, and that has to be asserted rather than assumed - a passing run
// whose telemetry is silent proves "it didn't crash this time", not "it works".

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureInfo installs a capturing Infof/Warnf for the duration of a test.
func captureInfo(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	prevI, prevW := Infof, Warnf
	sink := func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, sprintfLike(format, args...))
		mu.Unlock()
	}
	Infof, Warnf = sink, sink
	t.Cleanup(func() { Infof, Warnf = prevI, prevW })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// TestTelemetryNamesTheCodePathOnSuccess is the gate for the remote-diagnosis contract: after a
// SUCCESSFUL open, the log stream must name the drive mode AND the raw MF_TRANSFORM_ASYNC it came
// from, the tier, the device policy, and the resolved adapter. Previously the child's open trace
// only ever reached a log on a CRASH, so a passing run on a rig we cannot shell into was mute.
func TestTelemetryNamesTheCodePathOnSuccess(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	get := captureInfo(t)
	resetChildren(t)
	s, err := OpenProcSession(0, 640, 360, 640, 360, 30, 2000, 60)
	if err != nil {
		t.Fatalf("OpenProcSession: %v", err)
	}
	defer s.Close()
	all := strings.Join(get(), "\n")
	t.Logf("captured telemetry:\n%s", all)

	// 1: drive mode AND the raw attribute behind it.
	for _, want := range []string{"drive=", "async_attr="} {
		if !strings.Contains(all, want) {
			t.Errorf("open telemetry is missing %q - a remote run could not tell async from sync", want)
		}
	}
	// 2: which device policy / whether this session shared the child's device.
	if !strings.Contains(all, "device=") || !strings.Contains(all, "shared=") {
		t.Error("open telemetry does not name the device policy - a passing run cannot say WHICH path passed")
	}
	// 3: tier.
	if !strings.Contains(all, "tier=") {
		t.Error("open telemetry does not name the encode tier")
	}
	// 4: resolved adapter, so a stale configured LUID is visible.
	if !strings.Contains(all, "adapter=") || !strings.Contains(all, "requested=") {
		t.Error("open telemetry does not name resolved vs requested adapter")
	}
	// 5: the child's own open-stage trace, which carries `bound <MFT> drive=… aware=…`.
	if !strings.Contains(all, "open trace") || !strings.Contains(all, "mfenc stage:") {
		t.Error("the child's open-stage trace was not logged on the success path")
	}
	if !strings.Contains(all, "device-policy=") {
		t.Error("the child's hello did not report its active policies")
	}
}

// TestStatsSeparateSaturationFromFailure: "saturated but healthy" and "failing" must be readable
// apart in the rendered counters. Summing both into Dropped (as the first cut did) makes the two
// incidents indistinguishable, and they need different responses.
func TestStatsSeparateSaturationFromFailure(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	resetChildren(t)
	s, err := OpenProcSession(0, 640, 360, 640, 360, 30, 2000, 60)
	if err != nil {
		t.Fatalf("OpenProcSession: %v", err)
	}
	buf := make([]byte, 640*360*4)
	for i := 0; i < 20; i++ {
		for p := 0; p < len(buf); p += 4 {
			buf[p], buf[p+3] = byte(p/4+i*11), 255
		}
		if err := s.Encode(buf, int64(i)*33_333_333); err != nil {
			t.Fatalf("Encode %d: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	st := s.Stats()
	s.Close()
	t.Logf("stats: drive=%s asyncAttr=%v devShared=%v devPolicy=%s tier-sw=%v busyDrops=%d encFails=%d ledgerFails=%d poisoned=%v adapter=%#x",
		st.Drive, st.AsyncAttr, st.DevShared, st.DevPolicy, st.Software,
		st.BusyDrops, st.EncFails, st.LedgerFails, st.Poisoned, uint64(st.AdapterLUID))

	// The fields must EXIST and be independently readable; a healthy run has both at zero.
	if st.BusyDrops != 0 || st.EncFails != 0 {
		t.Errorf("healthy run reported saturation/failures: busyDrops=%d encFails=%d", st.BusyDrops, st.EncFails)
	}
	if st.Drive == "" {
		t.Error("Stats does not carry the resolved drive mode")
	}
	if st.DevPolicy == "" {
		t.Error("Stats does not carry the device policy - the rendered panel cannot name the code path")
	}
	if st.AdapterLUID == 0 {
		t.Log("adapter LUID 0 (default adapter) - acceptable, but note it cannot identify the GPU")
	}
	if st.Poisoned || st.LedgerFails != 0 {
		t.Errorf("clean run shows ledger state: fails=%d poisoned=%v", st.LedgerFails, st.Poisoned)
	}
}

// TestStatsSurfacePoisonState: when the ledger poisons a tuple, a LIVE session's stats must say so
// and why - otherwise the only place the degrade exists is a log line that has already scrolled.
func TestStatsSurfacePoisonState(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	resetChildren(t)
	s, err := OpenProcSession(0, 320, 240, 320, 240, 30, 500, 30)
	if err != nil {
		t.Fatalf("OpenProcSession: %v", err)
	}
	defer s.Close()
	// Poison exactly this session's ledger row, as a crash streak would.
	c := &fakeClock{t: time.Now()}
	prev := nowFn
	nowFn = c.now
	t.Cleanup(func() { nowFn = prev })
	for i := 0; i < failLimit; i++ {
		c.add(time.Second)
		NoteCrash(s.child.luid, s.ledgerKey(), "ProcessInput")
	}
	st := s.Stats()
	if !st.Poisoned || st.PoisonReason == "" {
		t.Fatalf("poisoned tuple not visible in Stats: poisoned=%v reason=%q fails=%d",
			st.Poisoned, st.PoisonReason, st.LedgerFails)
	}
	if st.LedgerFails < failLimit {
		t.Errorf("Stats ledgerFails=%d, want >=%d", st.LedgerFails, failLimit)
	}
	t.Logf("poison visible in Stats: fails=%d reason=%q", st.LedgerFails, st.PoisonReason)
}

// sprintfLike is fmt.Sprintf; named so the capture helper reads as a log sink.
func sprintfLike(format string, args ...any) string { return fmt.Sprintf(format, args...) }
