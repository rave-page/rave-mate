//go:build !windows

package musiclib

import (
	"os"
	"path/filepath"
)

// pdbMountRoots returns likely removable-media mount points. macOS: /Volumes/*; Linux:
// /media/<user>/* and /run/media/<user>/* and /mnt/*.
func pdbMountRoots() []string {
	var bases []string
	bases = append(bases, "/Volumes")
	if u := os.Getenv("USER"); u != "" {
		bases = append(bases, filepath.Join("/media", u), filepath.Join("/run/media", u))
	}
	bases = append(bases, "/media", "/mnt")
	var out []string
	seen := map[string]bool{}
	for _, b := range bases {
		ents, err := os.ReadDir(b)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(b, e.Name())
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
