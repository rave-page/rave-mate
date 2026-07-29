package testcard

import (
	"image"
	"sync"
	"time"
)

// Verifier side: every pipeline stage with CPU pixels calls Observe on each frame. Non-card frames
// cost 6 samples (the finder check) and change nothing; card frames are decoded and accounted, and
// the per-stage tallies answer the questions no rate counter can: WHICH frames were skipped, how
// long each freeze ran, and whether latency is drifting.

// VerifyStats is one stage's tally since its first decoded card.
type VerifyStats struct {
	Stage   string    `json:"stage"`
	Session uint16    `json:"session"` // latest session seen
	FirstAt time.Time `json:"firstAt"`
	LastAt  time.Time `json:"lastAt"`

	Frames   uint64 `json:"frames"`  // frames offered to this stage while a card was active
	Decoded  uint64 `json:"decoded"` // clean CRC decodes
	CRCFail  uint64 `json:"crcFail"` // detected card, damaged payload (torn/blended frame)
	LowContr uint64 `json:"lowContrast"`

	LastSeq   uint32 `json:"lastSeq"`
	Unique    uint64 `json:"unique"`    // seq advanced: distinct pictures actually seen
	Dups      uint64 `json:"dups"`      // frame repeated the previous seq: upstream froze/repeated
	CurDupRun uint64 `json:"curDupRun"` // current freeze length in frames
	MaxDupRun uint64 `json:"maxDupRun"` // worst freeze seen, in frames
	Gaps      uint64 `json:"gaps"`      // total seqs skipped (sum of jump sizes - 1)
	MaxGap    uint32 `json:"maxGap"`
	Reorders  uint64 `json:"reorders"`  // seq went backwards without a session change
	GenBehind uint64 `json:"genBehind"` // frames flagged "generator overran" - gaps blamed upstream
	Restarts  uint64 `json:"restarts"`  // session id changed (generator restarted)

	// Latency: DeltaMs mixes both clocks (offset included), but DriftMs = last - min is offset-free
	// and is the number that turns "lagging more and more" into a measurement.
	LastDeltaMs int64 `json:"lastDeltaMs"`
	MinDeltaMs  int64 `json:"minDeltaMs"`
	MaxDeltaMs  int64 `json:"maxDeltaMs"`

	GenFPS uint8 `json:"genFPS"` // target rate the card itself declares
}

// DriftMs is how much older frames are NOW than the freshest one ever seen. Climbing = falling
// behind (queueing); stable = fixed latency.
func (v VerifyStats) DriftMs() int64 { return v.LastDeltaMs - v.MinDeltaMs }

// SeqRate is unique seqs per wall second - the pipeline's DELIVERED picture rate, which the wire
// fps cannot substitute for (60 wire fps of one repeated picture is a 0 here).
func (v VerifyStats) SeqRate() float64 {
	el := v.LastAt.Sub(v.FirstAt).Seconds()
	if el <= 0 {
		return 0
	}
	return float64(v.Unique) / el
}

const maxStages = 64 // registry bound: stage names come from code, not traffic, but cap anyway

var vreg = struct {
	sync.Mutex
	m map[string]*VerifyStats
}{m: make(map[string]*VerifyStats)}

// Observe accounts img at stage. Call it on EVERY frame: it self-detects the card.
func Observe(stage string, img *image.NRGBA) {
	ObserveAt(stage, img, time.Now())
}

// ObserveAt is Observe with an injectable clock (tests).
func ObserveAt(stage string, img *image.NRGBA, now time.Time) {
	p, derr := Decode(img)
	if derr == ErrNoCard {
		// Cheap path. A stage that WAS seeing a card and now sees none still counts the frame, so
		// "card disappeared under live traffic" is visible as Frames climbing with Decoded flat.
		vreg.Lock()
		if v, ok := vreg.m[stage]; ok {
			v.Frames++
			v.LastAt = now
		}
		vreg.Unlock()
		return
	}

	vreg.Lock()
	defer vreg.Unlock()
	v, ok := vreg.m[stage]
	if !ok {
		if len(vreg.m) >= maxStages {
			return
		}
		v = &VerifyStats{Stage: stage, FirstAt: now, MinDeltaMs: 1 << 62}
		vreg.m[stage] = v
	}
	v.Frames++
	v.LastAt = now
	switch derr {
	case ErrCRC, ErrVersion:
		v.CRCFail++
		return
	case ErrLowContrast:
		v.LowContr++
		return
	}

	if v.Decoded == 0 || p.Session != v.Session {
		if v.Decoded > 0 {
			v.Restarts++
		}
		// New run: baseline everything - deltas from another session's clock epoch are noise.
		v.Session, v.LastSeq, v.CurDupRun = p.Session, p.Seq, 0
		v.MinDeltaMs = 1 << 62
	} else {
		switch d := int64(p.Seq) - int64(v.LastSeq); {
		case d == 0:
			v.Dups++
			v.CurDupRun++
			v.MaxDupRun = max(v.MaxDupRun, v.CurDupRun)
		case d == 1:
			v.Unique++
			v.CurDupRun = 0
		case d > 1:
			v.Unique++
			v.CurDupRun = 0
			v.Gaps += uint64(d - 1)
			v.MaxGap = max(v.MaxGap, uint32(d-1))
		default:
			v.Reorders++
			v.CurDupRun = 0
		}
		v.LastSeq = p.Seq
	}
	v.Decoded++
	v.GenFPS = p.FPS
	if p.Flags&FlagBehind != 0 {
		v.GenBehind++
	}
	delta := p.DeltaMs(now)
	v.LastDeltaMs = delta
	v.MinDeltaMs = min(v.MinDeltaMs, delta)
	v.MaxDeltaMs = max(v.MaxDeltaMs, delta)
}

// VerifySnapshot returns a copy of every stage's tally.
func VerifySnapshot() map[string]VerifyStats {
	vreg.Lock()
	defer vreg.Unlock()
	out := make(map[string]VerifyStats, len(vreg.m))
	for k, v := range vreg.m {
		out[k] = *v
	}
	return out
}

// VerifyReset clears all tallies (a fresh experiment should not inherit the last one's freezes).
func VerifyReset() {
	vreg.Lock()
	defer vreg.Unlock()
	vreg.m = make(map[string]*VerifyStats)
}

// Report bundles the local generator's ground truth (nil when not running) with every verifier
// stage - the whole harness state in one wire-shaped value.
type Report struct {
	Gen    *GenStats              `json:"gen,omitempty"`
	Stages map[string]VerifyStats `json:"stages,omitempty"`
}
