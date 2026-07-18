package musiclib

import (
	"encoding/xml"
	"io"
	"strconv"
)

// Cue write-back into external DJ libraries: surgically replace ONLY the musical cues
// (hotcues / memory cues / loops) of matched tracks - beatgrids, tempo and every other
// field pass through untouched. Mirrors the gridfix writers (same streaming tokens +
// atomic temp+rename; caller backs the file up first). One writer per software:
//
//	Traktor   collection.nml  non-TYPE-4 CUE_V2 replaced; TYPE-4 grid cues + TEMPO kept
//	Rekordbox rekordbox.xml   POSITION_MARK replaced; TEMPO kept (File → Import Collection)
//	VirtualDJ database.xml    non-beatgrid Pois replaced; beatgrid Pois kept
//
// Serato stores cues in the audio files - see seratolib.ApplyCuesSerato. The Rekordbox
// master.db is deliberately NOT written (same reasoning as beatgrids: hot/memory cues live
// in djmdCue rows tied to ANLZ analysis data; a partial write would desync the library).

// CueUpdate is one track's full replacement cue set, matched by resolved file path.
// CueGrid entries are ignored (grids are the gridfix writers' job). BPM is only used
// where a target needs it to derive loop lengths in beats (VirtualDJ Poi@Size).
type CueUpdate struct {
	Path string
	BPM  float64
	Cues []CuePoint
	// GridAnchor (Traktor only): write the earliest hotcue as the TYPE-4 grid cue -
	// Traktor anchors the beatgrid on it and its pad still fires. Pre-existing grid
	// cues are replaced (one anchor, not two). Off = grid cues pass through untouched.
	GridAnchor bool
}

// cueUpdateIndex maps updates by path (empty paths dropped).
func cueUpdateIndex(updates []CueUpdate) map[string]*CueUpdate {
	byPath := make(map[string]*CueUpdate, len(updates))
	for i := range updates {
		if updates[i].Path != "" {
			byPath[updates[i].Path] = &updates[i]
		}
	}
	return byPath
}

// ── Traktor ──

// ApplyCuesNML rewrites collection.nml at path in place, replacing the non-grid CUE_V2
// elements of each ENTRY whose LOCATION resolves to an update's Path. TYPE-4 grid cues,
// TEMPO and everything else pass through. Unmatched updates are ignored. Atomic
// temp+rename; back the file up BEFORE calling.
func ApplyCuesNML(path string, updates []CueUpdate) (WritebackResult, error) {
	if path == "" || len(updates) == 0 {
		return WritebackResult{}, nil
	}
	byPath := cueUpdateIndex(updates)
	var res WritebackResult
	err := rewriteNMLFile(path, func(src io.Reader, dst io.Writer) error {
		r, e := applyCuesNMLStream(src, byPath, dst)
		res = r
		return e
	})
	if err != nil {
		return WritebackResult{}, err
	}
	return res, nil
}

// applyCuesNMLStream streams src→dst, buffering each COLLECTION ENTRY and rewriting
// matched ones. Mirrors applyGridFixesStream.
func applyCuesNMLStream(src io.Reader, byPath map[string]*CueUpdate, dst io.Writer) (WritebackResult, error) {
	var res WritebackResult
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	var buf []xml.Token // COLLECTION ENTRY being buffered (nil = not inside one)
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
			if up := byPath[entryLocPath(buf)]; up != nil {
				if err := emitCueUpdatedEntry(enc, buf, *up); err != nil {
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

// emitCueUpdatedEntry re-encodes a buffered ENTRY with only its musical cues rewritten:
// non-TYPE-4 CUE_V2 subtrees dropped, replacements emitted before </ENTRY>. Grid cues
// (TYPE 4), TEMPO and all other elements pass through unchanged - unless up.GridAnchor
// re-anchors the grid on the earliest hotcue, which drops the old TYPE-4 cues instead
// (the new anchor replaces them; BPM from the entry's TEMPO, else up.BPM).
func emitCueUpdatedEntry(enc *xml.Encoder, buf []xml.Token, up CueUpdate) error {
	anchor := -1
	var gridBPM float64
	if up.GridAnchor {
		if gridBPM = entryTempoBPM(buf); gridBPM <= 0 {
			gridBPM = up.BPM
		}
		if gridBPM > 0 {
			anchor = earliestHotcue(up.Cues)
		}
	}
	dropGrid := anchor >= 0 // old grid cues are replaced by the new anchor
	skip := 0               // >0 while inside a dropped CUE_V2 subtree
	for _, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			if el.Name.Local == "CUE_V2" && (dropGrid || attrVal(el.Attr, "TYPE") != "4") {
				skip = 1
				continue
			}
			if err := enc.EncodeToken(el); err != nil {
				return err
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if el.Name.Local == "ENTRY" {
				if err := emitNMLCues(enc, up.Cues, anchor, gridBPM); err != nil {
					return err
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

// entryTempoBPM reads the buffered ENTRY's TEMPO BPM (0 = absent/unparsable).
func entryTempoBPM(buf []xml.Token) float64 {
	for _, tk := range buf {
		if se, ok := tk.(xml.StartElement); ok && se.Name.Local == "TEMPO" {
			v, _ := strconv.ParseFloat(attrVal(se.Attr, "BPM"), 64)
			return v
		}
	}
	return 0
}

// earliestHotcue returns the index of the earliest padded hotcue (-1 = none).
func earliestHotcue(cues []CuePoint) int {
	best := -1
	for i, c := range cues {
		if c.Kind != CueHot || c.Hotcue < 0 {
			continue
		}
		if best < 0 || c.StartMs < cues[best].StartMs {
			best = i
		}
	}
	return best
}

// emitNMLCues writes CUE_V2 elements for the non-grid cues (Traktor's unnamed cues are
// "n.n."). cues[anchor] doubles as the beatgrid anchor: TYPE 4 with a <GRID BPM> child,
// keeping its pad. With anchor < 0, TYPE-4 hotcues (imported grid anchors) are skipped -
// their original element passes through upstream untouched.
func emitNMLCues(enc *xml.Encoder, cues []CuePoint, anchor int, gridBPM float64) error {
	for i, c := range cues {
		if c.Kind == CueGrid {
			continue
		}
		if anchor < 0 && c.Type == 4 {
			continue // preserved via passthrough - re-emitting would duplicate it
		}
		name := c.Name
		if name == "" {
			name = "n.n."
		}
		typ := traktorCueType(c.Kind)
		if i == anchor {
			typ = 4
		}
		se := startElem("CUE_V2", [][2]string{
			{"NAME", name}, {"DISPL_ORDER", "0"}, {"TYPE", strconv.Itoa(typ)},
			{"START", f6(c.StartMs)}, {"LEN", f6(c.LenMs)}, {"REPEATS", "-1"},
			{"HOTCUE", strconv.Itoa(c.Hotcue)},
		})
		if err := enc.EncodeToken(se); err != nil {
			return err
		}
		if i == anchor {
			g := startElem("GRID", [][2]string{{"BPM", f6(gridBPM)}})
			if err := enc.EncodeToken(g); err != nil {
				return err
			}
			if err := enc.EncodeToken(g.End()); err != nil {
				return err
			}
		}
		if err := enc.EncodeToken(se.End()); err != nil {
			return err
		}
	}
	return nil
}

// ── Rekordbox (exported collection XML) ──

// ApplyCuesRekordboxXML rewrites the rekordbox XML at path in place, replacing ALL
// POSITION_MARK children of each COLLECTION TRACK whose Location resolves to an update's
// Path (hotcue Num≥0, memory cue Num=-1, loops carry End). TEMPO grids and everything
// else pass through; Rekordbox imports the result via File → Import Collection. Atomic
// temp+rename; back the file up BEFORE calling.
func ApplyCuesRekordboxXML(path string, updates []CueUpdate) (WritebackResult, error) {
	if path == "" || len(updates) == 0 {
		return WritebackResult{}, nil
	}
	byPath := cueUpdateIndex(updates)
	var res WritebackResult
	err := rewriteFileAtomic(path, "rekordbox-*.xml.tmp", func(src io.Reader, dst io.Writer) error {
		r, e := applyCuesRekordboxStream(src, byPath, dst)
		res = r
		return e
	})
	if err != nil {
		return WritebackResult{}, err
	}
	return res, nil
}

// applyCuesRekordboxStream streams src→dst, buffering each COLLECTION TRACK and rewriting
// matched ones. Mirrors applyRekordboxGridFixesStream; PLAYLISTS TRACK refs are untouched.
func applyCuesRekordboxStream(src io.Reader, byPath map[string]*CueUpdate, dst io.Writer) (WritebackResult, error) {
	var res WritebackResult
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	var buf []xml.Token // COLLECTION TRACK being buffered (nil = not inside one)
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
				case "TRACK":
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
		if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "TRACK" {
			if up := byPath[rbTrackLocPath(buf)]; up != nil {
				if err := emitCueUpdatedRBTrack(enc, buf, *up); err != nil {
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

// emitCueUpdatedRBTrack re-encodes a buffered TRACK with all POSITION_MARK subtrees dropped
// and replacements (rbMarks conventions) emitted before </TRACK> - Rekordbox's element
// order (TEMPO* then POSITION_MARK*) holds because TEMPOs pass through in place.
func emitCueUpdatedRBTrack(enc *xml.Encoder, buf []xml.Token, up CueUpdate) error {
	skip := 0 // >0 while inside a dropped POSITION_MARK subtree
	for _, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			if el.Name.Local == "POSITION_MARK" {
				skip = 1
				continue
			}
			if err := enc.EncodeToken(el); err != nil {
				return err
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if el.Name.Local == "TRACK" {
				for _, m := range rbMarks(up.Cues) {
					attrs := [][2]string{
						{"Name", m.Name}, {"Type", strconv.Itoa(m.Type)}, {"Start", m.Start},
					}
					if m.End != "" {
						attrs = append(attrs, [2]string{"End", m.End})
					}
					attrs = append(attrs, [2]string{"Num", strconv.Itoa(m.Num)})
					if err := emitElem(enc, "POSITION_MARK", attrs); err != nil {
						return err
					}
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

// ── VirtualDJ ──

// ApplyCuesVirtualDJ rewrites database.xml at path in place, replacing the non-beatgrid
// Pois of each Song whose FilePath equals an update's Path: hotcues become <Poi Type="cue"
// Num="1..">, memory cues <Poi Type="remix">, loops <Poi Type="loop"> (+Size in beats when
// BPM is known). Beatgrid Pois and everything else pass through. Refuse-while-VDJ-runs is
// the caller's job (it rewrites database.xml from memory on exit). Atomic temp+rename;
// back the file up BEFORE calling.
func ApplyCuesVirtualDJ(path string, updates []CueUpdate) (WritebackResult, error) {
	if path == "" || len(updates) == 0 {
		return WritebackResult{}, nil
	}
	byPath := cueUpdateIndex(updates)
	var res WritebackResult
	err := rewriteFileAtomic(path, "database-*.xml.tmp", func(src io.Reader, dst io.Writer) error {
		return vdjRewriteStream(src, dst, func(enc *xml.Encoder, buf []xml.Token, filePath string) (bool, error) {
			up := byPath[filePath]
			if up == nil {
				return false, nil
			}
			if err := emitCueUpdatedSong(enc, buf, *up); err != nil {
				return false, err
			}
			res.Updated++
			return true, nil
		}, nil)
	})
	if err != nil {
		return WritebackResult{}, err
	}
	return res, nil
}

// emitCueUpdatedSong re-encodes a buffered Song with only its musical Pois rewritten:
// non-beatgrid Poi subtrees dropped, replacements emitted before </Song>. Beatgrid Pois
// and everything else pass through unchanged.
func emitCueUpdatedSong(enc *xml.Encoder, buf []xml.Token, up CueUpdate) error {
	skip := 0 // >0 while inside a dropped non-beatgrid Poi subtree
	for _, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			if el.Name.Local == "Poi" && attrVal(el.Attr, "Type") != "beatgrid" {
				skip = 1
				continue
			}
			if err := enc.EncodeToken(el); err != nil {
				return err
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if el.Name.Local == "Song" {
				for _, p := range vdjCuePois(up.Cues, up.BPM) {
					if err := emitVDJPoi(enc, p); err != nil {
						return err
					}
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
