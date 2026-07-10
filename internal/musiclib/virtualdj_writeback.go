package musiclib

import (
	"encoding/xml"
	"io"
	"strconv"
)

// VirtualDJ database.xml write-back: beatgrid fix + managed-field merge. Streams the file
// token-by-token, buffering each <Song> subtree; Songs matched by FilePath are surgically
// rewritten, everything else (attrs, Comment, unknown elements, unmatched Songs) passes through
// verbatim. Same atomic temp+rename as the Traktor path (rewriteFileAtomic). Caller backs the
// file up first. VirtualDJ encodes tempo as seconds-per-beat (60/BPM) in Tags@Bpm / Scan@Bpm /
// beatgrid-Poi@Bpm; the grid anchor is a <Poi Type="beatgrid"> with Pos in seconds.

// ApplyGridFixesVirtualDJ rewrites database.xml at path in place, applying each fix to the Song
// whose FilePath equals fix.Path: Tags@Bpm + Scan@Bpm are set to 60/BPM, and all beatgrid Pois
// are replaced by one anchor at StartMs. Cue/loop Pois and every other attr/element are
// preserved. fix.Lock is ignored (VirtualDJ has no grid-lock flag). Unmatched fixes are ignored.
// Atomic temp+rename; back the file up BEFORE calling.
func ApplyGridFixesVirtualDJ(path string, fixes []GridFixUpdate) (WritebackResult, error) {
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
	err := rewriteFileAtomic(path, "database-*.xml.tmp", func(src io.Reader, dst io.Writer) error {
		return vdjRewriteStream(src, dst, func(enc *xml.Encoder, buf []xml.Token, filePath string) (bool, error) {
			fx := byPath[filePath]
			if fx == nil {
				return false, nil
			}
			if err := emitGridFixedSong(enc, buf, *fx); err != nil {
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

// MergeIntoVirtualDJFile rewrites database.xml at path in place, upserting updates by FilePath.
// For matched Songs the managed fields are taken from the update - Tags Genre/Label/Key/Bpm,
// Infos PlayCount, Scan Bpm (non-empty/non-zero only, so a merge without a value never wipes
// data) and ALL Pois regenerated from the update's beatgrid+cues (mirrors the Traktor
// TEMPO/CUE_V2 replacement); other attrs/elements are preserved. Comment and rating have no
// mapped field in this schema and are skipped. Tracks not in the database are appended as new
// Songs. Atomic temp+rename; back the file up BEFORE calling. No-op for empty updates.
func MergeIntoVirtualDJFile(path string, updates []Track) (WritebackResult, error) {
	if path == "" || len(updates) == 0 {
		return WritebackResult{}, nil
	}
	byPath := make(map[string]*Track, len(updates))
	for i := range updates {
		if updates[i].Path != "" {
			byPath[updates[i].Path] = &updates[i]
		}
	}
	matched := make(map[string]bool, len(byPath))
	var res WritebackResult
	err := rewriteFileAtomic(path, "database-*.xml.tmp", func(src io.Reader, dst io.Writer) error {
		matched = make(map[string]bool, len(byPath)) // reset if fn ever re-runs
		res = WritebackResult{}
		return vdjRewriteStream(src, dst, func(enc *xml.Encoder, buf []xml.Token, filePath string) (bool, error) {
			t := byPath[filePath]
			if t == nil {
				return false, nil
			}
			matched[filePath] = true
			if err := emitMergedVDJSong(enc, buf, *t); err != nil {
				return false, err
			}
			res.Updated++
			return true, nil
		}, func(enc *xml.Encoder) error {
			// Append new Songs before </VirtualDJ_Database> (updates order = deterministic).
			for i := range updates {
				p := updates[i].Path
				if p == "" || matched[p] {
					continue
				}
				matched[p] = true // dedupe repeated update paths
				if err := enc.Encode(trackToVDJSong(updates[i])); err != nil {
					return err
				}
				res.Added++
			}
			return nil
		})
	})
	if err != nil {
		return WritebackResult{}, err
	}
	return res, nil
}

// vdjRewriteStream streams src→dst, buffering each top-level <Song> subtree. rewrite is called
// with the buffered tokens + the Song's FilePath; returning false passes the Song through
// verbatim. beforeRootEnd (optional) runs just before </VirtualDJ_Database>.
func vdjRewriteStream(src io.Reader, dst io.Writer,
	rewrite func(enc *xml.Encoder, buf []xml.Token, filePath string) (bool, error),
	beforeRootEnd func(enc *xml.Encoder) error) error {
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	var buf []xml.Token // Song subtree being buffered (nil = not inside one)
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if buf == nil {
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Song" {
				buf = []xml.Token{xml.CopyToken(tok)}
				depth = 1
				continue
			}
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "VirtualDJ_Database" && beforeRootEnd != nil {
				if err := beforeRootEnd(enc); err != nil {
					return err
				}
			}
			if err := enc.EncodeToken(tok); err != nil {
				return err
			}
			continue
		}
		buf = append(buf, xml.CopyToken(tok))
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
		if depth > 0 {
			continue
		}
		// Song subtree complete.
		filePath := ""
		if se, ok := buf[0].(xml.StartElement); ok {
			filePath = attrVal(se.Attr, "FilePath")
		}
		done, err := rewrite(enc, buf, filePath)
		if err != nil {
			return err
		}
		if !done {
			for _, bt := range buf {
				if err := enc.EncodeToken(bt); err != nil {
					return err
				}
			}
		}
		buf = nil
	}
	return enc.Flush()
}

// emitGridFixedSong re-encodes a buffered Song with only its tempo/grid rewritten: Tags@Bpm +
// Scan@Bpm set to 60/BPM, all beatgrid Pois collapsed into one anchor at StartMs (appended
// before </Song> when none existed; a <Scan Bpm> is appended when the Song carried the tempo
// nowhere). Non-grid Pois and everything else pass through unchanged.
func emitGridFixedSong(enc *xml.Encoder, buf []xml.Token, fx GridFixUpdate) error {
	spb := ""
	if fx.BPM > 0 {
		spb = f6(60 / fx.BPM)
	}
	gridDone, bpmPlaced := false, false
	skip := 0 // >0 while inside a dropped beatgrid-Poi subtree
	for _, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			switch el.Name.Local {
			case "Tags", "Scan":
				if spb != "" {
					el.Attr = setAttr(el.Attr, "Bpm", spb)
					bpmPlaced = true
				}
			case "Poi":
				if attrVal(el.Attr, "Type") == "beatgrid" {
					skip = 1
					if !gridDone {
						if err := emitVDJGridPoi(enc, fx); err != nil {
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
			if el.Name.Local == "Song" {
				if !gridDone {
					if err := emitVDJGridPoi(enc, fx); err != nil {
						return err
					}
					gridDone = true
				}
				if spb != "" && !bpmPlaced {
					if err := emitElem(enc, "Scan", [][2]string{{"Bpm", spb}}); err != nil {
						return err
					}
					bpmPlaced = true
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

// emitVDJGridPoi writes the single replacement beatgrid anchor Poi (Pos seconds, Bpm 60/BPM).
func emitVDJGridPoi(enc *xml.Encoder, fx GridFixUpdate) error {
	attrs := [][2]string{{"Pos", f6(fx.StartMs / 1000)}, {"Type", "beatgrid"}}
	if fx.BPM > 0 {
		attrs = append(attrs, [2]string{"Bpm", f6(60 / fx.BPM)})
	}
	return emitElem(enc, "Poi", attrs)
}

// emitMergedVDJSong re-encodes a buffered Song, overriding managed Tags/Infos/Scan attrs from t
// and replacing ALL Pois with ones regenerated from t (beatgrid+cues). Missing managed elements
// are created before </Song>. Everything else passes through unchanged.
func emitMergedVDJSong(enc *xml.Encoder, buf []xml.Token, t Track) error {
	spb := ""
	if t.BPM > 0 {
		spb = f6(60 / t.BPM)
	}
	tagsSeen, infosSeen, scanSeen := false, false, false
	skip := 0 // >0 while inside a dropped Poi subtree
	for _, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			switch el.Name.Local {
			case "Tags":
				tagsSeen = true
				el.Attr = overrideVDJTagAttrs(el.Attr, t, spb)
			case "Infos":
				infosSeen = true
				if t.PlayCount > 0 {
					el.Attr = setAttr(el.Attr, "PlayCount", strconv.Itoa(t.PlayCount))
				}
			case "Scan":
				scanSeen = true
				if spb != "" {
					el.Attr = setAttr(el.Attr, "Bpm", spb)
				}
			case "Poi":
				skip = 1 // drop; regenerated before </Song>
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
				if !tagsSeen {
					if attrs := vdjTagAttrs(t, spb); len(attrs) > 0 {
						if err := emitElem(enc, "Tags", attrs); err != nil {
							return err
						}
					}
				}
				if !infosSeen && t.PlayCount > 0 {
					if err := emitElem(enc, "Infos", [][2]string{{"PlayCount", strconv.Itoa(t.PlayCount)}}); err != nil {
						return err
					}
				}
				if !scanSeen && spb != "" {
					if err := emitElem(enc, "Scan", [][2]string{{"Bpm", spb}}); err != nil {
						return err
					}
				}
				for _, p := range vdjPois(t) {
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

// vdjTagAttrs returns the managed Tags attrs t provides (ordered, non-empty only).
func vdjTagAttrs(t Track, spb string) [][2]string {
	var attrs [][2]string
	if t.Genre != "" {
		attrs = append(attrs, [2]string{"Genre", t.Genre})
	}
	if t.Label != "" {
		attrs = append(attrs, [2]string{"Label", t.Label})
	}
	if t.Key != "" {
		attrs = append(attrs, [2]string{"Key", t.Key})
	}
	if spb != "" {
		attrs = append(attrs, [2]string{"Bpm", spb})
	}
	return attrs
}

// overrideVDJTagAttrs replaces the managed Tags attrs with t's values (non-empty only).
func overrideVDJTagAttrs(attr []xml.Attr, t Track, spb string) []xml.Attr {
	out := attr
	for _, kv := range vdjTagAttrs(t, spb) {
		out = setAttr(out, kv[0], kv[1])
	}
	return out
}

// emitVDJPoi encodes one Poi element (only set attrs are written).
func emitVDJPoi(enc *xml.Encoder, p vdjPoi) error {
	attrs := [][2]string{{"Pos", strconv.FormatFloat(p.Pos, 'f', 6, 64)}, {"Type", p.Type}}
	if p.Bpm != "" {
		attrs = append(attrs, [2]string{"Bpm", p.Bpm})
	}
	if p.Num != 0 {
		attrs = append(attrs, [2]string{"Num", strconv.Itoa(p.Num)})
	}
	if p.Name != "" {
		attrs = append(attrs, [2]string{"Name", p.Name})
	}
	return emitElem(enc, "Poi", attrs)
}
