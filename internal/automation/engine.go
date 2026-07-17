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

// engine is the dependency set one chain run needs. Bundled so runChain/runStep stay free
// functions (testable without a Manager) instead of growing a parameter list nobody can read.
// The zero engine is legal: every step that needs a dependency checks it and fails closed.
type engine struct {
	st      *store.Store
	w       Worker
	presets PresetResolver
	creds   func() (apiBaseURL, token string) // rename-from-event event lookup; nil = none
}

// credentials returns the API base+token for rename-from-event ("" = not configured).
func (e engine) credentials() (string, string) {
	if e.creds == nil {
		return "", ""
	}
	return e.creds()
}

// skipStep marks a step that legitimately did nothing (no API credentials for a rename, no
// silence worth trimming). NOT a failure: the working file is untouched and the chain carries on,
// but the run reports partial - a step the user asked for did not happen. Mirrors the interactive
// path's skipMsg, so both paths report the same non-events the same way.
type skipStep struct{ msg string }

func (s skipStep) Error() string { return s.msg }

// survivors is the run-time half of the delete invariant: delete may only erase the original if
// a distinct artifact ACTUALLY survives it. Validate can only see step types, and a producing
// step is allowed to skip (planTrim finds no silence to cut; a human skips the gate), so a chain
// that validated as "transcode first, then drop the source" can reach the delete having produced
// nothing at all - the original would be the only copy. Both run paths feed this: runChain and
// runInteractive.
type survivors struct {
	producing int    // steps that supersede the original (transcode/trim/rename) reached before delete
	produced  int    // ...of those, the ones that actually happened - a skip counts none
	lastSkip  string // why the last such step did nothing, for the refusal message
}

// step records one action's outcome. Only steps that supersede the original count (transcode/
// trim produce a distinct file; rename relocates the original) - a skipped one leaves the original
// as the sole copy. copy/move don't count (see supersedesOriginal).
func (s *survivors) step(a Action, skipped bool, skipMsg string) {
	if !supersedesOriginal(a) {
		return
	}
	s.producing++
	if skipped {
		s.lastSkip = skipMsg
		return
	}
	s.produced++
}

// err reports why a delete must refuse, nil if it may proceed. A chain with NO producing steps
// ([delete], [copy, delete]) is an explicit purge/archive-purge - allowed. A chain that asked
// for a new file and got none is the total-data-loss shape - refused.
func (s survivors) err() error {
	if s.producing == 0 || s.produced > 0 {
		return nil
	}
	why := s.lastSkip
	if why == "" {
		why = "The step that would have produced it did nothing."
	}
	return fmt.Errorf("delete refused - nothing survives the original: %s No new file was produced, "+
		"so erasing the recording the chain started from would leave no copy of it at all", why)
}

// runChain executes a's action chain over filePath, threading a "current file" through the
// steps. Output is always a NEW file per step; inputs are never overwritten. The sole exception
// is the terminal `delete` step, and it consumes the chain's ORIGINAL input (filePath) - never
// the threaded current file, see runStep. Returns a Run recording per-step outcomes. Stops at
// the first failing step (status: error|partial).
func runChain(ctx context.Context, e engine, log Logger, a Automation, filePath, trigger string) Run {
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
	skipped := false   // a step legitimately did nothing (skipStep)
	truncated := false // stopped early on a terminal step with actions still pending
	var surv survivors // gates the delete: no distinct artifact produced ⇒ no delete
	for i, act := range a.Actions {
		if ctx.Err() != nil {
			run.Steps = append(run.Steps, StepResult{Type: act.Type, Error: "cancelled"})
			failed = true
			break
		}
		step := StepResult{Type: act.Type}
		next, err := runStep(ctx, e, act, a.WatchDir, filePath, current, surv)
		var skip skipStep
		if errors.As(err, &skip) {
			// No-op, not a failure: keep the working file and carry on (run → partial).
			surv.step(act, true, skip.msg)
			step.OK = true
			step.OutputPath = current
			run.Steps = append(run.Steps, step)
			log.Info(source, "automation step skipped", map[string]any{
				"runId": run.ID, "index": i, "type": act.Type, "reason": skip.msg,
			})
			skipped = true
			continue
		}
		if err != nil {
			step.Error = err.Error()
			run.Steps = append(run.Steps, step)
			log.Warn(source, "automation step failed", map[string]any{
				"runId": run.ID, "index": i, "type": act.Type, "error": err.Error(),
			})
			failed = true
			break
		}
		surv.step(act, false, "")
		step.OK = true
		step.OutputPath = next // "" for delete: it erases the chain's input and produces no file
		run.Steps = append(run.Steps, step)
		log.Info(source, "automation step ok", map[string]any{
			"runId": run.ID, "index": i, "type": act.Type, "output": next,
		})
		if act.Type == ActionDelete {
			// Delete is terminal - it consumes the chain's INPUT (an earlier transcode/copy output
			// may well survive it), so any trailing step would run against a file whose provenance
			// is now ambiguous. ValidateActions rejects trailing steps up front, so this only fires
			// for chains persisted before that check.
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
	case failed, truncated, skipped:
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
// copy, which doesn't relocate the working file; "" for delete). root is the automation's watch
// dir - the containment boundary for the delete step. orig is the chain's original input file,
// which is what delete erases - see the ActionDelete case. surv carries what the chain has
// produced so far, and only the delete step reads it. A skipStep error means the step was a
// legitimate no-op; runChain keeps the working file and continues.
//
// Every case delegates to the SAME plan/commit helper the interactive path's buildStep +
// commitStepSideEffects use (steps.go), so an unattended run and a manually gated one do the same
// work - the two paths diverging is what let rename-from-event ship dead on this one.
func runStep(ctx context.Context, e engine, act Action, root, orig, current string, surv survivors) (string, error) {
	switch act.Type {
	case ActionRename:
		base, token := e.credentials()
		target, _, skip, err := planRename(ctx, base, token, act, current)
		if err != nil {
			return "", err
		}
		if skip != "" {
			return "", skipStep{skip}
		}
		return commitRename(current, target)

	case ActionTranscode:
		return doTranscode(ctx, e, act, current, "", 0, 0)

	case ActionTrimSilence:
		plan, skip, err := planTrim(ctx, e, act, current)
		if err != nil {
			return "", err
		}
		if skip != "" {
			return "", skipStep{skip}
		}
		return doTranscode(ctx, e, act, current, trimPresetID, plan.Start, plan.End)

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
		// Refuse unless a distinct artifact really survives: the chain may have validated as
		// [trim-silence, delete] and then found no silence to cut, producing nothing.
		if err := surv.err(); err != nil {
			return "", err
		}
		// Targets orig, NEVER current: [transcode, delete] means "convert the recording, drop the
		// source" - the output must survive. Resolving against current would erase whatever the
		// previous step just produced (a transcode output, or a copy relocated INSIDE the watch
		// dir), leaving the recording nowhere. Fails closed when orig is gone (an earlier move
		// took it) - deleteTarget stats it; there is deliberately no fallback to current.
		target, err := deleteTarget(root, orig)
		if err != nil {
			return "", err
		}
		if err := localmedia.Delete(target); err != nil {
			return "", fmt.Errorf("delete: %w", err)
		}
		return "", nil // terminal: the chain's input is consumed; runChain stops here

	default:
		return "", fmt.Errorf("unknown action type %q", act.Type)
	}
}

// deleteTarget validates that path (the chain's original input file) is an existing regular file
// strictly inside root (the automation's watch dir) and returns its cleaned absolute form. Fails
// closed on every doubt - an empty path/root, an unresolvable path, a directory, or anything
// outside root. Containment is defense-in-depth now that it guards the ORIGINAL: that path comes
// from the watch-dir sweep/watcher, so it is definitionally inside root, and a caller handing in
// a file from elsewhere (a hand-driven RunManual, a stale persisted chain) is refused rather than
// trusted. The stat is also the fail-closed check for "an earlier step already relocated it".
func deleteTarget(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("delete: no input file")
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

// resolvePreset resolves act.PresetID (dflt when blank) and returns the preset the worker will
// actually encode with: the action's loudness override applied, then NormalizePreset - the exact
// coercion tcRun performs (it re-normalizes; NormalizePreset is idempotent). Proposals AND commits
// both resolve through here: a manual gate that shows loudness the encode silently drops (which
// NormalizePreset does for copy/none audio codecs) is a lie the user is asked to approve.
func resolvePreset(presets PresetResolver, act Action, dflt string) (transcode.Preset, bool) {
	if presets == nil {
		return transcode.Preset{}, false
	}
	id := act.PresetID
	if id == "" {
		id = dflt
	}
	p, ok := presets(id)
	if !ok {
		return transcode.Preset{}, false
	}
	return transcode.NormalizePreset(applyActionLoudness(p, act)), true
}

// doTranscode resolves the action's preset, builds a new output path alongside (or in OutputDir),
// runs transcode.run over the [trimStart, trimEnd) window, and returns the output path.
// trimEnd <= trimStart = encode to the end of the file (transcode.Job.Args).
func doTranscode(ctx context.Context, e engine, act Action, current, dflt string, trimStart, trimEnd float64) (string, error) {
	preset, ok := resolvePreset(e.presets, act, dflt)
	if !ok {
		return "", errors.New("unknown preset")
	}
	if e.w == nil {
		return "", errors.New("worker unavailable")
	}
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
		"trimEnd":   trimEnd,
	}
	if _, err := e.w.RunStream(ctx, "transcode", "transcode.run", params, nil); err != nil {
		return "", fmt.Errorf("transcode: %w", err)
	}
	return out, nil
}

// applyActionLoudness overlays an action's LUFS override onto the resolved preset. The overlay
// rules (off = don't override, on = replace the block wholesale, 0 = default) live in
// transcode.ApplyLoudnessOverride - every override surface shares them.
func applyActionLoudness(p transcode.Preset, act Action) transcode.Preset {
	return transcode.ApplyLoudnessOverride(p, act.LoudnessOn, act.LoudnessI, act.LoudnessTP, act.LoudnessRaiseOnly)
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
