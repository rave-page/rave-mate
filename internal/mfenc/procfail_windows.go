//go:build windows && cgo

package mfenc

// procfail: the encoder child's crash-loop safety net, and the crash-attribution decoder.
//
// WHY THIS EXISTS AS ITS OWN LEDGER. The original counter lived on procChild as
// `consecFail`, incremented only when the child died within crashWindow of its LAST SPAWN.
// The child respawns IMMEDIATELY after every crash and then idles until the next route, so a
// human-paced retry (always > 30 s later) reset the streak to 1 - the field log said
// "consecutive fails 1" on every single route and "poison after 3 fast-fails" could never fire.
// A driver AV loop therefore degraded to NOTHING instead of degrading to a working encoder.
// Two further holes: the poison entries were written only for sessions still registered at crash
// time (a crash during teardown poisoned nothing even at the limit), and the whole thing was
// keyed on geometry, so the same broken MFT was re-tried for every new resolution.
//
// The ledger fixes all three: the streak is measured CRASH TO CRASH, it is keyed on
// (adapter, encoder) - the thing that is actually broken - and it is updated from the child's
// exit path regardless of how many sessions happened to be live.
//
// RESET POLICY (explicit, because a silent decay re-arms a crash loop on a timer):
//   - a crash extends the streak when it lands within failWindow of the previous crash,
//     otherwise it STARTS a new streak at 1;
//   - failLimit crashes in a streak poisons (adapter, encoder);
//   - a poisoned tuple clears ONLY on proof of health plus quiet time: some session on it
//     delivered at least one AU (NoteHealthy) AND no crash for forgetAfter. Time alone never
//     clears it;
//   - ResetPoison() clears everything (a user action - new driver, new hardware).
//
// A poisoned HARDWARE tuple is not the end of the native engine: the next open on that adapter
// asks the child for the SOFTWARE tier (openCmd.SW), so the route still carries real pixels.
// Only a poisoned software tier means "no native video here", and then mediapipe substitutes an
// ffmpeg encoder.

import (
	"fmt"
	"sync"
	"time"
)

const (
	failLimit   = maxConsecFails   // crashes in one streak before the tuple is poisoned
	failWindow  = 10 * time.Minute // crash-to-crash gap that still counts as the same streak
	forgetAfter = 30 * time.Minute // quiet time before a HEALTHY tuple may be un-poisoned
)

// nowFn is the ledger's clock seam (tests drive streaks without sleeping for real minutes).
var nowFn = time.Now

type failKey struct {
	luid    int64
	encoder string // MFT friendly name; "" = nothing bound on this adapter yet
}

type failEntry struct {
	fails    int
	firstAt  time.Time
	lastAt   time.Time
	healthy  bool      // some session on this tuple produced an AU since the last crash
	healthAt time.Time // when that happened
	poisoned bool
	reason   string
	stage    string // the child's last latched stage at the crash that poisoned it
}

var (
	ledgerMu sync.Mutex
	ledger   = map[failKey]*failEntry{}
	// lastEncoder remembers which MFT the child bound on an adapter, so an open can predict
	// which ledger entry applies BEFORE the child has told us the name (chicken-and-egg: the
	// name only arrives in the opened event). The child's pick is deterministic per adapter.
	lastEncoder = map[int64]string{}
)

// NoteEncoder records the MFT bound on an adapter (called from a successful open).
func NoteEncoder(luid int64, encoder string) {
	if encoder == "" {
		return
	}
	ledgerMu.Lock()
	lastEncoder[luid] = encoder
	ledgerMu.Unlock()
}

// NoteHealthy marks (adapter, encoder) as having really produced output. Proof of health is a
// precondition for ever un-poisoning it - "it has been quiet for a while" is not.
func NoteHealthy(luid int64, encoder string) {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	e := ledger[failKey{luid, encoder}]
	if e == nil {
		return
	}
	e.healthy = true
	e.healthAt = nowFn()
}

// NoteCrash records one encoder-child death against (adapter, encoder) and reports the streak
// length plus whether the tuple is now poisoned. Called on EVERY child exit that looks like a
// fault, including one with no live sessions - a crash during teardown is still a crash.
func NoteCrash(luid int64, encoder, stage string) (fails int, poisoned bool) {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	now := nowFn()
	k := failKey{luid, encoder}
	e := ledger[k]
	if e == nil {
		e = &failEntry{}
		ledger[k] = e
	}
	// Streak is crash-to-crash. The old code measured "since the last SPAWN", which the
	// supervisor's own immediate respawn reset on every iteration.
	if e.fails == 0 || now.Sub(e.lastAt) > failWindow {
		e.fails = 1
		e.firstAt = now
	} else {
		e.fails++
	}
	e.lastAt = now
	e.healthy = false // a crash invalidates any earlier proof of health
	if e.fails >= failLimit && !e.poisoned {
		e.poisoned = true
		e.stage = stage
		e.reason = fmt.Sprintf("encoder child crashed %d times in %s on adapter %#x (%s)",
			e.fails, now.Sub(e.firstAt).Round(time.Second), uint64(luid), encoderLabel(encoder))
		if stage != "" {
			e.reason += " - last stage: " + stage
		}
	}
	return e.fails, e.poisoned
}

func encoderLabel(encoder string) string {
	if encoder == "" {
		return "encoder unknown"
	}
	return encoder
}

// orUnknown keeps a log line unambiguous when an older child sent no value.
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// tierName labels the encode tier for a log line.
func tierName(software bool) string {
	if software {
		return "software-mf"
	}
	return "hardware"
}

// poisonSuffix appends the ledger's poison reason when one exists.
func poisonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " POISONED: " + reason
}

// stageSuffix renders the latched crash stage for a log line ("" when unknown).
func stageSuffix(stage string) string {
	if stage == "" {
		return ""
	}
	return "; last stage: " + stage
}

// PoisonedOn reports whether the encoder the child would bind on this adapter is poisoned, and
// why. The caller's answer to a poisoned HARDWARE tuple is the software tier, not no video.
func PoisonedOn(luid int64) (string, bool) {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	enc, ok := lastEncoder[luid]
	if !ok {
		return "", false
	}
	return poisonedLocked(failKey{luid, enc})
}

// PoisonedTuple reports the state of one explicit (adapter, encoder) pair.
func PoisonedTuple(luid int64, encoder string) (string, bool) {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	return poisonedLocked(failKey{luid, encoder})
}

// poisonedLocked applies the reset policy on read: a poisoned tuple that has PROVEN healthy and
// then stayed quiet for forgetAfter is released. Doing it on read keeps the policy in one place
// and needs no timer goroutine.
func poisonedLocked(k failKey) (string, bool) {
	e := ledger[k]
	if e == nil || !e.poisoned {
		return "", false
	}
	if e.healthy && nowFn().Sub(e.lastAt) > forgetAfter {
		delete(ledger, k)
		return "", false
	}
	return e.reason, true
}

// ResetPoison clears the whole ledger (user action: new driver, new GPU, "try again").
func ResetPoison() {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	ledger = map[failKey]*failEntry{}
}

// FailReport is one ledger row for diagnostics/UI.
type FailReport struct {
	LUID     int64
	Encoder  string
	Fails    int
	Poisoned bool
	Reason   string
	Stage    string
	LastAt   time.Time
}

// FailLedger snapshots the ledger (diagnostics; encoder-scan surface).
func FailLedger() []FailReport {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	out := make([]FailReport, 0, len(ledger))
	for k, e := range ledger {
		out = append(out, FailReport{LUID: k.luid, Encoder: k.encoder, Fails: e.fails,
			Poisoned: e.poisoned, Reason: e.reason, Stage: e.stage, LastAt: e.lastAt})
	}
	return out
}

// ── crash attribution ──

// stageNames decodes the child's latched stage word (native/zigenc mf.zig Stage - the numbers
// are a pinned cross-process contract, asserted on both sides). A vendor AV leaves no stack and
// no reliable stderr tail, so this is what names the faulting call.
// 0 (Stage.idle) is deliberately absent: "nothing latched" must render EMPTY so a crash report
// says nothing rather than inventing a stage.
var stageNames = map[uint32]string{
	10: "MFStartup",
	11: "pick adapter",
	12: "D3D11CreateDevice",
	13: "MFCreateDXGIDeviceManager/ResetDevice",
	14: "MFTEnumEx",
	15: "IMFActivate::ActivateObject",
	16: "ProcessMessage(SET_D3D_MANAGER)",
	17: "SetOutputType",
	18: "SetInputType",
	19: "ICodecAPI::SetValue",
	20: "GetOutputStreamInfo",
	21: "QI(IMFMediaEventGenerator)",
	22: "CreateVideoProcessor",
	23: "CreateTexture2D/views/NV12 pool",
	24: "ProcessMessage(BEGIN_STREAMING)",
	25: "open complete (idle in the feed loop)",
	40: "gateInput (waiting for a free NV12 slot)",
	41: "RGBA→BGRA swizzle",
	42: "UpdateSubresource",
	43: "VideoProcessorBlt",
	44: "IMFSample::SetSampleTime",
	45: "ICodecAPI force-IDR",
	46: "waiting for METransformNeedInput",
	47: "ProcessInput",
	48: "IMFMediaEventGenerator::GetEvent",
	49: "ProcessOutput",
	50: "IMFMediaBuffer::Lock",
	51: "AU ring append",
	52: "software readback CopyResource",
	53: "software readback Map",
	54: "ICodecAPI set bitrate",
	60: "ProcessMessage(DRAIN)",
	61: "drain pump",
	70: "ProcessMessage(FLUSH)",
	71: "ProcessMessage(END_STREAMING)",
	72: "ProcessMessage(SET_D3D_MANAGER, 0)",
	73: "Release(IMFTransform)",
	74: "IMFActivate::ShutdownObject",
	75: "release NV12 pool",
	76: "release video processor",
	77: "release D3D11 device",
	78: "close complete",
}

// stageName renders a latched stage code for a crash report.
func stageName(code uint32) string {
	if n, ok := stageNames[code]; ok {
		return n
	}
	if code == 0 {
		return ""
	}
	return fmt.Sprintf("stage %d", code)
}
