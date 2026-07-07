package musiclib

import (
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ScanMissing partitions tracks into present (os.Stat succeeds) and missing.
// READ-ONLY: never modifies any file.
func ScanMissing(tracks []Track) (present, missing []Track) {
	for _, t := range tracks {
		if _, err := os.Stat(t.Path); err == nil {
			present = append(present, t)
		} else {
			missing = append(missing, t)
		}
	}
	return
}

// Candidate is a relocation match for a missing track.
type Candidate struct {
	Track      Track
	NewPath    string
	Confidence float64 // 0..1: 1.0=unique, 0.9=size-match, 0.5=ambiguous
}

// BuildIndex walks searchRoots once and maps lowercase-basename → []fullpath.
// READ-ONLY: never modifies any file. Unreadable dirs are skipped silently.
func BuildIndex(searchRoots []string) (map[string][]string, error) {
	idx := make(map[string][]string)
	for _, root := range searchRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// skip unreadable dirs/files
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				key := strings.ToLower(filepath.Base(path))
				idx[key] = append(idx[key], path)
			}
			return nil
		})
		if err != nil {
			// WalkDir root itself unreadable - skip
			continue
		}
	}
	return idx, nil
}

// Relocate finds candidates for each missing track in the index.
// Confidence: 1.0 unique basename; 0.9 basename + size within 1 KB; 0.5 ambiguous.
// Tracks with no match are omitted from the result.
func Relocate(missing []Track, index map[string][]string) []Candidate {
	const sizeTolerance = 1024 // bytes
	var out []Candidate
	for _, t := range missing {
		key := strings.ToLower(filepath.Base(t.Path))
		matches := index[key]
		if len(matches) == 0 {
			continue
		}
		if len(matches) == 1 {
			out = append(out, Candidate{Track: t, NewPath: matches[0], Confidence: 1.0})
			continue
		}
		// Multiple matches - prefer size match.
		want := int64(t.FileSizeKB) * 1024
		best := ""
		bestDiff := int64(math.MaxInt64)
		for _, p := range matches {
			fi, err := os.Stat(p)
			if err != nil {
				continue
			}
			diff := fi.Size() - want
			if diff < 0 {
				diff = -diff
			}
			if diff < bestDiff {
				bestDiff = diff
				best = p
			}
		}
		if best == "" {
			// can't stat any - take first, low confidence
			out = append(out, Candidate{Track: t, NewPath: matches[0], Confidence: 0.5})
			continue
		}
		conf := 0.5
		if bestDiff <= sizeTolerance {
			conf = 0.9
		}
		out = append(out, Candidate{Track: t, NewPath: best, Confidence: conf})
	}
	return out
}

// FixPlan holds the approved relocations to apply.
type FixPlan struct {
	Fixes []Candidate
}

// WriteFixedCollection streams src (an original collection.nml) to dst, rewriting
// LOCATION elements for matched tracks. dst is a caller-owned writer (a NEW file).
// The original src is never modified. Returns the number of LOCATION elements rewritten.
//
// Path split (reverse of resolveLocation):
//   - volume: drive letter + colon on Windows (e.g. "C:"), or "" on Unix
//   - dir:    "/:seg1/:seg2/:" Traktor format
//   - file:   basename
func WriteFixedCollection(src io.Reader, plan FixPlan, dst io.Writer) (fixed int, err error) {
	// Build lookup: reconstructed old-path → new-path (lower-case keys for safety).
	remap := make(map[string]string) // oldPath (as parsed) → newPath
	for _, c := range plan.Fixes {
		remap[c.Track.Path] = c.NewPath
	}

	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)

	// Accumulate the full token stream but rewrite LOCATION StartElements on the fly.
	for {
		tok, terr := dec.Token()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return fixed, terr
		}

		se, ok := tok.(xml.StartElement)
		if ok && se.Name.Local == "LOCATION" {
			// Reconstruct old path from attrs.
			var volume, dir, file string
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "VOLUME":
					volume = a.Value
				case "DIR":
					dir = a.Value
				case "FILE":
					file = a.Value
				}
			}
			oldPath := resolveLocation(volume, dir, file)
			if newPath, found := remap[oldPath]; found {
				// Replace the attrs with split new path.
				newVol, newDir, newFile := splitPath(newPath)
				newAttrs := make([]xml.Attr, 0, len(se.Attr))
				for _, a := range se.Attr {
					switch a.Name.Local {
					case "VOLUME":
						a.Value = newVol
					case "DIR":
						a.Value = newDir
					case "FILE":
						a.Value = newFile
					}
					newAttrs = append(newAttrs, a)
				}
				se.Attr = newAttrs
				fixed++
			}
			tok = se
		}

		if werr := enc.EncodeToken(tok); werr != nil {
			return fixed, werr
		}
	}
	return fixed, enc.Flush()
}

// WriteFixReport writes a CSV (oldPath,newPath,confidence) to dst - useful as a
// dry-run or companion to WriteFixedCollection.
func WriteFixReport(plan FixPlan, dst io.Writer) error {
	w := csv.NewWriter(dst)
	if err := w.Write([]string{"oldPath", "newPath", "confidence"}); err != nil {
		return err
	}
	for _, c := range plan.Fixes {
		if err := w.Write([]string{c.Track.Path, c.NewPath, fmt.Sprintf("%.2f", c.Confidence)}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// splitPath is the inverse of resolveLocation: splits an OS path into the three
// Traktor LOCATION components (VOLUME, DIR "/:seg/:" form, FILE).
func splitPath(p string) (volume, dir, file string) {
	file = filepath.Base(p)
	parent := filepath.Dir(p)

	if runtime.GOOS == "windows" {
		// filepath.VolumeName returns "C:" etc.
		volume = filepath.VolumeName(parent)
		parent = strings.TrimPrefix(parent, volume)
	}

	// Normalise to forward slashes for splitting, then rebuild "/:seg/:" form.
	parent = filepath.ToSlash(parent)
	// Trim leading/trailing slashes.
	parent = strings.Trim(parent, "/")
	if parent == "" {
		dir = "/:"
		return
	}
	segs := strings.Split(parent, "/")
	var sb strings.Builder
	for _, s := range segs {
		sb.WriteString("/:")
		sb.WriteString(s)
	}
	sb.WriteString("/:")
	dir = sb.String()
	return
}
