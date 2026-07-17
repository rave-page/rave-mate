package audio

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/mewkiz/flac/frame"
)

// flacDecoder decodes FLAC via mewkiz's frame parser, with its own metadata parse and its own
// O(log n) seek. mewkiz Stream.Seek needs a SEEKTABLE; for seektable-less files (EVERY direct
// capture - ffmpeg's flac muxer never writes one) it silently falls back to makeSeekTable(),
// which decodes the ENTIRE file before the first seek returns (~1.2 s per 12 MiB → ~47 s on an
// hour-long 485 MB set, paid again on every fresh open). Instead SeekTo binary-searches the
// file directly: FLAC frame headers carry their own absolute sample number and a CRC-8, so a
// probe is "scan a few KB to the next validated header, read its sample number" - no index, no
// cache, no first-seek scan, any FLAC. Subframe samples are int32 at the stream bit depth;
// scaled to float32 [-1,1].
type flacDecoder struct {
	f         io.ReadSeekCloser
	br        *bufio.Reader // frame parser reader over f (reset after every reposition)
	format    Format
	total     int64
	bps       uint8           // STREAMINFO bits per sample (frame-header sanity check)
	fixedBS   int             // STREAMINFO min==max blocksize (0 = variable-blocksize stream)
	seekPts   []flacSeekPoint // SEEKTABLE anchors when the file has one (recorder finalize writes it)
	scale     float32
	dataStart int64 // first audio frame (past fLaC magic + metadata blocks)
	fileSize  int64
	cur       int64

	buf []float32 // decoded interleaved f32 from the current frame
	off int       // consumed offset into buf (samples)
}

// flacSeekWindow is the binary-search terminal span: below this, decode linearly from the
// last known frame start. Also the probe scan span - must comfortably exceed the largest
// compressed frame (~64 KB for 32-bit 192 kHz worst cases; typical is 4-16 KB). var so the
// bounded-read regression test can shrink it to exercise the search on a small fixture.
var flacSeekWindow int64 = 512 << 10

// flacHdrMax bounds a frame header (sync+header+CRC ≤ ~40 bytes); probes keep this margin.
const flacHdrMax = 64

func newFLACDecoder(r io.ReadSeekCloser) (Decoder, error) {
	d := &flacDecoder{f: r}
	if err := d.parseMeta(); err != nil {
		_ = r.Close()
		return nil, err
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	d.fileSize = end
	if _, err := r.Seek(d.dataStart, io.SeekStart); err != nil {
		_ = r.Close()
		return nil, err
	}
	d.br = bufio.NewReaderSize(r, 64<<10)
	if d.total <= 0 {
		d.total = -1
	}
	return d, nil
}

// parseMeta reads the fLaC magic + STREAMINFO and skips the remaining metadata blocks, leaving
// dataStart at the first audio frame.
func (d *flacDecoder) parseMeta() error {
	var magic [4]byte
	if _, err := io.ReadFull(d.f, magic[:]); err != nil {
		return err
	}
	if string(magic[:]) != "fLaC" {
		return errors.New("flac: bad magic")
	}
	pos := int64(4)
	last := false
	first := true
	for !last {
		var hdr [4]byte
		if _, err := io.ReadFull(d.f, hdr[:]); err != nil {
			return err
		}
		last = hdr[0]&0x80 != 0
		btype := hdr[0] & 0x7f
		blen := int64(hdr[1])<<16 | int64(hdr[2])<<8 | int64(hdr[3])
		pos += 4
		if first {
			if btype != 0 || blen != 34 {
				return errors.New("flac: STREAMINFO not first")
			}
			var si [34]byte
			if _, err := io.ReadFull(d.f, si[:]); err != nil {
				return err
			}
			minBS := int(binary.BigEndian.Uint16(si[0:2]))
			maxBS := int(binary.BigEndian.Uint16(si[2:4]))
			if minBS == maxBS {
				d.fixedBS = minBS
			}
			rate := int(si[10])<<12 | int(si[11])<<4 | int(si[12])>>4
			ch := int(si[12]>>1&0x7) + 1
			d.bps = ((si[12]&1)<<4 | si[13]>>4) + 1
			d.total = int64(si[13]&0x0F)<<32 | int64(binary.BigEndian.Uint32(si[14:18]))
			if rate <= 0 || ch <= 0 || d.bps == 0 {
				return errors.New("flac: bad STREAMINFO")
			}
			d.format = Format{SampleRate: rate, Channels: ch}
			d.scale = 1.0 / float32(int64(1)<<(d.bps-1))
			first = false
		} else if btype == 3 && blen%18 == 0 && blen/18 <= 1<<16 { // SEEKTABLE → seed seek anchors
			raw := make([]byte, blen)
			if _, err := io.ReadFull(d.f, raw); err != nil {
				return err
			}
			for i := 0; i+18 <= len(raw); i += 18 {
				smp := binary.BigEndian.Uint64(raw[i:])
				if smp == 0xFFFFFFFFFFFFFFFF { // placeholder point
					continue
				}
				d.seekPts = append(d.seekPts, flacSeekPoint{
					sample: int64(smp), off: int64(binary.BigEndian.Uint64(raw[i+8:])),
				})
			}
		} else if _, err := d.f.Seek(blen, io.SeekCurrent); err != nil {
			return err
		}
		pos += blen
	}
	d.dataStart = pos
	return nil
}

// flacSeekPoint is one SEEKTABLE entry: first sample of a frame + its offset from dataStart.
type flacSeekPoint struct {
	sample, off int64
}

func (d *flacDecoder) Format() Format     { return d.format }
func (d *flacDecoder) TotalFrames() int64 { return d.total }
func (d *flacDecoder) Close() error       { return d.f.Close() }

// fill decodes the next FLAC frame into buf (interleaved f32). Returns io.EOF at stream end.
func (d *flacDecoder) fill() error {
	f, err := frame.Parse(d.br)
	if err != nil {
		if err == io.ErrUnexpectedEOF { // crash-cut capture: trailing partial frame = end of audio
			return io.EOF
		}
		return err
	}
	d.load(f)
	return nil
}

// load exposes a parsed frame's samples as the current buffer.
func (d *flacDecoder) load(f *frame.Frame) {
	ch := d.format.Channels
	bs := int(f.BlockSize)
	if cap(d.buf) < bs*ch {
		d.buf = make([]float32, bs*ch)
	}
	d.buf = d.buf[:bs*ch]
	for i := 0; i < bs; i++ {
		for c := 0; c < ch; c++ {
			d.buf[i*ch+c] = float32(f.Subframes[c].Samples[i]) * d.scale
		}
	}
	d.off = 0
}

func (d *flacDecoder) ReadFrames(dst []float32) (int, error) {
	ch := d.format.Channels
	want := len(dst) / ch
	wrote := 0
	for wrote < want {
		if d.off >= len(d.buf) {
			if err := d.fill(); err != nil {
				if err == io.EOF {
					break
				}
				if wrote > 0 {
					break
				}
				return 0, err
			}
		}
		avail := (len(d.buf) - d.off) / ch
		take := want - wrote
		if take > avail {
			take = avail
		}
		copy(dst[wrote*ch:], d.buf[d.off:d.off+take*ch])
		d.off += take * ch
		wrote += take
	}
	d.cur += int64(wrote)
	if wrote == 0 {
		return 0, io.EOF
	}
	return wrote, nil
}

// SeekTo binary-searches the file for the frame containing the target sample, then decodes
// forward to the exact sample. O(log n) probes of ≤512 KB each - never a full-file scan.
func (d *flacDecoder) SeekTo(target int64) error {
	if target < 0 {
		target = 0
	}
	if d.total > 0 && target > d.total {
		target = d.total
	}
	lo, loSmp := d.dataStart, int64(0)
	if target > 0 {
		hi := d.fileSize
		// SEEKTABLE anchors (when present) collapse most of the search up front; the probe
		// loop below still refines within the inter-point gap.
		if n := len(d.seekPts); n > 0 {
			i, j := 0, n-1
			for i < j {
				m := (i + j + 1) / 2
				if d.seekPts[m].sample <= target {
					i = m
				} else {
					j = m - 1
				}
			}
			if p := d.seekPts[i]; p.sample <= target && d.dataStart+p.off > lo && d.dataStart+p.off < d.fileSize {
				lo, loSmp = d.dataStart+p.off, p.sample
			}
			if i+1 < n {
				if h := d.dataStart + d.seekPts[i+1].off; h > lo && h < hi {
					hi = h
				}
			}
		}
		for hi-lo > flacSeekWindow {
			mid := lo + (hi-lo)/2
			smp, off, ok := d.syncScan(mid, minI64(mid+flacSeekWindow, hi))
			if !ok { // no valid frame in the window (tail junk / crash-cut) → search left
				hi = mid
				continue
			}
			if smp <= target {
				if off <= lo {
					break // no forward progress (window straddles the target frame)
				}
				lo, loSmp = off, smp
			} else {
				hi = mid
			}
		}
	}
	if err := d.reposition(lo); err != nil {
		return err
	}
	d.cur = loSmp
	// Decode forward frame-by-frame until the one containing target; land inside it. Bounded by
	// the terminal window (≤512 KB of compressed audio), not the file. Position is tracked as a
	// running sum from the validated anchor - NOT via f.SampleNumber(), which is wrong for the
	// final short frame of a fixed-blocksize stream (frame number × THIS frame's short size).
	pos := loSmp
	for {
		f, err := frame.Parse(d.br)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF { // seek to/past effective end
				d.buf, d.off, d.cur = d.buf[:0], 0, target
				return nil
			}
			return fmt.Errorf("flac seek decode: %w", err)
		}
		end := pos + int64(f.BlockSize)
		if end > target {
			d.load(f)
			skip := int(target - pos)
			if skip < 0 { // target precedes this frame (gap): land at frame start
				skip = 0
			}
			d.off = skip * d.format.Channels
			d.cur = pos + int64(skip)
			return nil
		}
		pos = end
	}
}

// reposition points the frame reader at an absolute file offset.
func (d *flacDecoder) reposition(off int64) error {
	if _, err := d.f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	d.br.Reset(d.f)
	d.buf, d.off = d.buf[:0], 0
	return nil
}

// syncScan finds the first valid frame header in file span [from,to): sync code + full header
// parse (CRC-8 verified by mewkiz) + STREAMINFO coherence. Returns its sample number + offset.
// Typical frames are 4-16 KB, so a 64 KB sub-scan almost always hits; the full span is read
// only on a miss. Moves d.f freely - callers reposition() before decoding.
func (d *flacDecoder) syncScan(from, to int64) (sample, off int64, ok bool) {
	if sub := from + 64<<10; sub < to {
		if s, o, found := d.scanSpan(from, sub); found {
			return s, o, true
		}
		// header may straddle the sub-window edge; rescan overlaps by flacHdrMax
		from = sub - flacHdrMax
	}
	return d.scanSpan(from, to)
}

func (d *flacDecoder) scanSpan(from, to int64) (sample, off int64, ok bool) {
	if to <= from+flacHdrMax {
		return 0, 0, false
	}
	if _, err := d.f.Seek(from, io.SeekStart); err != nil {
		return 0, 0, false
	}
	w := make([]byte, to-from)
	n, _ := io.ReadFull(d.f, w) // short read at EOF keeps what arrived
	w = w[:n]
	for i := 0; i+flacHdrMax <= len(w); i++ {
		if w[i] != 0xFF || w[i+1]&0xFC != 0xF8 {
			continue
		}
		f, err := frame.New(bytes.NewReader(w[i:]))
		if err != nil || !d.plausible(f) {
			continue
		}
		return int64(f.SampleNumber()), from + int64(i), true
	}
	return 0, 0, false
}

// plausible cross-checks a candidate header against STREAMINFO (CRC-8 alone is 1/256). Rejects
// a fixed-blocksize stream's final short frame: its SampleNumber() is unreliable (see SeekTo),
// and skipping it just anchors the search one frame earlier.
func (d *flacDecoder) plausible(f *frame.Frame) bool {
	if f.Channels.Count() != d.format.Channels || f.BlockSize == 0 {
		return false
	}
	if d.fixedBS > 0 && f.HasFixedBlockSize && int(f.BlockSize) != d.fixedBS {
		return false
	}
	if f.SampleRate != 0 && int(f.SampleRate) != d.format.SampleRate {
		return false
	}
	if f.BitsPerSample != 0 && f.BitsPerSample != d.bps {
		return false
	}
	s := int64(f.SampleNumber())
	return d.total <= 0 || s <= d.total
}

func minI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
