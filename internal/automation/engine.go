package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/transcode"
)

// runChain executes a's action chain over filePath, threading a "current file" through the
// steps. Output is always a NEW file per step; inputs are never overwritten (the sole exception
// is the terminal `delete` step, which consumes the working file - see runStep). Returns a Run
// recording per-step outcomes. Stops at the first failing step (status: error|partial).
func runChain(ctx context.Context, st *store.Store, w Worker, presets PresetResolver, log Logger, a Automation, filePath, trigger string) Run {
	started := time.Now().UTC().Format(time.RFC3339Nano)
	run := Run{
		ID:           chainID(a.ID, filePath, started),
		AutomationID: a.ID,
		Trigger:      trigger,
		FilePath:     filePath,
		StartedAt:    started,
		Status:       "running",
	}
	log.Info(source, "automation run started", map[string]any{
		"runId": run.ID, "automationId": a.ID, "file": filePath, "trigger": trigger, "steps": len(a.Actions),
	})

	current := filePath
	failed := false
	truncated := false // stopped early on a terminal step with actions still pending
	for i, act := range a.Actions {
		if ctx.Err() != nil {
			run.Steps = append(run.Steps, StepResult{Type: act.Type, Error: "cancelled"})
			failed = true
			break
		}
		step := StepResult{Type: act.Type}
		next, err := runStep(ctx, st, w, presets, act, a.WatchDir, current)
		if err != nil {
			step.Error = err.Error()
			run.Steps = append(run.Steps, step)
			log.Warn(source, "automation step failed", map[string]any{
				"runId": run.ID, "index": i, "type": act.Type, "error": err.Error(),
			})
			failed = true
			break
		}
		step.OK = true
		step.OutputPath = next // "" for delete: no file survives it
		run.Steps = append(run.Steps, step)
		log.Info(source, "automation step ok", map[string]any{
			"runId": run.ID, "index": i, "type": act.Type, "output": next,
		})
		if act.Type == ActionDelete {
			// Delete is terminal - the working file is gone. ValidateActions rejects trailing
			// steps up front, so this only fires for chains persisted before that check.
			if n := len(a.Actions) - i - 1; n > 0 {
				truncated = true
				log.Warn(source, "chain stopped after delete", map[string]any{
					"runId": run.ID, "skippedSteps": n,
				})
			}
			break
		}
		current = next
	}

	run.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	switch {
	case failed && len(run.Steps) <= 1:
		run.Status = "error"
	case failed, truncated:
		run.Status = "partial"
	default:
		run.Status = "success"
	}
	log.Info(source, "automation run finished", map[string]any{
		"runId": run.ID, "status": run.Status, "steps": len(run.Steps),
	})
	return run
}

// runStep runs one action over current and returns the new "current file" (== current for
// copy, which doesn't relocate the working file; "" for delete, which consumes it). root is the
// automation's watch dir - the containment boundary for the delete step.
func runStep(ctx context.Context, st *store.Store, w Worker, presets PresetResolver, act Action, root, current string) (string, error) {
	switch act.Type {
	case ActionTranscode:
		return doTranscode(ctx, w, presets, act, current, 0)

	case ActionTrimSilence:
		lead, err := detectLeadingSilence(ctx, st, w, current)
		if err != nil {
			return "", err
		}
		ta := act
		if ta.PresetID == "" {
			ta.PresetID = "remux"
		}
		return doTranscode(ctx, w, presets, ta, current, lead)

	case ActionMove:
		if act.OutputDir == "" {
			return "", errors.New("move-to: missing outputDir")
		}
		dest := filepath.Join(act.OutputDir, filepath.Base(current))
		if err := os.MkdirAll(act.OutputDir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir dest: %w", err)
		}
		if err := moveFile(current, dest); err != nil {
			return "", err
		}
		return dest, nil

	case ActionCopy:
		if act.OutputDir == "" {
			return "", errors.New("copy-to: missing outputDir")
		}
		dest := filepath.Join(act.OutputDir, filepath.Base(current))
		if err := os.MkdirAll(act.OutputDir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir dest: %w", err)
		}
		if err := copyFile(current, dest); err != nil {
			return "", err
		}
		// copy doesn't relocate the working file; current stays.
		return current, nil

	case ActionDelete:
		target, err := deleteTarget(root, current)
		if err != nil {
			return "", err
		}
		if err := localmedia.Delete(target); err != nil {
			return "", fmt.Errorf("delete: %w", err)
		}
		return "", nil // terminal: no file survives; runChain stops here

	default:
		return "", fmt.Errorf("unknown action type %q", act.Type)
	}
}

// deleteTarget validates that path is an existing regular file strictly inside root (the
// automation's watch dir) and returns its cleaned absolute form. Fails closed on every doubt -
// an empty path/root, an unresolvable path, a directory, or anything outside root - so a chain
// with a mis-set OutputDir can never reach past the watched folder and erase arbitrary files.
func deleteTarget(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("delete: no current file")
	}
	if strings.TrimSpace(root) == "" {
		return "", errors.New("delete: automation has no watch directory")
	}
	ap, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("delete: resolve file: %w", err)
	}
	ar, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("delete: resolve watch dir: %w", err)
	}
	if !withinDir(ar, ap) {
		return "", fmt.Errorf("delete: %s is outside the watch directory", filepath.Base(ap))
	}
	fi, err := os.Stat(ap)
	if err != nil {
		return "", fmt.Errorf("delete: %w", err)
	}
	if fi.IsDir() {
		return "", errors.New("delete: refusing to delete a directory")
	}
	return ap, nil
}

// withinDir reports whether path sits strictly under dir (dir itself is not "within"). Lexical:
// filepath.Rel compares case-insensitively on Windows, and a symlink out of dir deletes the link,
// not its target.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// doTranscode resolves act.PresetID (+ the action's loudness override), builds a new output path
// alongside (or in OutputDir), runs transcode.run with trimStart, and returns the output path.
func doTranscode(ctx context.Context, w Worker, presets PresetResolver, act Action, current string, trimStart float64) (string, error) {
	if presets == nil {
		return "", errors.New("unknown preset")
	}
	preset, ok := presets(act.PresetID)
	if !ok {
		return "", errors.New("unknown preset")
	}
	preset = applyActionLoudness(preset, act)
	out := transcodeOut(act.OutputDir, current, preset)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", fmt.Errorf("mkdir output: %w", err)
	}
	// Pass the FULL resolved preset inline (not just its id): the worker resolves builtin
	// ids but not the user's custom presets, so a custom-preset automation otherwise dies
	// with "unknown preset". The worker resolves the HW encoder from this preset's accel.
	params := map[string]any{
		"input":     current,
		"output":    out,
		"preset":    preset,
		"trimStart": trimStart,
		"trimEnd":   0.0,
	}
	if _, err := w.RunStream(ctx, "transcode", "transcode.run", params, nil); err != nil {
		return "", fmt.Errorf("transcode: %w", err)
	}
	return out, nil
}

// applyActionLoudness overlays an action's LUFS override onto the resolved preset.
//
// Semantics (back-compat by construction):
//   - act.LoudnessOn == false → preset returned untouched, so a preset that normalizes still
//     normalizes exactly as before. Every automation saved before these fields existed decodes
//     with LoudnessOn=false and therefore behaves identically.
//   - act.LoudnessOn == true → the action's target REPLACES the preset's loudness block
//     wholesale (on/I/TP/raise-only), rather than merging field-by-field: a half-overridden
//     target (action I + preset TP) is nobody's intent.
//
// There is no "force off" override - LoudnessOn=false means "don't override". To skip
// normalization, point the action at a preset that doesn't normalize.
//
// Zero values resolve to defaults: I → defaultLoudnessI (-14 LUFS streaming target); TP stays 0
// and transcode.Preset.EffectiveTP resolves it to transcode.DefaultLoudnessTP (-1 dBTP). The
// worker's transcode.NormalizePreset then clamps both to sane ranges and drops loudness entirely
// for copy/none audio codecs (normalizing needs a re-encode).
func applyActionLoudness(p transcode.Preset, act Action) transcode.Preset {
	if !act.LoudnessOn {
		return p
	}
	p.LoudnessOn = true
	p.LoudnessI = act.LoudnessI
	if p.LoudnessI == 0 {
		p.LoudnessI = defaultLoudnessI
	}
	p.LoudnessTP = act.LoudnessTP
	p.LoudnessRaiseOnly = act.LoudnessRaiseOnly
	return p
}

// detectLeadingSilence runs the (cached) transcode.silence probe and returns leadingSeconds.
// Uses default params (0 → worker's -50dB/2s, identical wire call to the pre-cache behavior),
// sharing the KindSilence cache with ProbeSilence/buildTrim on default-param probes.
func detectLeadingSilence(ctx context.Context, st *store.Store, w Worker, current string) (float64, error) {
	lead, _, _, err := cachedSilenceProbe(ctx, st, w, current, 0, 0)
	if err != nil {
		return 0, err
	}
	return lead, nil
}

// transcodeOut builds the new output path: <outDir>/<base>-<presetId><ext>. outDir defaults
// to the input's directory. Never collides with the input (preset id + ext suffix).
func transcodeOut(outDir, current string, preset transcode.Preset) string {
	if outDir == "" {
		outDir = filepath.Dir(current)
	}
	name := filepath.Base(current)
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return filepath.Join(outDir, base+"-"+preset.ID+preset.Ext())
}

// moveFile renames src→dst, falling back to copy+remove on a cross-device rename error.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove src after copy: %w", err)
	}
	return nil
}

// copyFile copies src→dst (O_CREATE|O_TRUNC, 0o644).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open dst: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close dst: %w", err)
	}
	return nil
}

// chainID derives a stable run id from automation id + file + start timestamp.
func chainID(automationID, filePath, started string) string {
	sum := sha256.Sum256([]byte(automationID + "|" + filePath + "|" + started))
	return automationID + "-" + hex.EncodeToString(sum[:6])
}
