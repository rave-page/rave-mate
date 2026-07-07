package musiclib

import (
	"bufio"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Collection pruning: drop tracks (and their playlist references) whose files are gone from the
// SOURCE Traktor collection.nml, so a re-import doesn't re-add what the user cleaned up. Streams the
// (hundreds-of-MB) file token-by-token; the original is replaced atomically only after a clean
// rewrite. Caller is responsible for backing the file up first (BackupCollection).

// PruneResult reports what a collection prune removed.
type PruneResult struct {
	TracksRemoved int `json:"tracksRemoved"` // COLLECTION ENTRY elements dropped (file gone)
	RefsRemoved   int `json:"refsRemoved"`   // PLAYLIST PRIMARYKEY references dropped
}

// removalCounts (pass 1) holds how many entries each container loses, so pass 2 can correct the
// ENTRIES attribute - which Traktor writes ahead of the entries it counts.
type removalCounts struct {
	collection  int
	perPlaylist []int // indexed by PLAYLIST occurrence order in the document
}

// PruneCollectionFile rewrites collection.nml at path in place, dropping every COLLECTION track and
// PLAYLIST reference whose resolved OS path is in removed, and correcting the ENTRIES counts. Writes
// to a temp file in the same dir and renames over the original only on a fully-clean rewrite (so a
// failure leaves the file untouched). Back the file up BEFORE calling. No-op for an empty removed set.
func PruneCollectionFile(path string, removed map[string]bool) (PruneResult, error) {
	if path == "" || len(removed) == 0 {
		return PruneResult{}, nil
	}
	// Pass 1: count removals per container.
	f1, err := os.Open(path)
	if err != nil {
		return PruneResult{}, err
	}
	counts, cerr := countRemovals(bufio.NewReaderSize(f1, 1<<20), removed)
	_ = f1.Close()
	if cerr != nil {
		return PruneResult{}, cerr
	}
	// Pass 2: rewrite to a temp file with entries dropped + counts fixed.
	f2, err := os.Open(path)
	if err != nil {
		return PruneResult{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "collection-*.nml.tmp")
	if err != nil {
		_ = f2.Close()
		return PruneResult{}, err
	}
	tmpName := tmp.Name()
	bw := bufio.NewWriterSize(tmp, 1<<20)
	res, perr := pruneCollection(bufio.NewReaderSize(f2, 1<<20), removed, counts, bw)
	if perr == nil {
		perr = bw.Flush()
	}
	if perr == nil {
		perr = tmp.Sync()
	}
	if cerr := tmp.Close(); perr == nil {
		perr = cerr
	}
	// Close the source handle BEFORE renaming over it - Windows refuses to replace an open file.
	_ = f2.Close()
	if perr != nil {
		_ = os.Remove(tmpName)
		return PruneResult{}, perr
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return PruneResult{}, err
	}
	return res, nil
}

// countRemovals streams src once, counting the COLLECTION tracks + per-PLAYLIST references that
// resolve to a removed path.
func countRemovals(src io.Reader, removed map[string]bool) (removalCounts, error) {
	var rc removalCounts
	dec := xml.NewDecoder(src)
	plIdx := -1
	var inEntry, skip, isRef bool
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return rc, nil
		}
		if err != nil {
			return rc, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "PLAYLIST":
				plIdx++
				rc.perPlaylist = append(rc.perPlaylist, 0)
			case "ENTRY":
				inEntry, skip, isRef = true, false, false
			case "LOCATION":
				if inEntry {
					if p := locPath(t.Attr); p != "" && removed[p] {
						skip = true
					}
				}
			case "PRIMARYKEY":
				if inEntry {
					isRef = true
					if p := keyPath(t.Attr); p != "" && removed[p] {
						skip = true
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "ENTRY" && inEntry {
				if skip {
					if isRef && plIdx >= 0 && plIdx < len(rc.perPlaylist) {
						rc.perPlaylist[plIdx]++
					} else if !isRef {
						rc.collection++
					}
				}
				inEntry = false
			}
		}
	}
}

// pruneCollection (pass 2) streams src→dst, dropping each ENTRY whose track LOCATION or playlist
// PRIMARYKEY resolves to a removed path, and rewriting COLLECTION/PLAYLIST ENTRIES counts from the
// pass-1 totals. Non-ENTRY content is copied verbatim (token re-encode, as WriteFixedCollection).
func pruneCollection(src io.Reader, removed map[string]bool, counts removalCounts, dst io.Writer) (PruneResult, error) {
	var res PruneResult
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	plIdx := -1
	var buf []xml.Token // tokens of the ENTRY being buffered (nil = not inside one)
	var skip, isRef bool
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, err
		}
		// Container starts (not inside an ENTRY): fix the ENTRIES count, then emit.
		if se, ok := tok.(xml.StartElement); ok && buf == nil {
			switch se.Name.Local {
			case "COLLECTION":
				se.Attr = adjustEntries(se.Attr, counts.collection)
				tok = se
			case "PLAYLIST":
				plIdx++
				rm := 0
				if plIdx < len(counts.perPlaylist) {
					rm = counts.perPlaylist[plIdx]
				}
				se.Attr = adjustEntries(se.Attr, rm)
				tok = se
			case "ENTRY":
				buf = []xml.Token{xml.CopyToken(tok)}
				skip, isRef = false, false
				continue
			}
		}
		if buf != nil {
			if se, ok := tok.(xml.StartElement); ok {
				switch se.Name.Local {
				case "LOCATION":
					if p := locPath(se.Attr); p != "" && removed[p] {
						skip = true
					}
				case "PRIMARYKEY":
					isRef = true
					if p := keyPath(se.Attr); p != "" && removed[p] {
						skip = true
					}
				}
			}
			buf = append(buf, xml.CopyToken(tok))
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "ENTRY" {
				if skip {
					if isRef {
						res.RefsRemoved++
					} else {
						res.TracksRemoved++
					}
				} else {
					for _, bt := range buf {
						if e := enc.EncodeToken(bt); e != nil {
							return res, e
						}
					}
				}
				buf = nil
			}
			continue
		}
		if e := enc.EncodeToken(tok); e != nil {
			return res, e
		}
	}
	return res, enc.Flush()
}

// locPath resolves a LOCATION element's attrs to an OS path ("" if not a file location).
func locPath(attr []xml.Attr) string {
	var vol, dir, file string
	for _, a := range attr {
		switch a.Name.Local {
		case "VOLUME":
			vol = a.Value
		case "DIR":
			dir = a.Value
		case "FILE":
			file = a.Value
		}
	}
	if file == "" {
		return ""
	}
	return resolveLocation(vol, dir, file)
}

// keyPath resolves a PRIMARYKEY element's KEY attr to an OS path ("" if absent).
func keyPath(attr []xml.Attr) string {
	for _, a := range attr {
		if a.Name.Local == "KEY" {
			return resolveKey(a.Value)
		}
	}
	return ""
}

// adjustEntries returns attr with ENTRIES decremented by removed (clamped ≥0). Unchanged if
// removed==0 or the attr is absent/unparseable.
func adjustEntries(attr []xml.Attr, removed int) []xml.Attr {
	if removed <= 0 {
		return attr
	}
	out := make([]xml.Attr, len(attr))
	copy(out, attr)
	for i := range out {
		if out[i].Name.Local == "ENTRIES" {
			if n, err := strconv.Atoi(strings.TrimSpace(out[i].Value)); err == nil {
				if n -= removed; n < 0 {
					n = 0
				}
				out[i].Value = strconv.Itoa(n)
			}
			break
		}
	}
	return out
}
