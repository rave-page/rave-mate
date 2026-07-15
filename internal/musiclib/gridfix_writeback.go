package musiclib

import (
	"bufio"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Beatgrid-fix write-back into a Traktor collection.nml: surgically rewrite ONLY the tempo/grid
// of matched ENTRYs (TEMPO BPM, the single TYPE-4 AutoGrid CUE_V2, optional LOCK stamp) and pass
// everything else - hotcues, loops, INFO, unmatched entries, playlists - through untouched.
// Ports fix_grids.process_entry semantics. Same streaming token approach + atomic temp+rename
// as MergeIntoCollectionFile. Caller backs the file up first.

// GridFixUpdate is one beatgrid correction, matched to a collection ENTRY by resolved LOCATION path.
type GridFixUpdate struct {
	Path    string  // resolved OS path of the track file
	BPM     float64 // corrected constant tempo
	StartMs float64 // grid marker position (ms)
	Lock    bool    // set Traktor's grid-lock flag on the ENTRY
}

// ApplyGridFixes rewrites collection.nml at path in place, applying each fix to the COLLECTION
// ENTRY whose LOCATION resolves to fix.Path. Unmatched fixes are ignored; unmatched entries and
// all other content pass through. Atomic temp+rename; back the file up BEFORE calling.
func ApplyGridFixes(path string, fixes []GridFixUpdate) (WritebackResult, error) {
	if path == "" || len(fixes) == 0 {
		return WritebackResult{}, nil
	}
	byPath := make(map[string]*GridFixUpdate, len(fixes))
	for i := range fixes {
		if fixes[i].Path != "" {
			byPath[fixes[i].Path] = &fixes[i]
		}
	}
	var res WritebackResult
	err := rewriteFileAtomic(path, "collection-*.nml.tmp", func(src io.Reader, dst io.Writer) error {
		r, e := applyGridFixesStream(src, byPath, dst)
		res = r
		return e
	})
	if err != nil {
		return WritebackResult{}, err
	}
	return res, nil
}

// rewriteNMLFile = rewriteFileAtomic with the collection.nml temp pattern.
func rewriteNMLFile(path string, fn func(src io.Reader, dst io.Writer) error) error {
	return rewriteFileAtomic(path, "collection-*.nml.tmp", fn)
}

// rewriteFileAtomic streams path through fn into a same-dir temp file (tmpPattern) and renames
// over the original only on a fully-clean rewrite (mirrors MergeIntoCollectionFile).
func rewriteFileAtomic(path, tmpPattern string, fn func(src io.Reader, dst io.Writer) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), tmpPattern)
	if err != nil {
		_ = f.Close()
		return err
	}
	tmpName := tmp.Name()
	bw := bufio.NewWriterSize(tmp, 1<<20)
	perr := fn(bufio.NewReaderSize(f, 1<<20), bw)
	if perr == nil {
		perr = bw.Flush()
	}
	if perr == nil {
		perr = tmp.Sync()
	}
	if cerr := tmp.Close(); perr == nil {
		perr = cerr
	}
	_ = f.Close() // close before rename (Windows refuses to replace an open file)
	if perr != nil {
		_ = os.Remove(tmpName)
		return perr
	}
	if err := replaceFile(tmpName, path); err != nil { // retries a transient reader (nmlsrc watcher / AV / sync)
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// applyGridFixesStream streams src→dst, buffering each COLLECTION ENTRY and rewriting matched
// ones (entryLocPath resolver, as mergeCollection). Everything else re-encodes verbatim.
func applyGridFixesStream(src io.Reader, byPath map[string]*GridFixUpdate, dst io.Writer) (WritebackResult, error) {
	var res WritebackResult
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	var buf []xml.Token // tokens of the COLLECTION ENTRY being buffered (nil = not inside one)
	inCollection := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, err
		}
		if buf == nil {
			if se, ok := tok.(xml.StartElement); ok {
				switch se.Name.Local {
				case "COLLECTION":
					inCollection = true
				case "ENTRY":
					if inCollection {
						buf = []xml.Token{xml.CopyToken(tok)}
						continue
					}
				}
			}
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "COLLECTION" {
				inCollection = false
			}
			if err := enc.EncodeToken(tok); err != nil {
				return res, err
			}
			continue
		}
		buf = append(buf, xml.CopyToken(tok))
		if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "ENTRY" {
			if fx := byPath[entryLocPath(buf)]; fx != nil {
				if err := emitGridFixedEntry(enc, buf, *fx); err != nil {
					return res, err
				}
				res.Updated++
			} else {
				for _, bt := range buf {
					if err := enc.EncodeToken(bt); err != nil {
						return res, err
					}
				}
			}
			buf = nil
		}
	}
	return res, enc.Flush()
}

// emitGridFixedEntry re-encodes a buffered ENTRY with only its grid rewritten: TEMPO BPM updated
// (created after INFO when absent - fix_grids.process_entry), ALL TYPE-4 CUE_V2 replaced by one
// AutoGrid cue at the first grid cue's position (appended when none existed), optional LOCK
// stamp. Non-grid cues and every other element pass through unchanged.
func emitGridFixedEntry(enc *xml.Encoder, buf []xml.Token, fx GridFixUpdate) error {
	hasTempo := false
	for _, tk := range buf {
		if se, ok := tk.(xml.StartElement); ok && se.Name.Local == "TEMPO" {
			hasTempo = true
			break
		}
	}
	gridDone := false
	skip := 0 // >0 while inside a dropped TYPE-4 CUE_V2 subtree
	for _, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			switch el.Name.Local {
			case "ENTRY":
				if fx.Lock {
					el.Attr = setAttr(el.Attr, "LOCK", "1")
					el.Attr = setAttr(el.Attr, "LOCK_MODIFICATION_TIME", time.Now().Format("2006-01-02T15:04:05"))
				}
			case "TEMPO":
				el.Attr = setAttr(el.Attr, "BPM", f6(fx.BPM))
			case "CUE_V2":
				if attrVal(el.Attr, "TYPE") == "4" {
					skip = 1
					if !gridDone {
						if err := emitGridCue(enc, fx); err != nil {
							return err
						}
						gridDone = true
					}
					continue
				}
			}
			if err := enc.EncodeToken(el); err != nil {
				return err
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			switch el.Name.Local {
			case "INFO":
				if err := enc.EncodeToken(el); err != nil {
					return err
				}
				if !hasTempo {
					if err := emitNewTempo(enc, fx.BPM); err != nil {
						return err
					}
					hasTempo = true
				}
				continue
			case "ENTRY":
				if !hasTempo {
					if err := emitNewTempo(enc, fx.BPM); err != nil {
						return err
					}
					hasTempo = true
				}
				if !gridDone {
					if err := emitGridCue(enc, fx); err != nil {
						return err
					}
					gridDone = true
				}
			}
			if err := enc.EncodeToken(el); err != nil {
				return err
			}
		default:
			if skip == 0 {
				if err := enc.EncodeToken(tk); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// emitNewTempo writes a fresh <TEMPO BPM=".." BPM_QUALITY="100.000000"> (fix_grids ~478-485).
func emitNewTempo(enc *xml.Encoder, bpm float64) error {
	return emitElem(enc, "TEMPO", [][2]string{{"BPM", f6(bpm)}, {"BPM_QUALITY", "100.000000"}})
}

// emitGridCue writes the single replacement AutoGrid CUE_V2 with its GRID child.
func emitGridCue(enc *xml.Encoder, fx GridFixUpdate) error {
	cue := startElem("CUE_V2", [][2]string{
		{"NAME", "AutoGrid"}, {"DISPL_ORDER", "0"}, {"TYPE", "4"},
		{"START", f6(fx.StartMs)}, {"LEN", "0.000000"}, {"REPEATS", "-1"}, {"HOTCUE", "-1"},
	})
	if err := enc.EncodeToken(cue); err != nil {
		return err
	}
	grid := startElem("GRID", [][2]string{{"BPM", f6(fx.BPM)}})
	if err := enc.EncodeToken(grid); err != nil {
		return err
	}
	if err := enc.EncodeToken(grid.End()); err != nil {
		return err
	}
	return enc.EncodeToken(cue.End())
}

// startElem builds a StartElement with ordered attrs.
func startElem(name string, attrs [][2]string) xml.StartElement {
	se := xml.StartElement{Name: xml.Name{Local: name}}
	for _, a := range attrs {
		se.Attr = append(se.Attr, xml.Attr{Name: xml.Name{Local: a[0]}, Value: a[1]})
	}
	return se
}

// setAttr returns a copy of attr with name set to value (appended if absent).
func setAttr(attr []xml.Attr, name, value string) []xml.Attr {
	out := make([]xml.Attr, len(attr))
	copy(out, attr)
	for i := range out {
		if out[i].Name.Local == name {
			out[i].Value = value
			return out
		}
	}
	return append(out, xml.Attr{Name: xml.Name{Local: name}, Value: value})
}

// attrVal returns the value of the named attr ("" if absent).
func attrVal(attr []xml.Attr, name string) string {
	for _, a := range attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// f6 formats a float with Traktor's 6-decimal convention.
func f6(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
