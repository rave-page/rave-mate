// Package cuewriteback pushes edited cue data into installed DJ softwares' libraries:
// target detection (cheap fs probes) + backup-first apply. Extracted from webui so
// remotectl (peer RPC) drives the same write path without importing the UI. Behavior
// contract: backup before every library write, refuse VirtualDJ while it runs (it
// rewrites database.xml from memory on exit), Serato writes per-file (no library backup).
package cuewriteback

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/serato"
	"rave.page/mate/internal/seratolib"
	"rave.page/mate/internal/sysactivity"
)

// Target is one detected DJ-software write destination.
type Target struct {
	Key   string `json:"key"`   // "traktor" | "rekordbox" | "virtualdj" | "serato"
	Label string `json:"label"` // product name (not translated)
	Path  string `json:"path"`  // file (NML/XML) or _Serato_ dir the write hits
}

// DetectTargets probes which DJ libraries exist on this machine (cheap fs probes).
// nmlOverride is the configured Traktor collection path ("" = auto-discover).
func DetectTargets(nmlOverride string) []Target {
	var out []Target
	if p := nmlPath(nmlOverride); p != "" {
		out = append(out, Target{"traktor", "Traktor", p})
	}
	if installs, err := musiclib.DiscoverRekordbox(); err == nil && len(installs) > 0 {
		out = append(out, Target{"rekordbox", "Rekordbox", installs[0].XML})
	}
	if p, err := musiclib.DiscoverVirtualDJ(); err == nil && p != "" {
		out = append(out, Target{"virtualdj", "VirtualDJ", p})
	}
	if dirs := serato.DetectSeratoDirs(); len(dirs) > 0 {
		out = append(out, Target{"serato", "Serato", dirs[0]})
	}
	return out
}

// nmlPath resolves the Traktor collection file the write actions target.
func nmlPath(override string) string {
	if override != "" {
		return override
	}
	if installs, err := musiclib.DiscoverTraktor(); err == nil && len(installs) > 0 {
		return installs[0].Collection
	}
	return ""
}

// BackupFile copies a library file into backupRoot before a write.
func BackupFile(backupRoot, app, path string) error {
	if backupRoot == "" {
		return fmt.Errorf("no backup dir")
	}
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return err
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	name := fmt.Sprintf("%s-%s-%s", app, time.Now().Format("20060102-150405"), filepath.Base(path))
	dst, err := os.Create(filepath.Join(backupRoot, name))
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

// ApplyCues writes updates into t's library, backup-first into backupRoot. Refuses while
// VirtualDJ is running; Serato = per-file temp+verify+rename with its own running-refusal.
// Export invariant enforced here for EVERY entry point (local rail + peer RPC): only the
// target's scope ships, and pads are numbered in track-time order (pad 0 = the earliest
// cue - left-to-right, top-to-bottom pad rows).
// TraktorRunning reports a running Traktor. Exact "traktor" covers the TP≤3 exe; the prefix
// covers versioned exes ("Traktor Pro 4") without matching our own "rave-mate-feature-traktor"
// child. Shared by every collection.nml writer (cues here, beatgrids in webui gridfix).
func TraktorRunning() bool {
	set, ok := sysactivity.New().RunningProcesses()
	return ok && (sysactivity.Running(set, "traktor") || sysactivity.RunningPrefix(set, "traktor pro"))
}

func ApplyCues(t Target, updates []musiclib.CueUpdate, backupRoot string) (musiclib.WritebackResult, error) {
	var zero musiclib.WritebackResult
	for i := range updates {
		cues := cuepattern.FilterForSoftware(updates[i].Cues, t.Key)
		cues, _ = cuepattern.DedupeCues(cues, 5) // stacked double-write dupes never reach a pad
		cues, _ = cuepattern.RenumberPadsByTime(cues, "", 0)
		updates[i].Cues = cues
	}
	switch t.Key {
	case "traktor":
		// Traktor holds the collection in memory: it never reloads a live file edit and
		// overwrites the file from memory on save/exit - a write under a running Traktor
		// silently vanishes. Refuse, like the VDJ guard below.
		if TraktorRunning() {
			return zero, fmt.Errorf("%s", i18n.T("library.gf.traktorRunning"))
		}
		// safety: full collection backup before the write
		if installs, err := musiclib.DiscoverTraktor(); err == nil && len(installs) > 0 && installs[0].Collection != "" {
			if _, berr := musiclib.BackupCollection(installs[0], backupRoot); berr != nil {
				return zero, fmt.Errorf("%s%s", i18n.T("library.gf.backupFailed"), berr.Error())
			}
		} else if err := BackupFile(backupRoot, "traktor", t.Path); err != nil {
			return zero, fmt.Errorf("%s%s", i18n.T("library.gf.backupFailed"), err.Error())
		}
		return musiclib.ApplyCuesNML(t.Path, updates)
	case "rekordbox":
		if err := BackupFile(backupRoot, "rekordbox", t.Path); err != nil {
			return zero, fmt.Errorf("%s%s", i18n.T("library.gf.backupFailed"), err.Error())
		}
		return musiclib.ApplyCuesRekordboxXML(t.Path, updates)
	case "virtualdj":
		// VDJ rewrites database.xml from memory on exit - a live write would be clobbered.
		if set, ok := sysactivity.New().RunningProcesses(); ok && sysactivity.RunningPrefix(set, "virtualdj") {
			return zero, fmt.Errorf("%s", i18n.T("library.gf.vdjRunning"))
		}
		if err := BackupFile(backupRoot, "virtualdj", t.Path); err != nil {
			return zero, fmt.Errorf("%s%s", i18n.T("library.gf.backupFailed"), err.Error())
		}
		return musiclib.ApplyCuesVirtualDJ(t.Path, updates)
	case "serato":
		// per-file temp+verify+rename with its own Serato-running refusal; no library backup needed
		return seratolib.ApplyCuesSerato(t.Path, updates)
	}
	return zero, fmt.Errorf("unknown write target %q", t.Key)
}
