// Package mp4frag indexes fragmented MP4s (OBS recordings) for MSE playback. Chromium's
// demuxer ignores mfra and range-scans every moof of a multi-GB fMP4 before it will play or
// seek - ~1 GB of reads and 30 s+ on a busy disk for an hour-long set. This parser reads only
// headers (ftyp+moov ≈ KBs, mfra tail ≈ tens of KB): init-segment length, MSE codec string,
// per-fragment {time, byte offset} and duration. The webview feeds fragments through
// MediaSource instead of a plain <video src>, so start and seek touch only the bytes they
// need. No mfra (crash-cut recording) → sequential moof header walk, salvaging every
// fragment up to the corruption point. Pure stdlib, headers only - never decodes media.
package mp4frag

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ErrNotFragmented marks a classic MP4 (moov carries the sample tables) - plain <video src>
// handles those fine; MSE is only for the fragmented layout.
var ErrNotFragmented = errors.New("not a fragmented mp4")

// Frag is one moof+mdat pair: start time (seconds) and moof byte offset.
type Frag struct {
	T float64 `json:"t"`
	O int64   `json:"o"`
}

// ContractVer bumps when the Index JSON contract changes so older cached blobs re-parse.
// 2 = InitB64 (sanitized init segment) added.
const ContractVer = 2

// Index is everything the MSE runtime needs to stream a fragmented MP4.
type Index struct {
	Ver      int     `json:"v"`       // ContractVer at parse time
	Mime     string  `json:"mime"`    // e.g. `video/mp4; codecs="avc1.64002a,mp4a.40.2"`
	InitLen  int64   `json:"init"`    // init segment = file bytes [0, InitLen)
	InitB64  string  `json:"initb64"` // SANITIZED init segment (see sanitizeInit) - what MSE appends
	Duration float64 `json:"dur"`     // seconds (last fragment start + its sample durations)
	End      int64   `json:"end"`     // fragment data end (mfra offset / walk stop); last frag = [O, End)
	Frags    []Frag  `json:"frags"`
}

const (
	maxMoovLen = 32 << 20 // init segment sanity cap: a real fMP4 moov is KBs
	maxMfraLen = 64 << 20
)

type box struct {
	typ  string
	off  int64 // absolute file offset of the box header
	hdr  int64 // header length (8 or 16)
	size int64 // total box size incl. header
}

// readBox parses the box header at off. size==0 (to EOF) is normalized against fileSize.
func readBox(r io.ReaderAt, off, fileSize int64) (box, error) {
	var h [16]byte
	if _, err := r.ReadAt(h[:8], off); err != nil {
		return box{}, err
	}
	b := box{typ: string(h[4:8]), off: off, hdr: 8, size: int64(binary.BigEndian.Uint32(h[:4]))}
	switch b.size {
	case 0:
		b.size = fileSize - off
	case 1:
		if _, err := r.ReadAt(h[8:16], off+8); err != nil {
			return box{}, err
		}
		b.hdr = 16
		b.size = int64(binary.BigEndian.Uint64(h[8:16]))
	}
	if b.size < b.hdr {
		return box{}, fmt.Errorf("bad box size %d at %d", b.size, off)
	}
	return b, nil
}

// child finds the first direct child box of typ inside body (raw bytes past the parent header).
func child(body []byte, typ string) []byte {
	for off := 0; off+8 <= len(body); {
		size := int(binary.BigEndian.Uint32(body[off:]))
		hdr := 8
		if size == 1 && off+16 <= len(body) {
			size = int(binary.BigEndian.Uint64(body[off+8:]))
			hdr = 16
		}
		if size < hdr || off+size > len(body) {
			return nil
		}
		if string(body[off+4:off+8]) == typ {
			return body[off+hdr : off+size]
		}
		off += size
	}
	return nil
}

// children collects every direct child box of typ inside body.
func children(body []byte, typ string) [][]byte {
	var out [][]byte
	for off := 0; off+8 <= len(body); {
		size := int(binary.BigEndian.Uint32(body[off:]))
		hdr := 8
		if size == 1 && off+16 <= len(body) {
			size = int(binary.BigEndian.Uint64(body[off+8:]))
			hdr = 16
		}
		if size < hdr || off+size > len(body) {
			return out
		}
		if string(body[off+4:off+8]) == typ {
			out = append(out, body[off+hdr:off+size])
		}
		off += size
	}
	return out
}

// trak is the per-track header info the index build needs.
type trak struct {
	id        uint32
	timescale uint32
	video     bool
	codec     string // RFC 6381 codec string ("" = unsupported)
	defDur    uint32 // trex default_sample_duration
}

// Parse indexes the fragmented MP4 at path. ErrNotFragmented for classic MP4s.
func Parse(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return parse(f, fi.Size())
}

func parse(r io.ReaderAt, fileSize int64) (*Index, error) {
	// top level: ftyp, then moov (mvex = fragmented) before the first moof
	var off, initEnd, firstMoof int64
	var moovRaw []byte
	for off < fileSize {
		b, err := readBox(r, off, fileSize)
		if err != nil {
			return nil, err
		}
		switch b.typ {
		case "moov":
			if b.size > maxMoovLen {
				return nil, fmt.Errorf("moov too large (%d)", b.size)
			}
			moovRaw = make([]byte, b.size-b.hdr)
			if _, err := r.ReadAt(moovRaw, b.off+b.hdr); err != nil {
				return nil, err
			}
			initEnd = b.off + b.size
		case "moof":
			firstMoof = b.off
		case "mdat":
			if moovRaw != nil && firstMoof == 0 {
				return nil, ErrNotFragmented // moov with samples, media follows - classic layout
			}
			if moovRaw == nil {
				return nil, ErrNotFragmented // moov at EOF (classic non-faststart); Chromium handles it in 2 requests
			}
		}
		if firstMoof > 0 {
			break
		}
		off += b.size
	}
	if moovRaw == nil || firstMoof == 0 {
		return nil, ErrNotFragmented
	}
	if child(moovRaw, "mvex") == nil {
		return nil, ErrNotFragmented
	}
	if initEnd > firstMoof {
		return nil, fmt.Errorf("moov end %d past first moof %d", initEnd, firstMoof)
	}

	traks, err := parseTraks(moovRaw)
	if err != nil {
		return nil, err
	}
	lead := pickTrack(traks)
	if lead == nil {
		return nil, errors.New("no usable track")
	}
	mime, err := mseMime(traks, lead.video)
	if err != nil {
		return nil, err
	}

	frags, end, err := fragsFromMfra(r, fileSize, lead)
	if err != nil {
		frags, end, err = fragsFromWalk(r, fileSize, firstMoof, lead)
		if err != nil {
			return nil, err
		}
	}
	if len(frags) == 0 {
		return nil, errors.New("no fragments")
	}

	dur := frags[len(frags)-1].T + lastFragDur(r, fileSize, frags[len(frags)-1].O, lead)
	init := make([]byte, initEnd)
	if _, err := r.ReadAt(init, 0); err != nil {
		return nil, err
	}
	sanitizeInit(init)
	return &Index{Ver: ContractVer, Mime: mime, InitLen: initEnd,
		InitB64: base64.StdEncoding.EncodeToString(init), Duration: dur, End: end, Frags: frags}, nil
}

// sanitizeInit patches (in place, sizes unchanged) init-segment quirks Chromium's MSE parser
// rejects but its file demuxer tolerates. Today: OBS writes samplesize=0 in the FLAC
// AudioSampleEntry - MSE errors the whole init append on it; the field must be 16.
func sanitizeInit(init []byte) {
	moov := child(init, "moov")
	if moov == nil {
		return
	}
	for _, tk := range children(moov, "trak") {
		mdia := child(tk, "mdia")
		if mdia == nil {
			continue
		}
		if hdlr := child(mdia, "hdlr"); len(hdlr) < 12 || string(hdlr[8:12]) != "soun" {
			continue
		}
		minf := child(mdia, "minf")
		if minf == nil {
			continue
		}
		stbl := child(minf, "stbl")
		if stbl == nil {
			continue
		}
		stsd := child(stbl, "stsd")
		// first sample entry: 8 box hdr + 6 reserved + 2 dataref + 8 reserved + 2 channels,
		// then the 2-byte samplesize. child() returns sub-slices of init, so writes land in place.
		if len(stsd) >= 8+28 {
			entry := stsd[8:]
			if entry[26] == 0 && entry[27] == 0 {
				entry[26], entry[27] = 0x00, 0x10 // 16
			}
		}
	}
}

// parseTraks extracts id/timescale/handler/codec per trak + trex defaults.
func parseTraks(moov []byte) ([]trak, error) {
	var out []trak
	for _, tk := range children(moov, "trak") {
		var t trak
		if tkhd := child(tk, "tkhd"); len(tkhd) >= 24 {
			if tkhd[0] == 1 { // version 1: 8-byte times
				t.id = binary.BigEndian.Uint32(tkhd[20:])
			} else {
				t.id = binary.BigEndian.Uint32(tkhd[12:])
			}
		}
		mdia := child(tk, "mdia")
		if mdia == nil {
			continue
		}
		if mdhd := child(mdia, "mdhd"); len(mdhd) >= 24 {
			if mdhd[0] == 1 {
				t.timescale = binary.BigEndian.Uint32(mdhd[20:])
			} else {
				t.timescale = binary.BigEndian.Uint32(mdhd[12:])
			}
		}
		if hdlr := child(mdia, "hdlr"); len(hdlr) >= 12 {
			t.video = string(hdlr[8:12]) == "vide"
		}
		if minf := child(mdia, "minf"); minf != nil {
			if stbl := child(minf, "stbl"); stbl != nil {
				if stsd := child(stbl, "stsd"); len(stsd) > 8 {
					t.codec = codecString(stsd[8:], t.video) // past fullbox ver/flags + entry_count
				}
			}
		}
		if t.timescale == 0 {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, errors.New("no traks in moov")
	}
	if mvex := child(moov, "mvex"); mvex != nil {
		for _, tx := range children(mvex, "trex") {
			if len(tx) < 24 {
				continue
			}
			id := binary.BigEndian.Uint32(tx[4:])
			for i := range out {
				if out[i].id == id {
					out[i].defDur = binary.BigEndian.Uint32(tx[12:])
				}
			}
		}
	}
	return out, nil
}

// pickTrack chooses the index's time reference: the video track, else the first.
func pickTrack(traks []trak) *trak {
	for i := range traks {
		if traks[i].video {
			return &traks[i]
		}
	}
	if len(traks) > 0 {
		return &traks[0]
	}
	return nil
}

// mseMime builds the addSourceBuffer mime. Every track must have a known codec - MSE
// rejects the whole buffer otherwise, and the caller then falls back to plain src.
func mseMime(traks []trak, hasVideo bool) (string, error) {
	var codecs []string
	for _, t := range traks {
		if t.codec == "" {
			return "", errors.New("unsupported codec for MSE")
		}
		codecs = append(codecs, t.codec)
	}
	kind := "audio/mp4"
	if hasVideo {
		kind = "video/mp4"
	}
	return kind + `; codecs="` + strings.Join(codecs, ",") + `"`, nil
}

// codecString maps the first stsd sample entry to an RFC 6381 codec string ("" = unsupported).
func codecString(entries []byte, video bool) string {
	if len(entries) < 16 {
		return ""
	}
	size := int(binary.BigEndian.Uint32(entries[:4]))
	if size < 16 || size > len(entries) {
		return ""
	}
	format := string(entries[4:8])
	// sample-entry fixed part: 8 header + 6 reserved + 2 data_ref; visual adds 70, audio 20
	fixed := 16
	if video {
		fixed += 70
	} else {
		fixed += 20
	}
	if size < fixed {
		return ""
	}
	sub := entries[fixed:size]
	switch format {
	case "avc1", "avc3":
		if c := child(sub, "avcC"); len(c) >= 4 {
			return fmt.Sprintf("%s.%02x%02x%02x", format, c[1], c[2], c[3])
		}
	case "hvc1", "hev1":
		if c := child(sub, "hvcC"); len(c) >= 13 {
			return hevcCodec(format, c)
		}
	case "av01":
		if c := child(sub, "av1C"); len(c) >= 4 {
			profile := c[1] >> 5
			level := c[1] & 0x1f
			tier := "M"
			if c[2]&0x80 != 0 {
				tier = "H"
			}
			depth := 8
			if c[2]&0x40 != 0 { // high_bitdepth
				depth = 10
				if c[2]&0x20 != 0 {
					depth = 12
				}
			}
			return fmt.Sprintf("av01.%d.%02d%s.%02d", profile, level, tier, depth)
		}
	case "mp4a":
		if c := child(sub, "esds"); c != nil {
			return mp4aCodec(c)
		}
	case "Opus":
		return "opus"
	case "fLaC":
		return "flac"
	}
	return ""
}

// hevcCodec builds the ISO 14496-15 Annex E codec string from an hvcC record.
func hevcCodec(format string, c []byte) string {
	space := ""
	switch c[1] >> 6 {
	case 1:
		space = "A"
	case 2:
		space = "B"
	case 3:
		space = "C"
	}
	profile := c[1] & 0x1f
	compat := binary.BigEndian.Uint32(c[2:6])
	// compat flags print bit-reversed (bit 31 first), trailing zeros trimmed by %x of the reversal
	var rev uint32
	for i := 0; i < 32; i++ {
		if compat&(1<<uint(31-i)) != 0 {
			rev |= 1 << uint(i)
		}
	}
	tier := "L"
	if c[1]&0x20 != 0 {
		tier = "H"
	}
	s := fmt.Sprintf("%s.%s%d.%x.%s%d", format, space, profile, rev, tier, c[12])
	// constraint bytes 6..11: dot-separated hex, trailing zero bytes omitted
	cons := c[6:12]
	last := -1
	for i, v := range cons {
		if v != 0 {
			last = i
		}
	}
	for i := 0; i <= last; i++ {
		s += fmt.Sprintf(".%x", cons[i])
	}
	return s
}

// mp4aCodec extracts the AAC object type from an esds ("mp4a.40.<aot>").
func mp4aCodec(esds []byte) string {
	b := esds[4:] // past fullbox
	// ES descriptor (tag 3): skip expandable length, ES_ID(2), flags(1) + optional deps
	if len(b) < 2 || b[0] != 0x03 {
		return ""
	}
	b = skipDescLen(b[1:])
	if len(b) < 3 {
		return ""
	}
	flags := b[2]
	b = b[3:]
	if flags&0x80 != 0 && len(b) >= 2 { // streamDependenceFlag
		b = b[2:]
	}
	if flags&0x40 != 0 && len(b) >= 1 { // URL_Flag
		n := int(b[0])
		if len(b) < 1+n {
			return ""
		}
		b = b[1+n:]
	}
	if flags&0x20 != 0 && len(b) >= 2 { // OCRstreamFlag
		b = b[2:]
	}
	// DecoderConfigDescriptor (tag 4): objectTypeIndication then 12 bytes to DecoderSpecificInfo
	if len(b) < 2 || b[0] != 0x04 {
		return ""
	}
	b = skipDescLen(b[1:])
	if len(b) < 14 {
		return ""
	}
	oti := b[0]
	b = b[13:]
	if b[0] != 0x05 { // DecoderSpecificInfo: AudioSpecificConfig
		return fmt.Sprintf("mp4a.%02x", oti)
	}
	b = skipDescLen(b[1:])
	if len(b) < 1 {
		return fmt.Sprintf("mp4a.%02x", oti)
	}
	aot := b[0] >> 3
	if aot == 31 && len(b) >= 2 { // escape: 6-bit extension
		aot = 32 + ((b[0]&0x07)<<3 | b[1]>>5)
	}
	return fmt.Sprintf("mp4a.%02x.%d", oti, aot)
}

// skipDescLen skips an MPEG-4 descriptor's expandable length field.
func skipDescLen(b []byte) []byte {
	for i := 0; i < 4 && i < len(b); i++ {
		if b[i]&0x80 == 0 {
			return b[i+1:]
		}
	}
	return nil
}

// fragsFromMfra reads the tail mfra and returns the lead track's fragment table.
func fragsFromMfra(r io.ReaderAt, fileSize int64, lead *trak) ([]Frag, int64, error) {
	var tail [16]byte
	if fileSize < 16 {
		return nil, 0, errors.New("file too small")
	}
	if _, err := r.ReadAt(tail[:], fileSize-16); err != nil {
		return nil, 0, err
	}
	if string(tail[4:8]) != "mfro" {
		return nil, 0, errors.New("no mfra")
	}
	mfraLen := int64(binary.BigEndian.Uint32(tail[12:]))
	if mfraLen <= 0 || mfraLen > maxMfraLen || mfraLen > fileSize {
		return nil, 0, errors.New("bad mfro size")
	}
	mfraOff := fileSize - mfraLen
	b, err := readBox(r, mfraOff, fileSize)
	if err != nil || b.typ != "mfra" {
		return nil, 0, errors.New("no mfra box")
	}
	raw := make([]byte, b.size-b.hdr)
	if _, err := r.ReadAt(raw, b.off+b.hdr); err != nil {
		return nil, 0, err
	}
	for _, tf := range children(raw, "tfra") {
		frags, ok := parseTfra(tf, lead)
		if ok {
			return frags, mfraOff, nil
		}
	}
	return nil, 0, errors.New("no tfra for lead track")
}

// parseTfra decodes one tfra's entries if it belongs to the lead track.
func parseTfra(tf []byte, lead *trak) ([]Frag, bool) {
	if len(tf) < 24 {
		return nil, false
	}
	ver := tf[0]
	if binary.BigEndian.Uint32(tf[4:]) != lead.id {
		return nil, false
	}
	sizes := binary.BigEndian.Uint32(tf[8:])
	nTraf := int(sizes>>4&3) + 1
	nTrun := int(sizes>>2&3) + 1
	nSamp := int(sizes&3) + 1
	n := int(binary.BigEndian.Uint32(tf[12:]))
	entry := 8 + 8 + nTraf + nTrun + nSamp // ver 1
	timeLen := 8
	if ver == 0 {
		entry = 4 + 4 + nTraf + nTrun + nSamp
		timeLen = 4
	}
	if n <= 0 || 16+n*entry > len(tf) {
		return nil, false
	}
	ts := float64(lead.timescale)
	frags := make([]Frag, 0, n)
	off := 16
	var lastO int64 = -1
	for i := 0; i < n; i++ {
		var t, o uint64
		if timeLen == 8 {
			t = binary.BigEndian.Uint64(tf[off:])
			o = binary.BigEndian.Uint64(tf[off+8:])
		} else {
			t = uint64(binary.BigEndian.Uint32(tf[off:]))
			o = uint64(binary.BigEndian.Uint32(tf[off+4:]))
		}
		off += entry
		// tfra may carry several entries per moof (random-access samples); keep the first
		if int64(o) == lastO {
			continue
		}
		if int64(o) < lastO {
			return nil, false // offsets must ascend
		}
		lastO = int64(o)
		frags = append(frags, Frag{T: float64(t) / ts, O: int64(o)})
	}
	return frags, len(frags) > 0
}

// fragsFromWalk sequentially walks top-level boxes from the first moof (mfra missing - e.g.
// a crash-cut recording), reading each moof's tfdt for the fragment start time. Stops
// cleanly at the first unparsable box, keeping everything salvaged so far.
func fragsFromWalk(r io.ReaderAt, fileSize, firstMoof int64, lead *trak) ([]Frag, int64, error) {
	var frags []Frag
	ts := float64(lead.timescale)
	off := firstMoof
	end := fileSize
	for off < fileSize {
		b, err := readBox(r, off, fileSize)
		if err != nil || b.off+b.size > fileSize {
			end = off // truncated tail: media before this point still plays
			break
		}
		switch b.typ {
		case "moof":
			if b.size > 16<<20 {
				end = off
				off = fileSize // corrupt size - stop
				break
			}
			raw := make([]byte, b.size-b.hdr)
			if _, err := r.ReadAt(raw, b.off+b.hdr); err != nil {
				end = off
				off = fileSize
				break
			}
			if t, ok := moofTfdt(raw, lead.id); ok {
				frags = append(frags, Frag{T: float64(t) / ts, O: b.off})
			}
		case "mfra":
			end = off
			off = fileSize
		}
		if off >= fileSize {
			break
		}
		off += b.size
	}
	if len(frags) == 0 {
		return nil, 0, errors.New("walk found no fragments")
	}
	return frags, end, nil
}

// moofTfdt returns the lead track's baseMediaDecodeTime within a moof body.
func moofTfdt(moof []byte, trackID uint32) (uint64, bool) {
	for _, traf := range children(moof, "traf") {
		tfhd := child(traf, "tfhd")
		if len(tfhd) < 8 || binary.BigEndian.Uint32(tfhd[4:]) != trackID {
			continue
		}
		tfdt := child(traf, "tfdt")
		if len(tfdt) < 8 {
			return 0, false
		}
		if tfdt[0] == 1 {
			if len(tfdt) < 12 {
				return 0, false
			}
			return binary.BigEndian.Uint64(tfdt[4:]), true
		}
		return uint64(binary.BigEndian.Uint32(tfdt[4:])), true
	}
	return 0, false
}

// lastFragDur sums the final fragment's sample durations for the lead track (0 on any
// parse trouble - duration is then just the last fragment's start, close enough).
func lastFragDur(r io.ReaderAt, fileSize, moofOff int64, lead *trak) float64 {
	b, err := readBox(r, moofOff, fileSize)
	if err != nil || b.typ != "moof" || b.size > 16<<20 {
		return 0
	}
	raw := make([]byte, b.size-b.hdr)
	if _, err := r.ReadAt(raw, b.off+b.hdr); err != nil {
		return 0
	}
	for _, traf := range children(raw, "traf") {
		tfhd := child(traf, "tfhd")
		if len(tfhd) < 8 || binary.BigEndian.Uint32(tfhd[4:]) != lead.id {
			continue
		}
		defDur := lead.defDur
		flags := binary.BigEndian.Uint32(tfhd[:4]) & 0xffffff
		p := 8
		if flags&0x01 != 0 { // base-data-offset
			p += 8
		}
		if flags&0x02 != 0 { // sample-description-index
			p += 4
		}
		if flags&0x08 != 0 && len(tfhd) >= p+4 { // default-sample-duration
			defDur = binary.BigEndian.Uint32(tfhd[p:])
		}
		var total uint64
		for _, trun := range children(traf, "trun") {
			if len(trun) < 8 {
				continue
			}
			tflags := binary.BigEndian.Uint32(trun[:4]) & 0xffffff
			count := int(binary.BigEndian.Uint32(trun[4:]))
			q := 8
			if tflags&0x001 != 0 { // data-offset
				q += 4
			}
			if tflags&0x004 != 0 { // first-sample-flags
				q += 4
			}
			per := 0
			if tflags&0x100 != 0 { // sample-duration present
				per += 4
			}
			if tflags&0x200 != 0 { // sample-size
				per += 4
			}
			if tflags&0x400 != 0 { // sample-flags
				per += 4
			}
			if tflags&0x800 != 0 { // sample-cts
				per += 4
			}
			if tflags&0x100 != 0 {
				for i := 0; i < count && q+per*i+4 <= len(trun); i++ {
					total += uint64(binary.BigEndian.Uint32(trun[q+per*i:]))
				}
			} else {
				total += uint64(count) * uint64(defDur)
			}
		}
		return float64(total) / float64(lead.timescale)
	}
	return 0
}
