// Package tagwrite writes DJ analysis (BPM, key, genre, …) into audio file tags with
// precise frames - ID3v2 (TBPM/TKEY/…) for MP3, Vorbis comments (BPM/INITIALKEY/…) for
// FLAC. Writes are atomic (temp copy → fsync → rename over the original) so a crash can't
// corrupt the file, and only the named fields are touched (others are preserved). A field
// value of "" CLEARS that tag - which is how a revert restores a field that was absent
// before. M4A/WAV/AIFF tag-writing is not supported yet (Supported reports false).
package tagwrite

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Canonical field names (the analysis/catalog set we manage).
const (
	FieldTitle   = "title"
	FieldArtist  = "artist"
	FieldAlbum   = "album"
	FieldGenre   = "genre"
	FieldComment = "comment"
	FieldBPM     = "bpm"
	FieldKey     = "key"
	FieldYear    = "year"   // MP3 TDRC (v2.4) / TYER (v2.3), FLAC DATE
	FieldLabel   = "label"  // MP3 TPUB, FLAC LABEL (read fallback ORGANIZATION)
	FieldRating  = "rating" // canonical "0".."255" (Traktor scale); MP3 POPM, FLAC RATING (0-100 on disk)
	FieldDrops   = "drops"  // rave-mate drop markers: JSON []float64 ms; MP3 TXXX / FLAC RAVEMATE_DROPS
)

// Tags maps a canonical field → value. Presence means "set this field"; "" means "clear it".
// A field absent from the map is left untouched.
type Tags map[string]string

// Supported reports whether tag-writing is implemented for the file's format (mp3/flac).
func Supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".flac":
		return true
	}
	return false
}

// Read returns the current values of the canonical fields present in the file.
func Read(path string) (Tags, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return readMP3(path)
	case ".flac":
		return readFLAC(path)
	}
	return nil, fmt.Errorf("tagwrite: unsupported format %q", filepath.Ext(path))
}

// Write applies tags to the file (set/clear per-field, others preserved), atomically.
func Write(path string, t Tags) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return writeMP3(path, t)
	case ".flac":
		return writeFLAC(path, t)
	}
	return fmt.Errorf("tagwrite: unsupported format %q", filepath.Ext(path))
}

// tempSibling returns a temp path in the same directory (so os.Rename is atomic - same volume).
func tempSibling(path string) string { return path + ".rmtag.tmp" }

// copyFile copies src→dst (used to stage an MP3 for in-place id3v2 Save on the temp).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
