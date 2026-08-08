package musiclib

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
)

// Collection repair: undo the damage classes older rave-mate writers left in a Traktor
// collection.nml (all verified against a real library):
//
//	pads out of track-time order   pattern applies once numbered slots per-drop
//	stacked duplicate cues         the same cue written onto two pad slots
//	padded TYPE-4 grid cues        the old single-cue anchor form Traktor never keeps
//	phase-shifted grid markers     gridfix moved markers onto an uncalibrated detector
//	                               bias (12-40ms off the old lattice) - restored from a
//	                               reference (pre-damage backup) collection
//
// Token-surgical: only HOTCUE/START attrs change and duplicate subtrees drop; every
// other attr (COLOR, …) and element passes through byte-faithfully. Atomic temp+rename;
// caller backs the file up first.

// RefGrid is a reference entry's single grid marker (from a pre-damage backup).
type RefGrid struct {
	StartMs float64
	BPM     float64
}

// RepairOptions control RepairCollectionFile.
type RepairOptions struct {
	Ref    map[string]RefGrid // reference grids by resolved path (nil = no grid restore)
	DryRun bool               // analyze + report only, write nothing
}

// grid-restore window: below MinMs = noise (leave), above MaxMs = probably a deliberate
// manual regrid (leave). The observed gridfix damage sits at 12-40ms.
const (
	gridRestoreMinMs = 3.0
	gridRestoreMaxMs = 60.0
	dupeEpsMs        = 5.0
)

// RepairReport says what a repair pass found/changed.
type RepairReport struct {
	Entries       int      // COLLECTION entries scanned
	Changed       int      // entries rewritten
	PadsReordered int      // entries whose pad slots were renumbered into time order
	DupesRemoved  int      // stacked duplicate cues dropped
	PadsSplit     int      // padded TYPE-4 anchors split into grid cue + plain pad cue
	GridsRestored int      // grid markers moved back onto the reference lattice
	Details       []string // per-entry summaries (capped)
}

const repairDetailCap = 200

func (r *RepairReport) detail(s string) {
	if len(r.Details) < repairDetailCap {
		r.Details = append(r.Details, s)
	}
}

// RepairCollectionFile repairs collection.nml at path in place (atomic temp+rename;
// back the file up BEFORE calling). DryRun analyzes without writing.
func RepairCollectionFile(path string, opts RepairOptions) (RepairReport, error) {
	var rep RepairReport
	if opts.DryRun {
		f, err := os.Open(path)
		if err != nil {
			return rep, err
		}
		defer func() { _ = f.Close() }()
		err = repairStream(bufio.NewReaderSize(f, 1<<20), io.Discard, opts, &rep)
		return rep, err
	}
	err := rewriteNMLFile(path, func(src io.Reader, dst io.Writer) error {
		return repairStream(src, dst, opts, &rep)
	})
	if err != nil {
		return RepairReport{}, err
	}
	return rep, nil
}

// ParseCollectionGrids streams a (reference) collection.nml into a path → single-grid
// map. Entries with 0 or 2+ TYPE-4 markers are omitted (no meaningful single lattice).
func ParseCollectionGrids(r io.Reader) (map[string]RefGrid, error) {
	out := map[string]RefGrid{}
	_, err := ParseCollection(r, func(t Track) {
		if len(t.Beatgrid) != 1 || t.Path == "" {
			return
		}
		bpm := t.Beatgrid[0].BPM
		if bpm <= 0 {
			bpm = t.BPM
		}
		out[t.Path] = RefGrid{StartMs: t.Beatgrid[0].PositionMs, BPM: bpm}
	})
	return out, err
}

// repairStream streams src→dst, buffering each COLLECTION ENTRY and repairing it.
func repairStream(src io.Reader, dst io.Writer, opts RepairOptions, rep *RepairReport) error {
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	var buf []xml.Token
	inCollection := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
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
				return err
			}
			continue
		}
		buf = append(buf, xml.CopyToken(tok))
		if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "ENTRY" {
			rep.Entries++
			if err := repairEntry(enc, buf, opts, rep); err != nil {
				return err
			}
			buf = nil
		}
	}
	return enc.Flush()
}

// repairCue is one CUE_V2 of the entry under analysis.
type repairCue struct {
	tokIdx  int // StartElement position in buf
	typ     int
	startMs float64
	lenMs   float64
	hotcue  int
	name    string
	drop    bool // stacked duplicate - subtree dropped
	newSlot int  // repaired HOTCUE (== hotcue when unchanged)
	split   bool // padded TYPE-4: emit a plain pad clone after it
}

// repairEntry analyzes one buffered ENTRY and re-encodes it with the repairs applied.
// Entries without a resolvable LOCATION (embedded remix-set/sample entries) pass through
// untouched - their pad slots aren't track-time semantics.
func repairEntry(enc *xml.Encoder, buf []xml.Token, opts RepairOptions, rep *RepairReport) error {
	if entryLocPath(buf) == "" {
		for _, tk := range buf {
			if err := enc.EncodeToken(tk); err != nil {
				return err
			}
		}
		return nil
	}
	var cues []repairCue
	depth := 0
	for i, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if depth > 0 {
				depth++
				continue
			}
			if el.Name.Local == "CUE_V2" {
				c := repairCue{tokIdx: i}
				c.typ, _ = strconv.Atoi(attrVal(el.Attr, "TYPE"))
				c.startMs, _ = strconv.ParseFloat(attrVal(el.Attr, "START"), 64)
				c.lenMs, _ = strconv.ParseFloat(attrVal(el.Attr, "LEN"), 64)
				c.hotcue = -1
				if v := attrVal(el.Attr, "HOTCUE"); v != "" {
					c.hotcue, _ = strconv.Atoi(v)
				}
				c.name = attrVal(el.Attr, "NAME")
				c.newSlot = c.hotcue
				cues = append(cues, c)
				depth = 1
			}
		case xml.EndElement:
			if depth > 0 {
				depth--
			}
		}
	}

	dupes, split := analyzeCues(cues)
	reordered := renumberRepairPads(cues)
	gridDelta, gridNewStart := analyzeGridRestore(buf, cues, opts.Ref)

	changed := dupes > 0 || split > 0 || reordered || !math.IsNaN(gridNewStart)
	if changed {
		rep.Changed++
		rep.DupesRemoved += dupes
		rep.PadsSplit += split
		if reordered {
			rep.PadsReordered++
		}
		d := entryLocPath(buf)
		var what []string
		if reordered {
			what = append(what, "pads reordered")
		}
		if dupes > 0 {
			what = append(what, fmt.Sprintf("%d duplicate cue(s) dropped", dupes))
		}
		if split > 0 {
			what = append(what, "padded grid cue split")
		}
		if !math.IsNaN(gridNewStart) {
			rep.GridsRestored++
			what = append(what, fmt.Sprintf("grid restored (%+.1fms)", gridDelta))
		}
		rep.detail(fmt.Sprintf("%s: %s", d, joinComma(what)))
	}

	// emit pass
	ci := -1
	skip := 0
	for _, tk := range buf {
		switch el := tk.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			if el.Name.Local == "CUE_V2" {
				ci++
				c := &cues[ci]
				if c.drop {
					skip = 1
					continue
				}
				if c.newSlot != c.hotcue || (c.typ == 4 && c.hotcue >= 0) {
					slot := c.newSlot
					if c.typ == 4 {
						slot = -1 // pad moves to the split clone
					}
					el.Attr = setAttr(el.Attr, "HOTCUE", strconv.Itoa(slot))
				}
				if c.typ == 4 && !math.IsNaN(gridNewStart) {
					el.Attr = setAttr(el.Attr, "START", f6(gridNewStart))
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
			if err := enc.EncodeToken(el); err != nil {
				return err
			}
			if el.Name.Local == "CUE_V2" && ci >= 0 && cues[ci].split {
				c := cues[ci]
				name := c.name
				if name == "" {
					name = "n.n."
				}
				pad := startElem("CUE_V2", [][2]string{
					{"NAME", name}, {"DISPL_ORDER", "0"}, {"TYPE", "0"},
					{"START", f6(c.startMs)}, {"LEN", "0.000000"}, {"REPEATS", "-1"},
					{"HOTCUE", strconv.Itoa(c.newSlot)},
				})
				if err := enc.EncodeToken(pad); err != nil {
					return err
				}
				if err := enc.EncodeToken(pad.End()); err != nil {
					return err
				}
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

// analyzeCues marks stacked duplicates (same TYPE, ~same position; loops also ~same
// length) and padded TYPE-4 anchors to split. Keeper priority: padded > named > first.
func analyzeCues(cues []repairCue) (dupes, split int) {
	for i := range cues {
		c := &cues[i]
		if c.typ == 4 && c.hotcue >= 0 {
			c.split = true
			split++
		}
		if c.drop || c.typ == 4 || c.typ == 3 || c.typ == 1 || c.typ == 2 {
			continue // only musical cues (cue/loop) dedupe
		}
		for j := i + 1; j < len(cues); j++ {
			o := &cues[j]
			if o.drop || o.typ != c.typ || math.Abs(o.startMs-c.startMs) > dupeEpsMs {
				continue
			}
			if c.typ == 5 && math.Abs(o.lenMs-c.lenMs) > dupeEpsMs {
				continue
			}
			loser := o
			if repairKeeperBeats(*o, *c) {
				loser = c
			}
			loser.drop = true
			dupes++
			if loser == c {
				break
			}
		}
	}
	return dupes, split
}

// repairKeeperBeats reports whether a should be kept over b.
func repairKeeperBeats(a, b repairCue) bool {
	if (a.hotcue >= 0) != (b.hotcue >= 0) {
		return a.hotcue >= 0
	}
	an, bn := a.name != "" && a.name != "n.n.", b.name != "" && b.name != "n.n."
	if an != bn {
		return an
	}
	return false
}

// renumberRepairPads reassigns surviving pad slots (non-grid cues + split clones) to
// ascending track-time order. Reports whether anything changed.
func renumberRepairPads(cues []repairCue) bool {
	var padded []int
	for i := range cues {
		if cues[i].drop {
			continue
		}
		if (cues[i].typ != 4 && cues[i].hotcue >= 0) || cues[i].split {
			padded = append(padded, i)
		}
	}
	sort.SliceStable(padded, func(a, b int) bool { return cues[padded[a]].startMs < cues[padded[b]].startMs })
	changed := false
	for n, i := range padded {
		if cues[i].newSlot != n {
			cues[i].newSlot = n
			changed = true
		}
		if cues[i].typ == 4 {
			changed = true // pad relocates onto the split clone even when the slot number holds
		}
	}
	return changed
}

// analyzeGridRestore decides whether the entry's single grid marker should move back
// onto the reference lattice. Returns (delta ms, new START) or (0, NaN) for no-op.
func analyzeGridRestore(buf []xml.Token, cues []repairCue, ref map[string]RefGrid) (float64, float64) {
	if ref == nil {
		return 0, math.NaN()
	}
	var grid *repairCue
	gridCount := 0
	for i := range cues {
		if cues[i].typ == 4 {
			gridCount++
			grid = &cues[i]
		}
	}
	if gridCount != 1 {
		return 0, math.NaN()
	}
	rg, ok := ref[entryLocPath(buf)]
	if !ok || rg.BPM <= 0 {
		return 0, math.NaN()
	}
	liveBPM := entryGridBPMOrTempo(buf)
	if liveBPM <= 0 {
		return 0, math.NaN()
	}
	// BPM compatibility: unchanged, or the reference snapped to the live integer value
	if math.Abs(liveBPM-rg.BPM) > 0.01 && math.Abs(liveBPM-math.RoundToEven(rg.BPM)) > 1e-6 {
		return 0, math.NaN()
	}
	period := 60000.0 / liveBPM
	target := snapToLattice(grid.startMs, rg.StartMs, liveBPM)
	ph := math.Abs(grid.startMs - target)
	if ph = math.Min(ph, period-ph); ph < gridRestoreMinMs || ph > gridRestoreMaxMs {
		return 0, math.NaN()
	}
	return target - grid.startMs, target
}

// entryGridBPMOrTempo returns the buffered ENTRY's grid BPM (GRID child of its TYPE-4,
// else TEMPO BPM; 0 = none).
func entryGridBPMOrTempo(buf []xml.Token) float64 {
	if g := entryGridCues(buf); len(g) > 0 && g[0].BPM > 0 {
		return g[0].BPM
	}
	return entryTempoBPM(buf)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
