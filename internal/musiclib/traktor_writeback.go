package musiclib

import (
	"bufio"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// Live write-back into a Traktor collection.nml: upsert the synced tracks in place - rewrite the
// managed INFO attrs + TEMPO + CUE_V2 of existing ENTRYs (matched by file LOCATION) and append
// new ENTRYs for tracks not yet in the collection - leaving every other ENTRY (and unmanaged
// fields like MUSICAL_KEY, LOUDNESS, STEMS) untouched. Streams the (hundreds-of-MB) file
// token-by-token; the original is replaced atomically only after a clean rewrite. Caller backs
// the file up first (mirrors PruneCollectionFile).

// WritebackResult reports what a collection write-back changed.
type WritebackResult struct {
	Updated int `json:"updated"` // existing ENTRYs whose managed fields were rewritten
	Added   int `json:"added"`   // new ENTRYs appended to COLLECTION
}

// MergeIntoCollectionFile rewrites collection.nml at path in place, upserting updates by resolved
// file path. Managed fields (genre/label/comment/key/rating/playcount, TEMPO, CUE_V2) are taken
// from each update; the rest of every ENTRY is preserved. New tracks are appended to COLLECTION
// with the ENTRIES count corrected. Writes to a temp file in the same dir and renames over the
// original only on a fully-clean rewrite. Back the file up BEFORE calling. No-op for empty updates.
func MergeIntoCollectionFile(path string, updates []Track) (WritebackResult, error) {
	if path == "" || len(updates) == 0 {
		return WritebackResult{}, nil
	}
	byPath := make(map[string]*Track, len(updates))
	for i := range updates {
		if updates[i].Path != "" {
			byPath[updates[i].Path] = &updates[i]
		}
	}

	// Pass 1: which updates already exist (matched by COLLECTION LOCATION) → how many are new.
	f1, err := os.Open(path)
	if err != nil {
		return WritebackResult{}, err
	}
	matched, cerr := matchedCollectionPaths(bufio.NewReaderSize(f1, 1<<20), byPath)
	_ = f1.Close()
	if cerr != nil {
		return WritebackResult{}, cerr
	}
	newCount := 0
	for p := range byPath {
		if !matched[p] {
			newCount++
		}
	}

	// Pass 2: rewrite to a temp file: rewrite matched ENTRYs, append new ones, fix ENTRIES.
	f2, err := os.Open(path)
	if err != nil {
		return WritebackResult{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "collection-*.nml.tmp")
	if err != nil {
		_ = f2.Close()
		return WritebackResult{}, err
	}
	tmpName := tmp.Name()
	bw := bufio.NewWriterSize(tmp, 1<<20)
	res, perr := mergeCollection(bufio.NewReaderSize(f2, 1<<20), byPath, matched, newCount, bw)
	if perr == nil {
		perr = bw.Flush()
	}
	if perr == nil {
		perr = tmp.Sync()
	}
	if cerr := tmp.Close(); perr == nil {
		perr = cerr
	}
	_ = f2.Close() // close before rename (Windows refuses to replace an open file)
	if perr != nil {
		_ = os.Remove(tmpName)
		return WritebackResult{}, perr
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return WritebackResult{}, err
	}
	return res, nil
}

// matchedCollectionPaths streams src once, returning the set of update paths present as COLLECTION
// tracks (ENTRY/LOCATION). PLAYLIST PRIMARYKEY refs are ignored (they aren't collection tracks).
func matchedCollectionPaths(src io.Reader, byPath map[string]*Track) (map[string]bool, error) {
	matched := make(map[string]bool)
	dec := xml.NewDecoder(src)
	var inEntry, isRef bool
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return matched, nil
		}
		if err != nil {
			return matched, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "ENTRY":
				inEntry, isRef = true, false
			case "PRIMARYKEY":
				isRef = true
			case "LOCATION":
				if inEntry && !isRef {
					if p := locPath(t.Attr); p != "" && byPath[p] != nil {
						matched[p] = true
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "ENTRY" {
				inEntry = false
			}
		}
	}
}

// mergeCollection (pass 2) streams src→dst: passes everything through verbatim except (a) the
// COLLECTION ENTRIES count is bumped by newCount, (b) each COLLECTION ENTRY matching an update is
// surgically rewritten, and (c) new ENTRYs are appended just before </COLLECTION>.
func mergeCollection(src io.Reader, byPath map[string]*Track, matched map[string]bool, newCount int, dst io.Writer) (WritebackResult, error) {
	var res WritebackResult
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	enc.Indent("", "  ")

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

		// Outside a buffered ENTRY: handle container starts/ends.
		if buf == nil {
			if se, ok := tok.(xml.StartElement); ok {
				switch se.Name.Local {
				case "COLLECTION":
					inCollection = true
					se.Attr = addEntries(se.Attr, newCount)
					if err := enc.EncodeToken(se); err != nil {
						return res, err
					}
					continue
				case "ENTRY":
					if inCollection {
						buf = []xml.Token{xml.CopyToken(tok)}
						continue
					}
				}
			}
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "COLLECTION" {
				// Append new ENTRYs before closing COLLECTION.
				for p, t := range byPath {
					if matched[p] {
						continue
					}
					if err := enc.Encode(buildExportEntry(*t)); err != nil {
						return res, err
					}
					res.Added++
				}
				inCollection = false
				if err := enc.EncodeToken(tok); err != nil {
					return res, err
				}
				continue
			}
			if err := enc.EncodeToken(tok); err != nil {
				return res, err
			}
			continue
		}

		// Buffering a COLLECTION ENTRY.
		buf = append(buf, xml.CopyToken(tok))
		if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "ENTRY" {
			p := entryLocPath(buf)
			if t := byPath[p]; t != nil && matched[p] {
				if err := emitMergedEntry(enc, buf, *t); err != nil {
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

// entryLocPath returns the resolved OS path of a buffered ENTRY's LOCATION ("" if none).
func entryLocPath(buf []xml.Token) string {
	for _, tk := range buf {
		if se, ok := tk.(xml.StartElement); ok && se.Name.Local == "LOCATION" {
			return locPath(se.Attr)
		}
	}
	return ""
}

// emitMergedEntry re-encodes a buffered ENTRY, overriding INFO managed attrs from t and replacing
// its TEMPO + CUE_V2 nodes with ones regenerated from t (cues/beatgrid). All other elements
// (LOCATION, ALBUM, MUSICAL_KEY, LOUDNESS, STEMS, …) pass through unchanged.
func emitMergedEntry(enc *xml.Encoder, buf []xml.Token, t Track) error {
	skipDepth := 0 // >0 while inside a TEMPO/CUE_V2 subtree we're dropping
	for _, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if skipDepth > 0 {
				skipDepth++
				continue
			}
			switch el.Name.Local {
			case "TEMPO", "CUE_V2":
				skipDepth = 1 // drop old grid/cues; regenerated below
				continue
			case "INFO":
				el.Attr = overrideInfoAttrs(el.Attr, t)
			}
			if err := enc.EncodeToken(el); err != nil {
				return err
			}
		case xml.EndElement:
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if el.Name.Local == "ENTRY" {
				if err := emitTempoAndCues(enc, t); err != nil {
					return err
				}
			}
			if err := enc.EncodeToken(el); err != nil {
				return err
			}
		default:
			if skipDepth == 0 {
				if err := enc.EncodeToken(tk); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// emitTempoAndCues writes a fresh TEMPO (track BPM) + CUE_V2 set (beatgrid + cues) for t.
func emitTempoAndCues(enc *xml.Encoder, t Track) error {
	if t.BPM > 0 {
		if err := emitElem(enc, "TEMPO", [][2]string{{"BPM", strconv.FormatFloat(t.BPM, 'f', 6, 64)}}); err != nil {
			return err
		}
	}
	for _, c := range nmlCues(t) {
		if err := emitElem(enc, "CUE_V2", [][2]string{
			{"NAME", c.Name}, {"TYPE", strconv.Itoa(c.Type)},
			{"START", c.Start}, {"LEN", c.Len}, {"HOTCUE", strconv.Itoa(c.Hotcue)},
		}); err != nil {
			return err
		}
	}
	return nil
}

// emitElem encodes a self-contained element with the given ordered attrs.
func emitElem(enc *xml.Encoder, name string, attrs [][2]string) error {
	se := xml.StartElement{Name: xml.Name{Local: name}}
	for _, a := range attrs {
		se.Attr = append(se.Attr, xml.Attr{Name: xml.Name{Local: a[0]}, Value: a[1]})
	}
	if err := enc.EncodeToken(se); err != nil {
		return err
	}
	return enc.EncodeToken(se.End())
}

// overrideInfoAttrs replaces the managed INFO attrs with t's values (only non-empty/non-zero, so
// a merge that produced no value never wipes existing data). Unmanaged attrs are untouched.
func overrideInfoAttrs(attr []xml.Attr, t Track) []xml.Attr {
	set := map[string]string{}
	if t.Genre != "" {
		set["GENRE"] = t.Genre
	}
	if t.Label != "" {
		set["LABEL"] = t.Label
	}
	if t.Comment != "" {
		set["COMMENT"] = t.Comment
	}
	if t.Key != "" {
		set["KEY"] = t.Key
	}
	if t.Rating > 0 {
		set["RANKING"] = strconv.Itoa(t.Rating)
	}
	if t.PlayCount > 0 {
		set["PLAYCOUNT"] = strconv.Itoa(t.PlayCount)
	}
	out := make([]xml.Attr, len(attr))
	copy(out, attr)
	for k, v := range set {
		found := false
		for i := range out {
			if out[i].Name.Local == k {
				out[i].Value, found = v, true
				break
			}
		}
		if !found {
			out = append(out, xml.Attr{Name: xml.Name{Local: k}, Value: v})
		}
	}
	return out
}

// buildExportEntry builds a fresh nmlExportEntry for a new track (reuses the ExportTraktorNML path).
func buildExportEntry(t Track) nmlExportEntry {
	e := nmlExportEntry{Title: t.Title, Artist: t.Artist, Location: pathToLocation(t.Path)}
	if t.Album != "" {
		e.Album = &nmlAlbum{Title: t.Album}
	}
	e.Info = nmlInfo{
		Bitrate: t.BitrateBps, Genre: t.Genre, Label: t.Label, Comment: t.Comment,
		Key: t.Key, Playtime: int(t.DurationSec), ImportDate: t.ImportDate,
		ReleaseDate: t.ReleaseDate, PlayCount: t.PlayCount, Ranking: t.Rating,
		FileSize: t.FileSizeKB,
	}
	if t.DurationSec > 0 {
		e.Info.PlaytimeF = strconv.FormatFloat(t.DurationSec, 'f', 6, 64)
	}
	if t.BPM > 0 {
		e.Tempo = &nmlTempo{BPM: strconv.FormatFloat(t.BPM, 'f', 6, 64)}
	}
	e.Cues = nmlCues(t)
	return e
}

// addEntries returns attr with ENTRIES incremented by add (no-op if add<=0 or attr absent).
func addEntries(attr []xml.Attr, add int) []xml.Attr {
	if add <= 0 {
		return attr
	}
	out := make([]xml.Attr, len(attr))
	copy(out, attr)
	for i := range out {
		if out[i].Name.Local == "ENTRIES" {
			if n, err := strconv.Atoi(out[i].Value); err == nil {
				out[i].Value = strconv.Itoa(n + add)
			}
			break
		}
	}
	return out
}
