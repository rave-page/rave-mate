package gridfix

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

// stubAnalyzer serves scripted detections/errors; optional per-call hook + delay.
type stubAnalyzer struct {
	mu     sync.Mutex
	calls  int
	det    map[string]*Detection
	errs   map[string]error
	delay  time.Duration
	onCall func(n int)
}

func (s *stubAnalyzer) Analyze(ctx context.Context, path string) (*Detection, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if s.onCall != nil {
		s.onCall(n)
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := s.errs[path]; err != nil {
		return nil, err
	}
	d := s.det[path]
	if d == nil {
		return nil, fmt.Errorf("no scripted detection for %s", path)
	}
	return d, nil
}

func (s *stubAnalyzer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// synthDet builds a perfect constant grid (orchestration tests only - the
// fit/plan math has its own golden tests).
func synthDet(n int, anchor, period float64) *Detection {
	beats := make([]float64, n)
	for i := range beats {
		beats[i] = anchor + period*float64(i)
	}
	var dbs []float64
	for i := 0; i < n; i += 4 {
		dbs = append(dbs, beats[i])
	}
	return &Detection{Beats: beats, Downbeats: dbs}
}

func ptr(v float64) *float64 { return &v }

func TestBatchRunCounts(t *testing.T) {
	dir := t.TempDir()
	fix := writeAudioStub(t, dir, "fix.mp3", "f")
	ok := writeAudioStub(t, dir, "ok.mp3", "o")
	unstable := writeAudioStub(t, dir, "unstable.mp3", "u")
	bad := writeAudioStub(t, dir, "bad.mp3", "b")
	det := synthDet(128, 0.25, 0.5) // 120 BPM, anchor 0.25s
	stub := &stubAnalyzer{
		det: map[string]*Detection{
			fix:      det,
			ok:       det,
			unstable: synthDet(10, 0.25, 0.5), // <16 beats → nil fit
		},
		errs: map[string]error{bad: errors.New("decode boom")},
	}
	cache, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := NewBatch(stub, cache, BatchOptions{MinQuality: 0.85, ThresholdMS: 12})
	tracks := []BatchTrack{
		{Path: fix, Title: "Fix Me", OldBPM: 120, OldStartMs: ptr(290)},     // 40ms off → FIX
		{Path: ok, Title: "Aligned", OldBPM: 120, OldStartMs: ptr(250)},     // on grid → OK
		{Path: "locked.mp3", Title: "Locked", OldBPM: 140, Locked: true},    // no analysis
		{Path: "multi.mp3", Title: "Multi", OldBPM: 100, MultiMarker: true}, // no analysis
		{Path: bad, Title: "Broken", OldBPM: 128},                           // analyze error
		{Path: unstable, Title: "Unstable", OldBPM: 120},                    // nil fit → SKIP
	}
	var progresses []BatchProgress
	results := b.Run(context.Background(), tracks, func(p BatchProgress) { progresses = append(progresses, p) })

	if len(results) != 6 {
		t.Fatalf("results=%d want 6", len(results))
	}
	final := progresses[len(progresses)-1]
	if final.Phase != PhaseDone || final.Done != 6 || final.Total != 6 {
		t.Fatalf("final progress %+v", final)
	}
	if final.Fixed != 1 || final.OK != 1 || final.Skipped != 3 || final.Failed != 1 || final.Cached != 0 {
		t.Fatalf("counts fixed=%d ok=%d skip=%d fail=%d cached=%d", final.Fixed, final.OK, final.Skipped, final.Failed, final.Cached)
	}
	if progresses[0].Phase != PhaseScanning {
		t.Fatalf("first progress phase %s want scanning", progresses[0].Phase)
	}
	if progresses[1].Phase != PhaseAnalyzing || progresses[1].Current != "Fix Me" {
		t.Fatalf("second progress %+v want analyzing/Fix Me", progresses[1])
	}
	if results[0].Plan.Status != StatusFix || results[0].FromCache {
		t.Fatalf("fix track: %+v", results[0])
	}
	if results[1].Plan.Status != StatusOK {
		t.Fatalf("aligned track: %+v", results[1])
	}
	if results[2].Plan.Status != StatusSkip || results[2].Plan.Detail != "grid locked - not touching" {
		t.Fatalf("locked track: %+v", results[2])
	}
	if results[3].Plan.Status != StatusSkip ||
		results[3].Plan.Detail != "multiple grid markers (manually gridded?) - not touching" {
		t.Fatalf("multimarker track: %+v", results[3])
	}
	if results[4].Err == "" || results[4].Plan.Status != "" {
		t.Fatalf("error track: %+v", results[4])
	}
	if results[5].Plan.Status != StatusSkip || results[5].Plan.Detail != "no stable constant grid found - fix manually" {
		t.Fatalf("unstable track: %+v", results[5])
	}
	if got := stub.callCount(); got != 4 { // locked + multimarker never analyzed
		t.Fatalf("analyze calls=%d want 4", got)
	}
	if results[0].Beats != 128 {
		t.Fatalf("beats diag=%d want 128", results[0].Beats)
	}
}

func TestBatchSecondRunServesCache(t *testing.T) {
	dir := t.TempDir()
	fix := writeAudioStub(t, dir, "fix.mp3", "f")
	ok := writeAudioStub(t, dir, "ok.mp3", "o")
	det := synthDet(128, 0.25, 0.5)
	stub := &stubAnalyzer{det: map[string]*Detection{fix: det, ok: det}}
	cache, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := NewBatch(stub, cache, BatchOptions{Checkpoint: "ckpt-x"})
	tracks := []BatchTrack{
		{Path: fix, Title: "A", OldBPM: 120, OldStartMs: ptr(290)},
		{Path: ok, Title: "B", OldBPM: 120, OldStartMs: ptr(250)},
	}
	r1 := b.Run(context.Background(), tracks, nil)
	if stub.callCount() != 2 || r1[0].FromCache || r1[1].FromCache {
		t.Fatalf("first run: calls=%d results=%+v", stub.callCount(), r1)
	}
	var final BatchProgress
	r2 := b.Run(context.Background(), tracks, func(p BatchProgress) { final = p })
	if stub.callCount() != 2 {
		t.Fatalf("second run re-analyzed: calls=%d", stub.callCount())
	}
	if !r2[0].FromCache || !r2[1].FromCache {
		t.Fatalf("second run not from cache: %+v", r2)
	}
	if final.Cached != 2 || final.Fixed != 1 || final.OK != 1 {
		t.Fatalf("second run progress: %+v", final)
	}
	if r2[0].Plan.Status != r1[0].Plan.Status || r2[1].Plan.Status != r1[1].Plan.Status {
		t.Fatal("cached run changed plan outcomes")
	}
	if cache.Len() != 2 {
		t.Fatalf("cache Len=%d want 2", cache.Len())
	}
}

func TestBatchCancelReturnsPartial(t *testing.T) {
	dir := t.TempDir()
	det := synthDet(128, 0.25, 0.5)
	var paths []string
	dets := map[string]*Detection{}
	for i := 0; i < 4; i++ {
		p := writeAudioStub(t, dir, fmt.Sprintf("t%d.mp3", i), "x")
		paths = append(paths, p)
		dets[p] = det
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := &stubAnalyzer{det: dets, onCall: func(n int) {
		if n == 3 {
			cancel() // mid-run: 3rd analyze aborts
		}
	}}
	b := NewBatch(stub, nil, BatchOptions{})
	var tracks []BatchTrack
	for i, p := range paths {
		tracks = append(tracks, BatchTrack{Path: p, Title: fmt.Sprintf("T%d", i), OldBPM: 120, OldStartMs: ptr(250)})
	}
	var final BatchProgress
	results := b.Run(ctx, tracks, func(p BatchProgress) { final = p })
	if len(results) != 2 {
		t.Fatalf("partial results=%d want 2", len(results))
	}
	if final.Phase != PhaseCancelled || final.Done != 2 || final.Failed != 0 {
		t.Fatalf("final progress %+v want cancelled/2 done/0 failed", final)
	}
}

// TestBatchCheckpointChangeReanalyzes guards the trained-model fix: a cache entry from one model
// must NOT be replayed for a different model - the new model has to actually re-analyze.
func TestBatchCheckpointChangeReanalyzes(t *testing.T) {
	dir := t.TempDir()
	fix := writeAudioStub(t, dir, "fix.mp3", "f")
	det := synthDet(128, 0.25, 0.5)
	stub := &stubAnalyzer{det: map[string]*Detection{fix: det}}
	cache, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	tracks := []BatchTrack{{Path: fix, Title: "A", OldBPM: 120, OldStartMs: ptr(290)}}
	// builtin model → analyze + cache under checkpoint ""
	NewBatch(stub, cache, BatchOptions{}).Run(context.Background(), tracks, nil)
	if stub.callCount() != 1 {
		t.Fatalf("first run calls=%d want 1", stub.callCount())
	}
	// same model → cache hit, no re-analyze
	r := NewBatch(stub, cache, BatchOptions{}).Run(context.Background(), tracks, nil)
	if stub.callCount() != 1 || !r[0].FromCache {
		t.Fatalf("same-model rerun: calls=%d fromCache=%v", stub.callCount(), r[0].FromCache)
	}
	// switch to a fine-tuned model → cache MISS → re-analyze
	r = NewBatch(stub, cache, BatchOptions{Checkpoint: "trained-v1"}).Run(context.Background(), tracks, nil)
	if stub.callCount() != 2 || r[0].FromCache {
		t.Fatalf("model-switch rerun: calls=%d fromCache=%v (must re-analyze with the new model)", stub.callCount(), r[0].FromCache)
	}
}

// TestBatchForceOverridesSkipsAndCache: force re-analyzes past Locked/MultiMarker AND the cache,
// but verified grids stay protected in both modes.
func TestBatchForceOverridesSkipsAndCache(t *testing.T) {
	dir := t.TempDir()
	fix := writeAudioStub(t, dir, "fix.mp3", "f")
	locked := writeAudioStub(t, dir, "locked.mp3", "l")
	multi := writeAudioStub(t, dir, "multi.mp3", "m")
	verified := writeAudioStub(t, dir, "verified.mp3", "v")
	det := synthDet(128, 0.25, 0.5)
	newStub := func() *stubAnalyzer {
		return &stubAnalyzer{det: map[string]*Detection{fix: det, locked: det, multi: det, verified: det}}
	}
	cache, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	tracks := []BatchTrack{
		{Path: fix, Title: "Fix", OldBPM: 120, OldStartMs: ptr(290)},
		{Path: locked, Title: "Locked", OldBPM: 120, OldStartMs: ptr(290), Locked: true},
		{Path: multi, Title: "Multi", OldBPM: 120, MultiMarker: true},
		{Path: verified, Title: "Verified", OldBPM: 120, OldStartMs: ptr(290), Verified: true},
	}
	// normal run: only fix analyzed; locked/multi/verified skipped
	s1 := newStub()
	r := NewBatch(s1, cache, BatchOptions{}).Run(context.Background(), tracks, nil)
	if s1.callCount() != 1 {
		t.Fatalf("normal run analyze calls=%d want 1", s1.callCount())
	}
	if r[3].Plan.Status != StatusSkip || r[3].Plan.Detail != "verified grid - protected" {
		t.Fatalf("verified not protected: %+v", r[3])
	}
	// force run: fix (cache-bypassed) + locked + multi re-analyzed; verified STILL skipped
	s2 := newStub()
	r = NewBatch(s2, cache, BatchOptions{Force: true}).Run(context.Background(), tracks, nil)
	if s2.callCount() != 3 {
		t.Fatalf("force run analyze calls=%d want 3 (verified excluded)", s2.callCount())
	}
	if r[3].Plan.Status != StatusSkip || r[3].Plan.Detail != "verified grid - protected" {
		t.Fatalf("verified not protected under force: %+v", r[3])
	}
	for i := 0; i < 3; i++ {
		if r[i].FromCache {
			t.Fatalf("force run served track %d from cache", i)
		}
	}
}

func TestBatchETAPositiveWhileAnalyzing(t *testing.T) {
	dir := t.TempDir()
	det := synthDet(128, 0.25, 0.5)
	dets := map[string]*Detection{}
	var tracks []BatchTrack
	for i := 0; i < 4; i++ {
		p := writeAudioStub(t, dir, fmt.Sprintf("e%d.mp3", i), "x")
		dets[p] = det
		tracks = append(tracks, BatchTrack{Path: p, Title: fmt.Sprintf("E%d", i), OldBPM: 120, OldStartMs: ptr(250)})
	}
	stub := &stubAnalyzer{det: dets, delay: 5 * time.Millisecond}
	b := NewBatch(stub, nil, BatchOptions{})
	sawETA := false
	results := b.Run(context.Background(), tracks, func(p BatchProgress) {
		if p.Phase == PhaseAnalyzing && p.Done > 0 && p.Done < p.Total && p.ETA > 0 {
			sawETA = true
		}
	})
	if len(results) != 4 {
		t.Fatalf("results=%d want 4", len(results))
	}
	if !sawETA {
		t.Fatal("no mid-run progress had ETA > 0")
	}
}

// TestBatchRunAutoBias: a run whose offsets are one systematic bias (all +14ms)
// re-plans itself with the bias subtracted - markers stay put (FIX→OK) and the
// second pass never re-analyzes.
func TestBatchRunAutoBias(t *testing.T) {
	dir := t.TempDir()
	det := synthDet(400, 0.25, 0.5)
	dets := map[string]*Detection{}
	var tracks []BatchTrack
	for i := 0; i < 12; i++ {
		p := writeAudioStub(t, dir, fmt.Sprintf("b%d.mp3", i), "x")
		dets[p] = det
		tracks = append(tracks, BatchTrack{Path: p, Title: fmt.Sprintf("B%d", i), OldBPM: 120, OldStartMs: ptr(264)}) // all 14ms off
	}
	stub := &stubAnalyzer{det: dets}
	cache, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := NewBatch(stub, cache, BatchOptions{})
	results, ab := b.RunAutoBias(context.Background(), tracks, nil)
	if !ab.Applied || ab.Samples != 12 || math.Abs(ab.MedianMS-14) > 0.5 || ab.MADMS > 0.5 {
		t.Fatalf("auto-bias %+v want applied median~14", ab)
	}
	if stub.callCount() != 12 {
		t.Fatalf("re-plan re-analyzed: calls=%d want 12", stub.callCount())
	}
	for _, r := range results {
		if r.Plan.Status != StatusOK {
			t.Fatalf("post-bias plan %+v want OK (marker stays)", r.Plan)
		}
	}

	// spread-out offsets (not systematic): measured but NOT applied
	dir2 := t.TempDir()
	dets2 := map[string]*Detection{}
	var tracks2 []BatchTrack
	for i := 0; i < 12; i++ {
		p := writeAudioStub(t, dir2, fmt.Sprintf("s%d.mp3", i), "x")
		dets2[p] = det
		tracks2 = append(tracks2, BatchTrack{Path: p, Title: fmt.Sprintf("S%d", i), OldBPM: 120, OldStartMs: ptr(250 + float64(i*4))})
	}
	cache2, err := OpenDetectionCache(dir2)
	if err != nil {
		t.Fatal(err)
	}
	_, ab2 := NewBatch(&stubAnalyzer{det: dets2}, cache2, BatchOptions{}).RunAutoBias(context.Background(), tracks2, nil)
	if ab2.Applied {
		t.Fatalf("scattered offsets applied a bias: %+v", ab2)
	}
}
