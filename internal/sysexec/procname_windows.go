//go:build windows

package sysexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Named gives a child a distinct image name in Task Manager / tasklist by launching it from a
// per-role hardlink of the exe (e.g. rave-mate-feature-player.exe). Hardlinks share the on-disk
// image + Authenticode signature - no copy, no extra disk, instant. The child still nests under
// the parent in the process tree (grouping). Best-effort: on any failure the real exe is used.
func Named(cmd *exec.Cmd, label string) {
	link := ensureProcLink(cmd.Path, label)
	if link == "" {
		return
	}
	cmd.Path = link
	if len(cmd.Args) > 0 {
		cmd.Args[0] = link
	}
}

// SetProcName is a no-op on Windows - the image name (set via Named's per-role hardlink) is what
// Task Manager shows; there is no supported API to rename a running process's image.
func SetProcName(string) {}

// procLinkDir is the writable cache dir holding the per-role exe hardlinks.
func procLinkDir() (string, error) {
	base, err := os.UserCacheDir() // %LocalAppData%
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "rave-mate", "proc")
	return dir, os.MkdirAll(dir, 0o755)
}

// ensureProcLink returns a path to a current hardlink of exe named for label, creating it on first
// use and refreshing it after an app update (a replaced exe breaks the old link's shared inode).
// "" on any failure → caller launches the real exe.
func ensureProcLink(exe, label string) string {
	dir, err := procLinkDir()
	if err != nil {
		return ""
	}
	return ensureProcLinkIn(dir, exe, label)
}

// ensureProcLinkIn is ensureProcLink against an explicit dir (testable core).
func ensureProcLinkIn(dir, exe, label string) string {
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	link := filepath.Join(dir, "rave-mate-"+sanitizeLabel(label)+".exe")
	if li, err := os.Stat(link); err == nil {
		if os.SameFile(exeInfo, li) {
			return link // fresh hardlink to the current exe
		}
		if err := os.Remove(link); err != nil {
			return "" // stale link still in use by an orphan child → don't run old bytes
		}
	}
	if err := os.Link(exe, link); err != nil {
		// lost a create race with a sibling spawn - reuse if it now matches the current exe
		if li, e := os.Stat(link); e == nil && os.SameFile(exeInfo, li) {
			return link
		}
		return ""
	}
	return link
}

// sanitizeLabel keeps a role label safe for a filename.
func sanitizeLabel(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?* `, r) {
			return '-'
		}
		return r
	}, s)
}
