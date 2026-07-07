// Package traktorcfg locates a Traktor install and safely mediates changes to its
// configuration - the binary "Traktor Settings.tsi" that holds Controller Manager
// mappings. Every write is preceded by a timestamped backup, and callers MUST check
// IsRunning first (Traktor overwrites Settings.tsi on exit, so editing it while Traktor
// is open loses the change). The actual mapping install/toggle (TSI surgery) builds on
// this; this layer is the detection + safety net.
package traktorcfg

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/musiclib"
)

// isTraktorExe matches the main Traktor application across versions - "Traktor.exe",
// "Traktor Pro 4.exe", "Traktor Pro 3.exe", etc. (the executable is named after the version,
// not a fixed "Traktor.exe"). Audio/controller drivers aren't "Traktor*.exe" processes.
func isTraktorExe(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(n, "traktor") && strings.HasSuffix(n, ".exe")
}

// settingsFile is the Controller-Manager-bearing settings file in each install dir.
const settingsFile = "Traktor Settings.tsi"

// Install is a discovered Traktor installation + its key config paths.
type Install struct {
	Version    string
	Dir        string
	Settings   string // "<Dir>/Traktor Settings.tsi" ("" if absent)
	Collection string
}

// Discover returns every Traktor install (newest first, per musiclib.DiscoverTraktor),
// with the Settings.tsi path resolved.
func Discover() ([]Install, error) {
	raw, err := musiclib.DiscoverTraktor()
	if err != nil {
		return nil, err
	}
	out := make([]Install, 0, len(raw))
	for _, in := range raw {
		s := filepath.Join(in.Dir, settingsFile)
		if _, err := os.Stat(s); err != nil {
			s = ""
		}
		out = append(out, Install{Version: in.Version, Dir: in.Dir, Settings: s, Collection: in.Collection})
	}
	return out, nil
}

// Newest returns the newest install with a Settings.tsi (ok=false if none has one).
// Discover yields newest-first, so the first hit is the newest - iterate front-to-back.
func Newest() (Install, bool, error) {
	all, err := Discover()
	if err != nil {
		return Install{}, false, err
	}
	in, ok := newestWithSettings(all)
	return in, ok, nil
}

// newestWithSettings returns the first install carrying a Settings.tsi from a newest-first
// slice. Pure (no filesystem) so the ordering invariant is unit-testable.
func newestWithSettings(all []Install) (Install, bool) {
	for i := range all {
		if all[i].Settings != "" {
			return all[i], true
		}
	}
	return Install{}, false
}

// ByVersion returns the install whose Version matches (ok=false if absent or it lacks a
// Settings.tsi). Used when the user pins a specific Traktor version instead of auto/newest.
func ByVersion(version string) (Install, bool, error) {
	all, err := Discover()
	if err != nil {
		return Install{}, false, err
	}
	for i := range all {
		if all[i].Version == version {
			return all[i], all[i].Settings != "", nil
		}
	}
	return Install{}, false, nil
}

// Backup copies path to a timestamped sibling "<path>.rmbak-YYYYMMDD-HHMMSS" and returns
// the backup path. Called before any settings mutation so a bad write is always recoverable.
func Backup(path string) (string, error) {
	bak := fmt.Sprintf("%s.rmbak-%s", path, time.Now().Format("20060102-150405"))
	if err := copyFile(path, bak); err != nil {
		return "", err
	}
	return bak, nil
}

// ListBackups returns existing rave-mate backups for path, newest first.
func ListBackups(path string) []string {
	matches, _ := filepath.Glob(path + ".rmbak-*")
	// Glob returns lexical order; the timestamp suffix sorts chronologically, so reverse.
	for i, j := 0, len(matches)-1; i < j; i, j = i+1, j-1 {
		matches[i], matches[j] = matches[j], matches[i]
	}
	return matches
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
