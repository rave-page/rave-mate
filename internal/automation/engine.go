package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/transcode"
)

// runChain executes a's action chain over filePath, threading a "current file" through the
// steps. Output is always a NEW file per step; inputs are never overwritten. Returns a Run
// recording per-step outcomes. Stops at the first failing step (status: error|partial).
func runChain(ctx context.Context, w Worker, presets PresetResolver, log Logger, a Automation, filePath, trigger string) Run {
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
	for i, act := range a.Actions {
		if ctx.Err() != nil {
			run.Steps = append(run.Steps, StepResult{Type: act.Type, Error: "cancelled"})
			failed = true
			break
		}
		step := StepResult{Type: act.Type}
		next, err := runStep(ctx, w, presets, act, current)
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
		step.OutputPath = next
		current = next
		run.Steps = append(run.Steps, step)
		log.Info(source, "automation step ok", map[string]any{
			"runId": run.ID, "index": i, "type": act.Type, "output": next,
		})
	}

	run.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	switch {
	case !failed:
		run.Status = "success"
	case len(run.Steps) <= 1:
		run.Status = "error"
	default:
		run.Status = "partial"
	}
	log.Info(source, "automation run finished", map[string]any{
		"runId": run.ID, "status": run.Status, "steps": len(run.Steps),
	})
	return run
}

// runStep runs one action over current and returns the new "current file" (== current for
// copy, which doesn't relocate the working file).
func runStep(ctx context.Context, w Worker, presets PresetResolver, act Action, current string) (string, error) {
	switch act.Type {
	case ActionTranscode:
		return doTranscode(ctx, w, presets, act, current, 0)

	case ActionTrimSilence:
		lead, err := detectLeadingSilence(ctx, w, current)
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

	default:
		return "", fmt.Errorf("unknown action type %q", act.Type)
	}
}

// doTranscode resolves act.PresetID, builds a new output path alongside (or in OutputDir),
// runs transcode.run with trimStart, and returns the output path.
func doTranscode(ctx context.Context, w Worker, presets PresetResolver, act Action, current string, trimStart float64) (string, error) {
	if presets == nil {
		return "", errors.New("unknown preset")
	}
	preset, ok := presets(act.PresetID)
	if !ok {
		return "", errors.New("unknown preset")
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
		"trimEnd":   0.0,
	}
	if _, err := w.RunStream(ctx, "transcode", "transcode.run", params, nil); err != nil {
		return "", fmt.Errorf("transcode: %w", err)
	}
	return out, nil
}

// detectLeadingSilence runs transcode.silence and returns leadingSeconds.
func detectLeadingSilence(ctx context.Context, w Worker, current string) (float64, error) {
	raw, err := w.RunStream(ctx, "transcode", "transcode.silence", map[string]any{"path": current}, nil)
	if err != nil {
		return 0, fmt.Errorf("silence detect: %w", err)
	}
	var res struct {
		LeadingSeconds  float64 `json:"leadingSeconds"`
		TrailingSeconds float64 `json:"trailingSeconds"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("silence decode: %w", err)
	}
	return res.LeadingSeconds, nil
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
