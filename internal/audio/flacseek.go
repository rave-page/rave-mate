package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"os"
	"time"

	"github.com/mewkiz/flac/frame"
)

// FLACEnsureSeekTable retrofits a real SEEKTABLE into a seektable-less FLAC by rewriting its
// PADDING block in place - ffmpeg's flac muxer (every direct/broadcast capture) reserves 8 KiB
// of padding but never writes seek points, so first-load seeking in ANY player degrades. The
// rewrite touches ONLY the metadata region before the first audio frame (one WriteAt), then
// restores the file's mtime so mtime-keyed analysis caches (peaks/loudness/streams) stay
// valid. Returns wrote=false with nil error when the file already has a table, isn't FLAC, or
// its padding is too small to host one (the decoder's binary-search seek covers those).
func FLACEnsureSeekTable(path string) (wrote bool, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	lay, err := flacLayout(f)
	if err != nil || lay.hasSeekTable || lay.padLen < 4+2*18 {
		return false, err // not FLAC / already indexed / padding can't host a useful table
	}

	pts, err := flacScanFrames(f, lay, fi.Size())
	if err != nil {
		return false, err
	}
	if len(pts) == 0 {
		return false, errors.New("flac seektable: no frames found")
	}
	// fit: (4+18n) SEEKTABLE + (4+rest) PADDING must equal the old 4+padLen exactly, so
	// n ≤ (padLen-4)/18; thin evenly when the scan found more.
	maxN := (lay.padLen - 4) / 18
	if maxN < 1 {
		return false, nil
	}
	if len(pts) > maxN {
		th := make([]flacScanPoint, 0, maxN)
		for i := 0; i < maxN; i++ {
			th = append(th, pts[i*len(pts)/maxN])
		}
		pts = th
	}

	// splice [padOff, padOff+4+padLen) → SEEKTABLE(4+18n) + PADDING(4+rest)
	tblLen := len(pts) * 18
	rest := lay.padLen - tblLen - 4
	if rest < 0 {
		return false, nil
	}
	region := make([]byte, 4+lay.padLen)
	region[0] = 3 // SEEKTABLE
	putLen24(region[1:4], tblLen)
	for i, p := range pts {
		binary.BigEndian.PutUint64(region[4+i*18:], uint64(p.sample))
		binary.BigEndian.PutUint64(region[4+i*18+8:], uint64(p.off))
		binary.BigEndian.PutUint16(region[4+i*18+16:], uint16(p.nsamples))
	}
	pad := region[4+tblLen:]
	pad[0] = 1 // PADDING
	if lay.padWasLast {
		pad[0] |= 0x80
	}
	putLen24(pad[1:4], rest)
	if _, err := f.WriteAt(region, lay.padOff); err != nil {
		return false, fmt.Errorf("flac seektable write: %w", err)
	}
	// mtime restore: content past dataStart is untouched, so mtime-keyed caches stay valid
	_ = f.Close()
	_ = os.Chtimes(path, time.Now(), fi.ModTime())
	return true, nil
}

// flacMetaLayout is the metadata geometry FLACEnsureSeekTable needs.
type flacMetaLayout struct {
	streamInfo   [34]byte
	hasSeekTable bool
	padOff       int64 // PADDING block header offset (0 = none found)
	padLen       int   // PADDING body length
	padWasLast   bool
	dataStart    int64
}

// flacLayout walks the metadata blocks. err only on real I/O/parse trouble; a non-FLAC file
// returns an error too (callers treat it as "nothing to do" via the wrote=false path).
func flacLayout(f *os.File) (flacMetaLayout, error) {
	var lay flacMetaLayout
	var magic [4]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "fLaC" {
		return lay, errors.New("not a flac file")
	}
	pos := int64(4)
	for {
		var hdr [4]byte
		if _, err := f.ReadAt(hdr[:], pos); err != nil {
			return lay, err
		}
		last := hdr[0]&0x80 != 0
		btype := hdr[0] & 0x7f
		blen := int64(hdr[1])<<16 | int64(hdr[2])<<8 | int64(hdr[3])
		switch btype {
		case 0:
			if _, err := f.ReadAt(lay.streamInfo[:], pos+4); err != nil {
				return lay, err
			}
		case 3:
			lay.hasSeekTable = true
		case 1:
			if int(blen) > lay.padLen { // host the table in the largest padding
				lay.padOff, lay.padLen, lay.padWasLast = pos, int(blen), last
			}
		}
		pos += 4 + blen
		if last {
			break
		}
	}
	lay.dataStart = pos
	return lay, nil
}

// flacScanPoint is one candidate SEEKTABLE entry (sample, offset from first frame, blocksize).
type flacScanPoint struct {
	sample, off int64
	nsamples    int
}

// flacScanFrames sequentially sync-scans the audio region collecting one seek point per
// ~flacSeekPointEverySec seconds. Header-only validation (CRC-8 + STREAMINFO coherence) - no
// sample decode, so an hour-long 485 MB capture scans in a couple of seconds, once ever.
func flacScanFrames(f *os.File, lay flacMetaLayout, fileSize int64) ([]flacScanPoint, error) {
	si := lay.streamInfo
	rate := int(si[10])<<12 | int(si[11])<<4 | int(si[12])>>4
	ch := int(si[12]>>1&0x7) + 1
	bps := ((si[12]&1)<<4 | si[13]>>4) + 1
	fixedBS := 0
	if minBS, maxBS := int(binary.BigEndian.Uint16(si[0:2])), int(binary.BigEndian.Uint16(si[2:4])); minBS == maxBS {
		fixedBS = minBS
	}
	total := int64(si[13]&0x0F)<<32 | int64(binary.BigEndian.Uint32(si[14:18]))
	every := int64(rate) * flacSeekPointEverySec

	var pts []flacScanPoint
	const chunk = 4 << 20
	buf := make([]byte, chunk+flacHdrMax)
	nextAt := int64(0) // record the next validated frame at/after this sample
	lastSmp := int64(-1)
	for off := lay.dataStart; off < fileSize; off += chunk {
		n, _ := f.ReadAt(buf, off)
		w := buf[:n]
		limit := len(w) - flacHdrMax
		if off+int64(len(w)) >= fileSize { // final chunk: scan to the true end
			limit = len(w) - 16
		}
		for i := 0; i < limit; i++ {
			if w[i] != 0xFF || w[i+1]&0xFC != 0xF8 {
				continue
			}
			fr, err := frame.New(bytes.NewReader(w[i:]))
			if err != nil || fr.Channels.Count() != ch || fr.BlockSize == 0 {
				continue
			}
			if fr.SampleRate != 0 && int(fr.SampleRate) != rate {
				continue
			}
			if fr.BitsPerSample != 0 && fr.BitsPerSample != bps {
				continue
			}
			if fixedBS > 0 && fr.HasFixedBlockSize && int(fr.BlockSize) != fixedBS {
				continue // final short frame: SampleNumber() unreliable
			}
			smp := int64(fr.SampleNumber())
			if smp <= lastSmp || (total > 0 && smp > total) {
				continue // non-monotonic = false sync
			}
			lastSmp = smp
			if smp >= nextAt {
				pts = append(pts, flacScanPoint{smp, off + int64(i) - lay.dataStart, int(fr.BlockSize)})
				nextAt = smp + every
			}
			i += flacMinFrameHop - 1 // frames are never tiny; skip ahead past this header
		}
	}
	return pts, nil
}

const (
	// flacSeekPointEverySec spaces seek points; thinned further if the padding is small.
	flacSeekPointEverySec = 5
	// flacMinFrameHop skips past a validated header before rescanning (min plausible frame).
	flacMinFrameHop = 64
)

func putLen24(dst []byte, v int) {
	dst[0] = byte(v >> 16)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v)
}
