package audio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Decoder is a seekable PCM source. Implementations normalize to interleaved float32 in [-1,1]
// at Format().SampleRate. Seek is sample-accurate and index-backed (O(1)/O(log n)) — never a
// full-file rescan. Not safe for concurrent use; the engine serializes access.
type Decoder interface {
	Format() Format
	// TotalFrames returns the frame count, or -1 only if genuinely unknowable.
	TotalFrames() int64
	// ReadFrames fills dst (interleaved, len must be a multiple of Channels) from the current
	// position and returns frames read. Returns (0, io.EOF) at end. A short read is not EOF.
	ReadFrames(dst []float32) (int, error)
	// SeekTo positions the read cursor to frame (0-based, clamped to [0,TotalFrames]). Named
	// SeekTo (not Seek) so the interface isn't mistaken for io.Seeker (go vet stdmethods).
	SeekTo(frame int64) error
	Close() error
}

// ErrUnsupported is returned by Open for a format the native path can't decode (e.g. AAC until a
// redistribution-clean codec is chosen); callers fall back to ffmpeg.
var ErrUnsupported = errors.New("audio: unsupported format (native decode unavailable)")

// Openable reports whether Open can natively decode path (by extension). AAC/M4A are excluded
// on purpose (see aac.go) so the caller keeps the ffmpeg fallback for them.
func Openable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".wave", ".aif", ".aiff", ".aifc", ".flac", ".mp3", ".ogg", ".oga":
		return true
	}
	return false
}

// Open decodes path with the native decoder for its type. It sniffs the leading magic bytes
// (robust to wrong extensions) and falls back to the extension. Returns ErrUnsupported for
// formats with no native decoder yet.
func Open(path string) (Decoder, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var magic [12]byte
	n, _ := io.ReadFull(f, magic[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	kind := sniff(magic[:n], path)
	switch kind {
	case "wav":
		return newWAVDecoder(f)
	case "aiff":
		return newAIFFDecoder(f)
	case "flac":
		return newFLACDecoder(f)
	case "mp3":
		return newMP3Decoder(f)
	case "ogg":
		return newVorbisDecoder(f)
	case "aac":
		_ = f.Close()
		return nil, fmt.Errorf("%w: aac/m4a (codec-license decision pending)", ErrUnsupported)
	default:
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, filepath.Ext(path))
	}
}

// sniff classifies by magic bytes, falling back to extension when magic is ambiguous (MP3 with
// no ID3, raw ADTS AAC).
func sniff(b []byte, path string) string {
	if len(b) >= 12 {
		switch {
		case string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE":
			return "wav"
		case string(b[0:4]) == "FORM" && (string(b[8:12]) == "AIFF" || string(b[8:12]) == "AIFC"):
			return "aiff"
		case string(b[0:4]) == "fLaC":
			return "flac"
		case string(b[0:4]) == "OggS":
			return "ogg"
		case string(b[0:3]) == "ID3":
			return "mp3"
		case string(b[0:4]) == "ftyp" || (len(b) >= 8 && string(b[4:8]) == "ftyp"):
			return "aac" // MP4/M4A container
		}
		// ADTS AAC sync (12-bit 0xFFF + layer bits 00) — checked before the MP3 frame sync,
		// which it would otherwise alias (both start 0xFF, top bits set). The 0xF6 mask keeps
		// real MPEG-1 Layer III frames (0xFA/0xFB) out.
		if b[0] == 0xFF && (b[1]&0xF6) == 0xF0 {
			return "aac"
		}
		// MPEG audio frame sync (0xFFEx) — MP3 with no ID3 tag.
		if b[0] == 0xFF && (b[1]&0xE0) == 0xE0 {
			return "mp3"
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".wave":
		return "wav"
	case ".aif", ".aiff", ".aifc":
		return "aiff"
	case ".flac":
		return "flac"
	case ".mp3":
		return "mp3"
	case ".ogg", ".oga":
		return "ogg"
	case ".aac", ".m4a", ".m4b", ".mp4":
		return "aac"
	}
	return ""
}
