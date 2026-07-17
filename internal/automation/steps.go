package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/store"
)

// Step build (propose, no side effects) + commit (perform) per action. buildStep returns
// (proposal, proposedOutputPath, skipMessage, err); a non-empty skipMessage means "no-op,
// continue". commitStepSideEffects performs the proposal and returns the new current path.
//
// The plan* helpers here are the single source of truth for WHAT a step does: the unattended
// engine path (runStep) calls the same ones, so a proposal a human approves is what the run
// performs, and a background run does what the manual preview showed.

func (m *Service) buildStep(ctx context.Context, rc *runContext, act Action) (any, string, string, error) {
	switch act.Type {
	case ActionRename:
		return m.buildRename(ctx, rc, act)
	case ActionTrimSilence:
		return m.buildTrim(ctx, rc, act)
	case ActionTranscode:
		return m.buildTranscode(rc, act)
	case ActionMove:
		return buildRelocate(rc, act, "move")
	case ActionCopy:
		return buildRelocate(rc, act, "copy")
	case ActionDelete:
		return buildDelete(rc)
	default:
		return nil, "", "", fmt.Errorf("unknown action %q", act.Type)
	}
}

func (m *Service) commitStepSideEffects(ctx context.Context, rc *runContext, act Action, proposal any, proposed string) (string, error) {
	switch act.Type {
	case ActionRename:
		return commitRename(rc.currentPath, proposed)
	case ActionTrimSilence:
		return m.commitTrim(ctx, rc, act, proposal)
	case ActionTranscode:
		return doTranscode(ctx, m.engine(), act, rc.currentPath, "", 0, 0)
	case ActionMove:
		dest := relocateDest(act.OutputDir, rc.currentPath)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := moveFile(rc.currentPath, dest); err != nil {
			return "", err
		}
		return dest, nil
	case ActionCopy:
		dest := relocateDest(act.OutputDir, rc.currentPath)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := copyFile(rc.currentPath, dest); err != nil {
			return "", err
		}
		return rc.currentPath, nil // copy doesn't relocate the working file
	case ActionDelete:
		// rc.origPath, NEVER rc.currentPath - see the runStep ActionDelete case for why.
		// Re-validate at commit: in manual mode the proposal sat in front of a human, and the
		// file may have moved/vanished/been swapped for a directory since it was built. The
		// survivor gate re-runs here too - buildDelete's verdict is not authority to destroy.
		if err := rc.surv.err(); err != nil {
			return "", err
		}
		target, err := deleteTarget(rc.auto.WatchDir, rc.origPath)
		if err != nil {
			return "", err
		}
		if err := localmedia.Delete(target); err != nil {
			return "", fmt.Errorf("delete: %w", err)
		}
		return "", nil // terminal: the chain's input is consumed; runInteractive stops here
	default:
		return rc.currentPath, nil
	}
}

// ── rename-from-event ────────────────────────────────────────────────────────

// planRename resolves the new path for a rename-from-event step over current: the file's mtime is
// matched against the caller's booked rave.page events (±BufferMinutes) and the winner fills the
// template. Returns (target, matchedEvent, skipMsg, err); matched is nil when no booking matched -
// the rename still proceeds with noEventSlug placeholders. Shared by both run paths.
func planRename(ctx context.Context, apiBase, token string, act Action, current string) (string, *MatchedEvent, string, error) {
	if apiBase == "" || token == "" {
		return "", nil, "API token not provided - skipping rename-from-event.", nil
	}
	fi, err := os.Stat(current)
	if err != nil {
		return "", nil, "", fmt.Errorf("cannot stat file: %w", err)
	}
	events, err := fetchUserEvents(ctx, apiBase, token)
	if err != nil {
		return "", nil, "", err
	}
	buf := act.BufferMinutes
	if buf == 0 {
		buf = defaultBufferMinutes
	}
	// No booked event in the window is non-fatal: the event/venue tokens fall back to
	// placeholders so {YYYY-MM-DD}/{originalBasename} templating still renames the file.
	// Skipping the whole rename here would leave recordings made outside any booking
	// untouched.
	matched := pickMatchingEvent(events, fi.ModTime().UnixMilli(), buf)
	ext := filepath.Ext(current)
	origBase := strings.TrimSuffix(filepath.Base(current), ext)
	dateBase := fi.ModTime().UTC()
	venueSlug := noEventSlug
	eventSlug := noEventSlug
	if matched != nil {
		if matched.StartsAt != nil {
			if ms, ok := parseMs(*matched.StartsAt); ok {
				dateBase = time.UnixMilli(ms).UTC()
			}
		}
		venueSlug = slugify(deref(matched.VenueName), "venue")
		eventSlug = slugify(orStr(deref(matched.Slug), matched.Title), matched.ID)
	}
	tmpl := act.Template
	if tmpl == "" {
		tmpl = defaultRenameTemplate
	}
	newName := applyTemplate(tmpl, dateBase.Format("2006-01-02"), venueSlug, eventSlug, sanitizeBasename(origBase), ext)
	return filepath.Join(filepath.Dir(current), newName), matched, "", nil
}

func (m *Service) buildRename(ctx context.Context, rc *runContext, act Action) (any, string, string, error) {
	base, token := m.bgCreds()
	proposed, matched, skip, err := planRename(ctx, base, token, act, rc.currentPath)
	if err != nil || skip != "" {
		return nil, "", skip, err
	}
	proposal := map[string]any{
		"kind": "rename", "currentPath": rc.currentPath, "proposedPath": proposed,
		"matchedEvent": matched, // nil when no booking matched the timestamp
	}
	return proposal, proposed, "", nil
}

// commitRename moves currentPath→target, de-colliding with -2/-3… suffixes.
func commitRename(current, target string) (string, error) {
	if target == current {
		return target, nil
	}
	target = dedupePath(target)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := moveFile(current, target); err != nil {
		return "", err
	}
	return target, nil
}

// ── trim-silence ─────────────────────────────────────────────────────────────

// trimPlan is the cut window resolved from a trim-silence action + the (cached) silence probe.
// Start/End are absolute seconds into the source; End <= Start means "to the end of the file"
// (transcode.Job.Args). Duration is 0 when the probe couldn't report one.
type trimPlan struct {
	Lead, Trail, Duration float64
	Start, End            float64
}

// planTrim probes current with the ACTION's own threshold + min-silence (0 → the engine defaults,
// which cachedSilenceProbe normalizes so the cache key matches a default-param probe) and resolves
// the cut window, honoring the TrimStart/TrimEnd flags (unset = on, the editor's default). A
// non-empty skipMsg means there is no silence worth cutting - the step is a no-op.
func planTrim(ctx context.Context, e engine, act Action, current string) (trimPlan, string, error) {
	lead, trail, dur, err := cachedSilenceProbe(ctx, e.st, e.w, current, act.ThresholdDb, act.MinSilenceSeconds)
	if err != nil {
		return trimPlan{}, "", err
	}
	p := trimPlan{Lead: lead, Trail: trail, Duration: dur}
	if act.TrimStart == nil || *act.TrimStart {
		p.Start = lead
	}
	p.End = dur // trim-end off (or no known duration) → encode to the end
	if (act.TrimEnd == nil || *act.TrimEnd) && dur > 0 {
		p.End = maxF(p.Start+0.1, dur-trail) // never invert the window on an all-silent file
	}
	if p.Start <= 0.05 && (p.End <= 0 || (dur > 0 && absF(dur-p.End) < 0.05)) {
		return p, "No leading/trailing silence detected - skipping trim.", nil
	}
	return p, "", nil
}

func (m *Service) buildTrim(ctx context.Context, rc *runContext, act Action) (any, string, string, error) {
	preset, ok := resolvePreset(m.presets, act, trimPresetID)
	if !ok {
		return nil, "", "Preset not found: " + orStr(act.PresetID, trimPresetID), nil
	}
	plan, skip, err := planTrim(ctx, m.engine(), act, rc.currentPath)
	if err != nil || skip != "" {
		return nil, "", skip, err
	}
	// transcodeOut, not a hand-rolled "-trimmed" name: commit runs doTranscode, which derives the
	// output path this same way. Any other rule here proposes a file the run won't write.
	proposed := transcodeOut(act.OutputDir, rc.currentPath, preset)
	var durPtr *float64
	if plan.Duration > 0 {
		d := plan.Duration
		durPtr = &d
	}
	proposal := map[string]any{
		"kind": "trim-silence", "currentPath": rc.currentPath, "proposedOutputPath": proposed,
		"leadingSilenceSeconds": plan.Lead, "trailingSilenceSeconds": plan.Trail,
		"durationSeconds": durPtr, "trimStart": plan.Start, "trimEnd": plan.End,
	}
	return proposal, proposed, "", nil
}

// commitTrim cuts the window the proposal carried - re-probing here could hand the encoder a
// different window than the human approved. Same doTranscode as the unattended path, so the
// action's preset, output folder and loudness override all apply.
func (m *Service) commitTrim(ctx context.Context, rc *runContext, act Action, proposal any) (string, error) {
	p, _ := proposal.(map[string]any)
	start, _ := p["trimStart"].(float64)
	end, _ := p["trimEnd"].(float64)
	return doTranscode(ctx, m.engine(), act, rc.currentPath, trimPresetID, start, end)
}

// ── transcode ────────────────────────────────────────────────────────────────

func (m *Service) buildTranscode(rc *runContext, act Action) (any, string, string, error) {
	// resolvePreset, not a raw m.presets lookup: the proposal must show what commit will actually
	// do, and commit's own resolvePreset drops loudness for a copy/none audio codec + clamps the
	// targets. Reading it raw here promised normalization the encode was never going to run.
	preset, ok := resolvePreset(m.presets, act, "")
	if !ok {
		return nil, "", "Preset not found: " + act.PresetID, nil
	}
	proposed := transcodeOut(act.OutputDir, rc.currentPath, preset)
	proposal := map[string]any{
		"kind": "transcode", "currentPath": rc.currentPath, "proposedOutputPath": proposed,
		"presetId": preset.ID, "presetLabel": preset.Label,
	}
	// Loudness keys appear only when normalizing, so a non-normalizing chain's proposal stays
	// byte-identical to the pre-loudness wire shape.
	if preset.LoudnessOn {
		proposal["loudnessOn"] = true
		proposal["loudnessI"] = preset.LoudnessI
		proposal["loudnessTP"] = preset.EffectiveTP()
		proposal["loudnessRaiseOnly"] = preset.LoudnessRaiseOnly
	}
	return proposal, proposed, "", nil
}

// ── delete ───────────────────────────────────────────────────────────────────

// buildDelete proposes deleting the chain's ORIGINAL input file (rc.origPath), not the working
// file. It ALWAYS returns a non-nil proposal (never a skip message): that is what gates the step
// behind an explicit commit in manual mode - the one destructive step must never slip through
// unconfirmed. Guard errors surface here, at propose time, so a manual run shows why instead of
// offering a delete it would refuse to perform. The proposal carries size+mtime so the UI can
// show what is about to be erased; "currentPath" is the wire's generic "path this step acts on"
// key (shape parity with the other proposals) and names the original.
func buildDelete(rc *runContext) (any, string, string, error) {
	// Same survivor gate as runStep, raised at propose time: never offer a human a delete that
	// would leave them with no copy of the recording.
	if err := rc.surv.err(); err != nil {
		return nil, "", "", err
	}
	target, err := deleteTarget(rc.auto.WatchDir, rc.origPath)
	if err != nil {
		return nil, "", "", err
	}
	proposal := map[string]any{"kind": "delete", "currentPath": target}
	if fi, err := os.Stat(target); err == nil {
		proposal["sizeBytes"] = fi.Size()
		proposal["modifiedAt"] = fi.ModTime().UTC().Format(time.RFC3339)
	}
	return proposal, "", "", nil // no proposed output path: delete produces no file
}

// ── move / copy ──────────────────────────────────────────────────────────────

func buildRelocate(rc *runContext, act Action, kind string) (any, string, string, error) {
	if act.OutputDir == "" {
		return nil, "", "", fmt.Errorf("%s-to: missing outputDir", kind)
	}
	dest := relocateDest(act.OutputDir, rc.currentPath)
	proposal := map[string]any{"kind": "move", "currentPath": rc.currentPath, "proposedOutputPath": dest}
	return proposal, dest, "", nil
}

func relocateDest(outDir, current string) string {
	return filepath.Join(outDir, filepath.Base(current))
}

// ── silence probe ────────────────────────────────────────────────────────────

// silenceProbe runs the worker silence pass → (leading, trailing, duration, err).
func silenceProbe(ctx context.Context, w Worker, path string, thresholdDb, minSilence float64) (float64, float64, float64, error) {
	raw, err := w.RunStream(ctx, "transcode", "transcode.silence",
		map[string]any{"path": path, "thresholdDb": thresholdDb, "minSilence": minSilence}, nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("silence detect: %w", err)
	}
	var res struct {
		Leading  float64 `json:"leadingSeconds"`
		Trailing float64 `json:"trailingSeconds"`
		Duration float64 `json:"durationSeconds"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, 0, 0, fmt.Errorf("silence decode: %w", err)
	}
	return res.Leading, res.Trailing, res.Duration, nil
}

// silenceCache is the persisted silence-probe result. Params (td/ms) are folded in and
// validated on read: the probe depends on file bytes + threshold + min-silence, so a params
// mismatch is a cache miss (re-probe). Kept under KindSilence so store.RetagAnalyses re-keys it
// across a self-inflicted tag rewrite (audio bytes, hence silence, unchanged).
type silenceCache struct {
	ThresholdDb float64 `json:"td"`
	MinSilence  float64 `json:"ms"`
	Leading     float64 `json:"l"`
	Trailing    float64 `json:"t"`
	Duration    float64 `json:"d"`
}

// cachedSilenceProbe fronts silenceProbe with the store's KindSilence cache, keyed by path +
// file mtime (version slot); params are folded into the blob and validated on read. Nil *Store
// or an unstattable path degrades to a direct probe (no-op cache). Normalizes 0-params to the
// defaults so the wire call + key match whether the caller passed explicit defaults or 0
// (the worker applies the same -50dB/2s defaults on 0).
func cachedSilenceProbe(ctx context.Context, st *store.Store, w Worker, path string, thresholdDb, minSilence float64) (float64, float64, float64, error) {
	if thresholdDb == 0 {
		thresholdDb = defaultThresholdDb
	}
	if minSilence == 0 {
		minSilence = defaultMinSilenceSecs
	}
	var mtime int64
	if fi, err := os.Stat(path); err == nil {
		mtime = fi.ModTime().Unix()
	}
	if mtime > 0 {
		if raw, ok := st.GetAnalysis(store.KindSilence, path, mtime); ok {
			var c silenceCache
			if json.Unmarshal(raw, &c) == nil && c.ThresholdDb == thresholdDb && c.MinSilence == minSilence {
				return c.Leading, c.Trailing, c.Duration, nil
			}
		}
	}
	lead, trail, dur, err := silenceProbe(ctx, w, path, thresholdDb, minSilence)
	if err != nil {
		return 0, 0, 0, err
	}
	if mtime > 0 {
		if blob, e := json.Marshal(silenceCache{thresholdDb, minSilence, lead, trail, dur}); e == nil {
			st.PutAnalysis(store.KindSilence, path, mtime, blob)
		}
	}
	return lead, trail, dur, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// dedupePath returns path, or path-2/path-3… if it (and earlier suffixes) exist.
func dedupePath(path string) string {
	if _, err := os.Stat(path); err != nil {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s-%d%s", base, n, ext)
		if _, err := os.Stat(cand); err != nil {
			return cand
		}
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
