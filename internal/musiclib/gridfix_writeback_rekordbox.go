package musiclib

import (
	"encoding/xml"
	"io"
	"strconv"
)

// Beatgrid-fix write-back into a rekordbox.xml (the File → Import/Export Collection bridge
// format): surgically rewrite ONLY the grid of matched TRACKs - all TEMPO children replaced by
// one constant-tempo anchor + AverageBpm attr updated - and pass everything else (POSITION_MARK
// hotcues/memory cues, unmatched tracks, PLAYLISTS) through untouched. Rekordbox itself imports
// the corrected grid per track via File → Import Collection. Mirrors gridfix_writeback.go
// (Traktor) so the UI routes by software; same streaming tokens + atomic temp+rename.
//
// The native master.db is deliberately NOT written: it has no beatgrid table - grids live in
// the binary ANLZ analysis files (.DAT PQTZ / .EXT PQT2) referenced by djmdContent, and writing
// djmdContent.BPM alone would desync displayed BPM from the actual grid. See UpdateGridFixes
// note in internal/rekordboxdb/write.go.

// ApplyGridFixesRekordboxXML rewrites the rekordbox XML at path in place, applying each fix to
// the COLLECTION TRACK whose Location resolves to fix.Path. Matched tracks get their TEMPO
// elements replaced by a single <TEMPO Inizio(sec) Bpm Metro="4/4" Battito="1"/> and their
// AverageBpm updated; GridFixUpdate.Lock is ignored (no grid-lock in rekordbox XML). Unmatched
// fixes are ignored; unmatched tracks and all other content pass through. Atomic temp+rename;
// back the file up BEFORE calling.
func ApplyGridFixesRekordboxXML(path string, fixes []GridFixUpdate) (WritebackResult, error) {
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
	err := rewriteFileAtomic(path, "rekordbox-*.xml.tmp", func(src io.Reader, dst io.Writer) error {
		r, e := applyRekordboxGridFixesStream(src, byPath, dst)
		res = r
		return e
	})
	if err != nil {
		return WritebackResult{}, err
	}
	return res, nil
}

// applyRekordboxGridFixesStream streams src→dst, buffering each COLLECTION TRACK and rewriting
// matched ones (Location resolved via locationToPath). Everything else re-encodes verbatim.
// TRACK refs under PLAYLISTS are outside COLLECTION and never touched.
func applyRekordboxGridFixesStream(src io.Reader, byPath map[string]*GridFixUpdate, dst io.Writer) (WritebackResult, error) {
	var res WritebackResult
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	var buf []xml.Token // tokens of the COLLECTION TRACK being buffered (nil = not inside one)
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
			if fx := byPath[rbTrackLocPath(buf)]; fx != nil {
				if err := emitGridFixedRBTrack(enc, buf, *fx); err != nil {
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

// rbTrackLocPath resolves a buffered TRACK's Location attr to an OS path ("" if absent).
func rbTrackLocPath(buf []xml.Token) string {
	if len(buf) == 0 {
		return ""
	}
	se, ok := buf[0].(xml.StartElement)
	if !ok {
		return ""
	}
	loc := attrVal(se.Attr, "Location")
	if loc == "" {
		return ""
	}
	return locationToPath(loc)
}

// emitGridFixedRBTrack re-encodes a buffered TRACK with only its grid rewritten: AverageBpm attr
// updated, ALL TEMPO children replaced by one anchor at the first TEMPO's position (inserted
// before the first POSITION_MARK - Rekordbox's element order - or before </TRACK> when a track
// had neither). POSITION_MARK and every other child pass through unchanged.
func emitGridFixedRBTrack(enc *xml.Encoder, buf []xml.Token, fx GridFixUpdate) error {
	gridDone := false
	skip := 0 // >0 while inside a dropped TEMPO subtree
	for _, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			switch el.Name.Local {
			case "TRACK":
				el.Attr = setAttr(el.Attr, "AverageBpm", bpmStr(fx.BPM))
			case "TEMPO":
				skip = 1
				if !gridDone {
					if err := emitRBTempo(enc, fx); err != nil {
						return err
					}
					gridDone = true
				}
				continue
			case "POSITION_MARK":
				if !gridDone {
					if err := emitRBTempo(enc, fx); err != nil {
						return err
					}
					gridDone = true
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
			if el.Name.Local == "TRACK" && !gridDone {
				if err := emitRBTempo(enc, fx); err != nil {
					return err
				}
				gridDone = true
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

// emitRBTempo writes the single replacement TEMPO anchor (attr conventions per export.go
// rbTempos: Inizio seconds 3dp, Bpm 2dp, Metro 4/4, Battito 1).
func emitRBTempo(enc *xml.Encoder, fx GridFixUpdate) error {
	return emitElem(enc, "TEMPO", [][2]string{
		{"Inizio", strconv.FormatFloat(fx.StartMs/1000, 'f', 3, 64)},
		{"Bpm", bpmStr(fx.BPM)},
		{"Metro", "4/4"},
		{"Battito", "1"},
	})
}
