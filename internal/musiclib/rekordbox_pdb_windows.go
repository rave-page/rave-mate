//go:build windows

package musiclib

import "os"

// pdbMountRoots returns every existing drive root (C:\..Z:\) - USB devices mount as drive letters.
func pdbMountRoots() []string {
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + ":\\"
		if fi, err := os.Stat(root); err == nil && fi.IsDir() {
			out = append(out, root)
		}
	}
	return out
}
