package mp4frag

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"testing"
)

// ── synthetic box builders ──────────────────────────────────────────────────────

func bx(typ string, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], body)
	return out
}

func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

func full(ver byte, flags uint32, body ...[]byte) []byte {
	h := u32(flags & 0xffffff)
	h[0] = ver
	return append(h, bytes.Join(body, nil)...)
}

// visual sample entry: 8 reserved/dataref + 70 fixed bytes, then children
func videoEntry(format string, kids ...[]byte) []byte {
	body := make([]byte, 8+70)
	body = append(body, bytes.Join(kids, nil)...)
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(out)))
	copy(out[4:8], format)
	copy(out[8:], body)
	return out
}

func audioEntry(format string, kids ...[]byte) []byte {
	body := make([]byte, 8+20)
	body = append(body, bytes.Join(kids, nil)...)
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(out)))
	copy(out[4:8], format)
	copy(out[8:], body)
	return out
}

func trakBox(id, timescale uint32, handler string, sampleEntry []byte) []byte {
	tkhd := bx("tkhd", full(0, 7, make([]byte, 8), u32(id), make([]byte, 60))) // ver0: times(8) id rest
	mdhd := bx("mdhd", full(0, 0, make([]byte, 8), u32(timescale), u32(0), make([]byte, 4)))
	hdlr := bx("hdlr", full(0, 0, u32(0), []byte(handler), make([]byte, 13)))
	stsd := bx("stsd", full(0, 0, u32(1), sampleEntry))
	stbl := bx("stbl", stsd)
	minf := bx("minf", stbl)
	mdia := bx("mdia", mdhd, hdlr, minf)
	return bx("trak", tkhd, mdia)
}

// avcC: ver,profile,compat,level (High 4.2 → avc1.640028-style)
func avcC() []byte { return bx("avcC", []byte{1, 0x64, 0x00, 0x2a, 0xff}) }

// esds carrying AAC-LC (OTI 0x40, AOT 2)
func esds() []byte {
	dsi := []byte{0x05, 2, 0x12, 0x10}                                         // AudioSpecificConfig: AOT=2
	dcd := append([]byte{0x04, byte(13 + 4), 0x40, 0x15}, make([]byte, 11)...) // OTI 0x40 + 11 filler
	dcd = append(dcd, dsi...)
	es := append([]byte{0x03, byte(3 + len(dcd)), 0, 0, 0}, dcd...)
	return bx("esds", full(0, 0, es))
}

func moofBox(seq, videoID uint32, tfdtTime uint64, sampleCount int, sampleDur uint32) []byte {
	mfhd := bx("mfhd", full(0, 0, u32(seq)))
	tfhd := bx("tfhd", full(0, 0x08, u32(videoID), u32(sampleDur))) // default-sample-duration
	tfdt := bx("tfdt", full(1, 0, u64(tfdtTime)))
	trun := bx("trun", full(0, 0, u32(uint32(sampleCount)))) // durations from tfhd default
	traf := bx("traf", tfhd, tfdt, trun)
	return bx("moof", mfhd, traf)
}

// buildFMP4 assembles ftyp+moov+N×(moof+mdat)[+mfra] with 90kHz video track 1 + audio track 2.
func buildFMP4(t *testing.T, nFrags int, withMfra bool) (string, []int64, []uint64) {
	t.Helper()
	const ts = 90000
	ftyp := bx("ftyp", []byte("isom"), u32(512), []byte("isomiso2"))
	mvhd := full(0, 0, make([]byte, 8), u32(1000), u32(0), make([]byte, 80))
	trex := func(id uint32) []byte {
		return bx("trex", full(0, 0, u32(id), u32(1), u32(3000), u32(0), u32(0)))
	}
	moov := bx("moov", bx("mvhd", mvhd),
		trakBox(1, ts, "vide", videoEntry("avc1", avcC())),
		trakBox(2, 48000, "soun", audioEntry("mp4a", esds())),
		bx("mvex", trex(1), trex(2)))

	file := append([]byte{}, ftyp...)
	file = append(file, moov...)
	var moofOffs []int64
	var times []uint64
	for i := 0; i < nFrags; i++ {
		tm := uint64(i) * 2 * ts // 2s per fragment
		moofOffs = append(moofOffs, int64(len(file)))
		times = append(times, tm)
		file = append(file, moofBox(uint32(i+1), 1, tm, 60, 3000)...) // 60×3000/90000 = 2s
		file = append(file, bx("mdat", bytes.Repeat([]byte{0xAB}, 512))...)
	}
	if withMfra {
		var entries []byte
		for i := range moofOffs {
			entries = append(entries, u64(times[i])...)
			entries = append(entries, u64(uint64(moofOffs[i]))...)
			entries = append(entries, 1, 1, 1) // traf/trun/sample numbers (1 byte each)
		}
		tfra := bx("tfra", full(1, 0, u32(1), u32(0), u32(uint32(len(moofOffs))), entries))
		mfroLen := len(bx("mfra", tfra)) + 16
		mfro := bx("mfro", full(0, 0, u32(uint32(mfroLen))))
		file = append(file, bx("mfra", tfra, mfro)...)
	}

	p := t.TempDir() + "/synth.mp4"
	if err := os.WriteFile(p, file, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, moofOffs, times
}

// ── tests ───────────────────────────────────────────────────────────────────────

func TestParseMfraIndex(t *testing.T) {
	p, offs, times := buildFMP4(t, 5, true)
	idx, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Mime != `video/mp4; codecs="avc1.64002a,mp4a.40.2"` {
		t.Fatalf("mime: %s", idx.Mime)
	}
	if len(idx.Frags) != 5 {
		t.Fatalf("frags: %d", len(idx.Frags))
	}
	for i, fr := range idx.Frags {
		if fr.O != offs[i] {
			t.Fatalf("frag %d offset %d want %d", i, fr.O, offs[i])
		}
		want := float64(times[i]) / 90000
		if math.Abs(fr.T-want) > 1e-9 {
			t.Fatalf("frag %d time %f want %f", i, fr.T, want)
		}
	}
	if idx.InitLen != offs[0] {
		t.Fatalf("init %d want %d", idx.InitLen, offs[0])
	}
	if idx.Ver != ContractVer {
		t.Fatalf("ver %d want %d", idx.Ver, ContractVer)
	}
	init, err := base64.StdEncoding.DecodeString(idx.InitB64)
	if err != nil || int64(len(init)) != idx.InitLen {
		t.Fatalf("initb64: err=%v len=%d want %d", err, len(init), idx.InitLen)
	}
	// sanitizer: the synthetic audio entry carries samplesize=0 (as OBS writes) → must be 16
	raw, _ := os.ReadFile(p)
	ai := bytes.Index(init, []byte("mp4a"))
	if ai < 0 {
		t.Fatal("no mp4a entry in init")
	}
	e := ai - 4 // entry start (size field)
	if init[e+26] != 0x00 || init[e+27] != 0x10 {
		t.Fatalf("audio samplesize not sanitized to 16: %x %x", init[e+26], init[e+27])
	}
	if raw[e+26] != 0 || raw[e+27] != 0 {
		t.Fatalf("test premise broken: raw samplesize not 0")
	}
	// 5 frags × 2 s: last starts at 8 s + 2 s of samples
	if math.Abs(idx.Duration-10) > 0.01 {
		t.Fatalf("duration %f want 10", idx.Duration)
	}
	if idx.End <= idx.Frags[4].O {
		t.Fatalf("end %d not past last moof %d", idx.End, idx.Frags[4].O)
	}
}

func TestParseWalkFallbackNoMfra(t *testing.T) {
	p, offs, _ := buildFMP4(t, 4, false)
	idx, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Frags) != 4 {
		t.Fatalf("frags: %d", len(idx.Frags))
	}
	for i, fr := range idx.Frags {
		if fr.O != offs[i] {
			t.Fatalf("frag %d offset %d want %d", i, fr.O, offs[i])
		}
	}
}

func TestParseWalkSalvagesTruncatedTail(t *testing.T) {
	p, offs, _ := buildFMP4(t, 4, false)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// crash-cut: drop half of the last mdat
	if err := os.WriteFile(p, raw[:len(raw)-260], 0o600); err != nil {
		t.Fatal(err)
	}
	idx, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	// last fragment's mdat is truncated → walk stops there; first 3 fully intact + 4th moof seen
	if len(idx.Frags) < 3 {
		t.Fatalf("salvaged %d frags, want ≥3", len(idx.Frags))
	}
	if idx.Frags[0].O != offs[0] {
		t.Fatalf("first frag offset %d want %d", idx.Frags[0].O, offs[0])
	}
}

func TestClassicMP4Rejected(t *testing.T) {
	// moov WITHOUT mvex, then mdat - the classic layout must keep the plain src path
	ftyp := bx("ftyp", []byte("isom"), u32(512))
	moov := bx("moov", bx("mvhd", full(0, 0, make([]byte, 96))),
		trakBox(1, 90000, "vide", videoEntry("avc1", avcC())))
	file := append(append(append([]byte{}, ftyp...), moov...), bx("mdat", make([]byte, 64))...)
	p := t.TempDir() + "/classic.mp4"
	if err := os.WriteFile(p, file, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(p); !errors.Is(err, ErrNotFragmented) {
		t.Fatalf("want ErrNotFragmented, got %v", err)
	}
}

func TestUnsupportedCodecRefused(t *testing.T) {
	// unknown video format → no MSE mime → error (caller falls back to plain src)
	const ts = 90000
	ftyp := bx("ftyp", []byte("isom"), u32(512))
	moov := bx("moov", bx("mvhd", full(0, 0, make([]byte, 96))),
		trakBox(1, ts, "vide", videoEntry("zzzz")),
		bx("mvex", bx("trex", full(0, 0, u32(1), u32(1), u32(3000), u32(0), u32(0)))))
	file := append(append([]byte{}, ftyp...), moov...)
	file = append(file, moofBox(1, 1, 0, 60, 3000)...)
	file = append(file, bx("mdat", make([]byte, 64))...)
	p := t.TempDir() + "/badcodec.mp4"
	if err := os.WriteFile(p, file, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(p); err == nil {
		t.Fatal("unsupported codec must refuse (plain-src fallback)")
	}
}

// TestParseRealOBSRecording exercises a real multi-GB OBS fMP4 when present (dev machine only).
func TestParseRealOBSRecording(t *testing.T) {
	const real = `E:\media\recordings\2026-07-04 03-59-08.mp4`
	if _, err := os.Stat(real); err != nil {
		t.Skip("real OBS recording not present")
	}
	idx, err := Parse(real)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Frags) != 1867 {
		t.Fatalf("frags %d want 1867", len(idx.Frags))
	}
	if idx.Mime == "" || idx.Duration < 3600 || idx.Duration > 4000 {
		t.Fatalf("mime=%q dur=%f", idx.Mime, idx.Duration)
	}
	t.Logf("real file: mime=%s frags=%d dur=%.1fs init=%d", idx.Mime, len(idx.Frags), idx.Duration, idx.InitLen)
}
