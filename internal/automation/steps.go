package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/store"
)

// Step build (propose, no side effects) + commit (perform) per action. buildStep returns
// (proposal, proposedOutputPath, skipMessage, err); a non-empty skipMessage means "no-op,
// continue". commitStepSideEffects performs the proposal and returns the new current path.

func (m *Service) buildStep(ctx context.Context, rc *runContext, act Action, hintDir string) (any, string, string, error) {
	switch act.Type {
	case ActionRename:
		return m.buildRename(ctx, rc, act)
	case ActionTrimSilence:
		return m.buildTrim(ctx, rc, act, hintDir)
	case ActionTranscode:
		return m.buildTranscode(rc, act, hintDir)
	case ActionMove:
		return buildRelocate(rc, act, "move")
	case ActionCopy:
		return buildRelocate(rc, act, "copy")
	default:
		return nil, "", "", fmt.Errorf("unknown action %q", act.Type)
	}
}

func (m *Service) commitStepSideEffects(ctx context.Context, rc *runContext, act Action, proposal any, proposed string) (string, error) {
	switch act.Type {
	case ActionRename:
		return commitRename(rc.currentPath, proposed)
	case ActionTrimSilence:
		return m.commitTrim(ctx, rc, proposal, proposed)
	case ActionTranscode:
		return doTranscode(ctx, m.w, m.presets, act, rc.currentPath, 0)
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
	default:
		return rc.currentPath, nil
	}
}

// ── rename-from-event ────────────────────────────────────────────────────────

func (m *Service) buildRename(ctx context.Context, rc *runContext, act Action) (any, string, string, error) {
	base, token := m.bgCreds()
	if base == "" || token == "" {
		return nil, "", "API token not provided - skipping rename-from-event.", nil
	}
	fi, err := os.Stat(rc.currentPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("cannot stat file: %w", err)
	}
	events, err := fetchUserEvents(ctx, base, token)
	if err != nil {
		return nil, "", "", err
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
	ext := filepath.Ext(rc.currentPath)
	origBase := strings.TrimSuffix(filepath.Base(rc.currentPath), ext)
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
	date := dateBase.Format("2006-01-02")
	tmpl := act.Template
	if tmpl == "" {
		tmpl = defaultRenameTemplate
	}
	newName := applyTemplate(tmpl, date, venueSlug, eventSlug, sanitizeBasename(origBase), ext)
	proposed := filepath.Join(filepath.Dir(rc.currentPath), newName)
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

func (m *Service) buildTrim(ctx context.Context, rc *runContext, act Action, hintDir string) (any, string, string, error) {
	thr := act.ThresholdDb
	if thr == 0 {
		thr = defaultThresholdDb
	}
	minSil := act.MinSilenceSeconds
	if minSil == 0 {
		minSil = defaultMinSilenceSecs
	}
	lead, trail, dur, err := cachedSilenceProbe(ctx, m.st, m.w, rc.currentPath, thr, minSil)
	if err != nil {
		return nil, "", "", err
	}
	trimStartFlag := act.TrimStart == nil || *act.TrimStart
	trimEndFlag := act.TrimEnd == nil || *act.TrimEnd
	start := 0.0
	if trimStartFlag {
		start = lead
	}
	end := dur
	if trimEndFlag && dur > 0 {
		end = maxF(start+0.1, dur-trail)
	}
	if start <= 0.05 && (end <= 0 || (dur > 0 && absF(dur-end) < 0.05)) {
		return nil, "", "No leading/trailing silence detected - skipping trim.", nil
	}
	ext := filepath.Ext(rc.currentPath)
	base := sanitizeBasename(strings.TrimSuffix(filepath.Base(rc.currentPath), ext))
	dir := hintDir
	if dir == "" {
		dir = filepath.Dir(rc.currentPath)
	}
	proposed := filepath.Join(dir, base+"-trimmed"+ext)
	var durPtr *float64
	if dur > 0 {
		d := dur
		durPtr = &d
	}
	proposal := map[string]any{
		"kind": "trim-silence", "currentPath": rc.currentPath, "proposedOutputPath": proposed,
		"leadingSilenceSeconds": lead, "trailingSilenceSeconds": trail,
		"durationSeconds": durPtr, "trimStart": start, "trimEnd": end,
	}
	return proposal, proposed, "", nil
}

func (m *Service) commitTrim(ctx context.Context, rc *runContext, proposal any, proposed string) (string, error) {
	p, _ := proposal.(map[string]any)
	start, _ := p["trimStart"].(float64)
	end, _ := p["trimEnd"].(float64)
	if err := os.MkdirAll(filepath.Dir(proposed), 0o755); err != nil {
		return "", err
	}
	preset := "remux"
	params := map[string]any{"input": rc.currentPath, "output": proposed, "presetId": preset, "trimStart": start, "trimEnd": end}
	if _, err := m.w.RunStream(ctx, "transcode", "transcode.run", params, nil); err != nil {
		return "", fmt.Errorf("trim transcode: %w", err)
	}
	return proposed, nil
}

// ── transcode ────────────────────────────────────────────────────────────────

func (m *Service) buildTranscode(rc *runContext, act Action, hintDir string) (any, string, string, error) {
	if m.presets == nil {
		return nil, "", "Preset not found: " + act.PresetID, nil
	}
	preset, ok := m.presets(act.PresetID)
	if !ok {
		return nil, "", "Preset not found: " + act.PresetID, nil
	}
	dir := act.OutputDir
	if dir == "" {
		dir = hintDir
	}
	proposed := transcodeOut(dir, rc.currentPath, preset)
	proposal := map[string]any{
		"kind": "transcode", "currentPath": rc.currentPath, "proposedOutputPath": proposed,
		"presetId": preset.ID, "presetLabel": preset.Label,
	}
	return proposal, proposed, "", nil
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
