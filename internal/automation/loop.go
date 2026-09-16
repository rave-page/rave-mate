package automation

import (
	"slices"
	"strings"
)

// Feedback-loop analysis: an automation re-triggers ITSELF when a chain step writes a file back
// into the watched folder AND that file would pass the automation's own Match - the watcher (or the
// next sweep) then starts another run over the file the last run produced, forever. The coordinator
// bounds the damage (serialized, capped queue), but the fix is to refuse the definite case at Save
// and skip a stored one at run time, so it never fires.

// LoopKind classifies a self-retrigger analysis.
type LoopKind int

const (
	LoopNone     LoopKind = iota
	LoopPossible          // a step writes into the watched folder, but the output ext may not match
	LoopDefinite          // ...and the output ext would pass the Match → it certainly re-triggers itself
)

// LoopReport is CheckLoop's outcome: the first offending step (1-based) + its output extension.
type LoopReport struct {
	Kind     LoopKind
	Step     int // 1-based step number for messages; 0 when Kind==LoopNone
	StepType ActionType
	Ext      string // output extension (".mp4"); "" when unknown (copy/move) or unresolved
}

// CheckLoop reports whether a's chain would re-trigger the automation: it leaves a NEW file in the
// watched folder (dir equal or nested) whose extension re-passes the Match. It threads the working
// file through the chain so a produced file that is later moved OUT of the watched folder does NOT
// count (the flagship [transcode → move to archive → delete] is fine); a copy-to duplicate left in
// the watched folder DOES. "Matches" is the Match EXTENSION gate only (extensions empty = any;
// pattern/min-size/min-age are treated as passing - a produced file can carry a matching name and
// is fresh/large enough on the next sweep). Definite = the extension certainly matches AND the
// producer is unconditional (transcode/copy); possible = the extension may not match, OR the
// producer can skip (trim-silence converges when there is no silence). presets resolves the
// transcode/trim output extension (nil ⇒ unknown → possible against a specific gate).
func CheckLoop(a Automation, presets PresetResolver) LoopReport {
	watch := strings.TrimSpace(a.WatchDir)
	if watch == "" {
		return LoopReport{}
	}
	cur := watch       // dir the working file lives in (starts in the watched folder)
	onOriginal := true // the working file is still the chain's input (which lives in the watch dir)
	produced := false  // a producing step has written a distinct new working file
	// The LAST producing step's output shape, for the final-resting-place check + copy duplicates:
	prodExt, prodStep, prodSoft := "", 0, false
	var prodType ActionType

	report := LoopReport{}
	consider := func(kind LoopKind, step int, typ ActionType, ext string) {
		switch {
		case kind == LoopDefinite && report.Kind != LoopDefinite:
			report = LoopReport{Kind: LoopDefinite, Step: step, StepType: typ, Ext: ext}
		case kind == LoopPossible && report.Kind == LoopNone:
			report = LoopReport{Kind: LoopPossible, Step: step, StepType: typ, Ext: ext}
		}
	}
	for i, act := range a.Actions {
		switch act.Type {
		case ActionTranscode:
			cur, onOriginal, produced = orDir(act.OutputDir, cur), false, true
			prodStep, prodType, prodSoft = i+1, act.Type, false
			prodExt = ""
			if p, ok := resolvePreset(presets, act, ""); ok {
				prodExt = p.Ext()
			}
		case ActionTrimSilence:
			// trim-silence content-conditionally SKIPS (no silence → no output), so it converges
			// rather than looping unconditionally: at most a possible loop, never a hard refusal.
			cur, onOriginal, produced = orDir(act.OutputDir, cur), false, true
			prodStep, prodType, prodSoft = i+1, act.Type, true
			prodExt = ""
			if p, ok := resolvePreset(presets, act, trimPresetID); ok {
				prodExt = p.Ext()
			}
		case ActionCopy:
			// A copy of a PRODUCED file into the watched folder is left behind as a new matching
			// file even if the working file later moves out - so check it here, unconditionally.
			// Copying the ORIGINAL (already in the watch dir) writes over itself: no new file.
			if !onOriginal && act.OutputDir != "" && withinOrEq(watch, act.OutputDir) {
				consider(loopKindOf(a.Match, prodExt, false), i+1, act.Type, prodExt)
			}
		case ActionMove:
			// Move relocates the working file. Moving the ORIGINAL within/into the watch dir is a
			// no-op relocation; a produced file's final resting place is checked after the loop.
			if act.OutputDir != "" {
				cur = act.OutputDir
			}
		}
	}
	// The final produced working file rests in cur; if that is the watched folder, it re-triggers.
	if produced && withinOrEq(watch, cur) {
		consider(loopKindOf(a.Match, prodExt, prodSoft), prodStep, prodType, prodExt)
	}
	return report
}

// loopKindOf classifies a produced file with ext against m's extension gate; a soft (skippable)
// producer is capped at possible (it converges rather than looping unconditionally).
func loopKindOf(m Match, ext string, soft bool) LoopKind {
	k := loopExtKind(m, ext)
	if soft && k == LoopDefinite {
		k = LoopPossible
	}
	return k
}

// loopExtKind classifies whether a produced file with ext would re-pass m's extension gate.
func loopExtKind(m Match, ext string) LoopKind {
	if len(m.Extensions) == 0 {
		return LoopDefinite // any file re-matches
	}
	if ext == "" {
		return LoopPossible // unknown output ext against a specific gate
	}
	if slices.Contains(m.Extensions, strings.ToLower(ext)) {
		return LoopDefinite
	}
	return LoopPossible // wrote into the folder, but with a non-matching extension
}

// withinOrEq reports whether child is dir itself or sits under it (reuses the coordinator's under).
func withinOrEq(dir, child string) bool {
	if strings.TrimSpace(child) == "" {
		return false
	}
	return under(dir, child)
}

func orDir(a, fallback string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return fallback
}
