package webui

// Extraction of the embedded Zig window child. Same contract as mfenc.stagedChildExe (the encoder
// child): version-stamped filename from the embed's content hash, atomic write+rename, re-extract
// only on mismatch, prune superseded copies. Kept a deliberate copy rather than a shared helper
// because the two live in different packages with different cache subdirs, and a shared "extract an
// exe" abstraction would have to carry both roles' naming - not worth the coupling for 40 lines.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
)

var (
	shellStageOnce sync.Once
	shellStagePath string
	shellStageErr  error
)

// hasEmbeddedShell reports whether this build carries the embedded Zig window child.
func hasEmbeddedShell() bool { return len(embeddedShell) > 0 }

// stagedShellExe extracts the embedded child to the per-role cache dir and returns its path;
// ("", nil) when this build has no embed.
func stagedShellExe() (string, error) {
	if len(embeddedShell) == 0 {
		return "", nil
	}
	shellStageOnce.Do(func() {
		sum := sha256.Sum256(embeddedShell)
		stamp := hex.EncodeToString(sum[:6])
		base, err := os.UserCacheDir()
		if err != nil {
			shellStageErr = err
			return
		}
		dir := filepath.Join(base, "rave-mate", "proc")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			shellStageErr = err
			return
		}
		dst := filepath.Join(dir, "rave-shell-"+stamp+".exe")
		if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(embeddedShell)) {
			shellStagePath = dst // hash-stamped name + size match = this version is already staged
			return
		}
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, embeddedShell, 0o755); err != nil {
			shellStageErr = err
			return
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			// Lost a race with a sibling extract (two instances starting at once): reuse the
			// winner's file if it now matches.
			if fi, serr := os.Stat(dst); serr == nil && fi.Size() == int64(len(embeddedShell)) {
				shellStagePath = dst
				return
			}
			shellStageErr = err
			return
		}
		shellStagePath = dst
		if old, err := filepath.Glob(filepath.Join(dir, "rave-shell-*.exe")); err == nil {
			for _, f := range old { // prune superseded versions; in-use ones refuse removal, which is fine
				if f != dst {
					_ = os.Remove(f)
				}
			}
		}
	})
	return shellStagePath, shellStageErr
}
