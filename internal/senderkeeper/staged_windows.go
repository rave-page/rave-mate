//go:build windows

package senderkeeper

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"rave.page/mate/internal/appdir"
)

var (
	stagedOnce sync.Once
	stagedPath string
)

// exePath returns the keeper to launch: the embedded one extracted to the proc cache under a
// hash-stamped name (so an update never overwrites a running keeper), else the sidecar.
func exePath() string {
	stagedOnce.Do(func() { stagedPath = stageEmbedded(embeddedKeeper) })
	if stagedPath != "" {
		return stagedPath
	}
	return appdir.SidecarPath(ExeName)
}

// stageEmbedded extracts b to <cache>/rave-mate/proc/rave-mate-keeper-<hash>.exe ("" on any failure
// or an empty embed). Superseded versions are pruned best-effort (a running one refuses removal).
func stageEmbedded(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return stageInto(filepath.Join(base, "rave-mate", "proc"), b)
}

func stageInto(dir string, b []byte) string {
	if os.MkdirAll(dir, 0o755) != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	dst := filepath.Join(dir, "rave-mate-keeper-"+hex.EncodeToString(sum[:6])+".exe")
	current := func() bool {
		fi, err := os.Stat(dst)
		return err == nil && fi.Size() == int64(len(b))
	}
	if !current() {
		tmp := dst + ".tmp"
		if os.WriteFile(tmp, b, 0o755) != nil {
			return ""
		}
		if os.Rename(tmp, dst) != nil {
			_ = os.Remove(tmp) // lost a race with a sibling extract, or dst is in use
			if !current() {
				return ""
			}
		}
	}
	if old, err := filepath.Glob(filepath.Join(dir, "rave-mate-keeper-*.exe")); err == nil {
		for _, f := range old {
			if f != dst {
				_ = os.Remove(f) // in-use ones refuse removal: fine
			}
		}
	}
	return dst
}
