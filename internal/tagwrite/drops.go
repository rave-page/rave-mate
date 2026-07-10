package tagwrite

// Drop markers in the FILE (rave-mate enrichment tag): mp3 = TXXX frame with
// description "RAVEMATE_DROPS", flac = vorbis comment RAVEMATE_DROPS. Value is the
// JSON []float64 (ms) the library stores - one format everywhere, survives re-import
// on any machine. Kept out of the canonical Tags map: drops are not a text field a
// user edits, they're structured data with their own read/write pair.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	id3v2 "github.com/bogem/id3v2/v2"
	flacvorbis "github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

const dropsKey = "RAVEMATE_DROPS"

// ReadDrops returns the drop markers stored in the file (nil when none).
func ReadDrops(path string) ([]float64, error) {
	var raw string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		tag, err := id3v2.Open(path, id3v2.Options{Parse: true, ParseFrames: []string{"TXXX"}})
		if err != nil {
			return nil, err
		}
		defer func() { _ = tag.Close() }()
		for _, f := range tag.GetFrames("TXXX") {
			if udf, ok := f.(id3v2.UserDefinedTextFrame); ok && udf.Description == dropsKey {
				raw = udf.Value
				break
			}
		}
	case ".flac":
		f, err := flac.ParseFile(path)
		if err != nil {
			return nil, err
		}
		if cmt, _ := findFlacComment(f); cmt != nil {
			if vs, err := cmt.Get(dropsKey); err == nil && len(vs) > 0 {
				raw = vs[0]
			}
		}
	default:
		return nil, fmt.Errorf("tagwrite: unsupported format %q", filepath.Ext(path))
	}
	if raw == "" {
		return nil, nil
	}
	var out []float64
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("tagwrite: %s tag corrupt: %w", dropsKey, err)
	}
	return out, nil
}

// WriteDrops stores drop markers in the file (empty = remove the tag). Atomic like
// every other tagwrite write.
func WriteDrops(path string, drops []float64) error {
	val := ""
	if len(drops) > 0 {
		raw, err := json.Marshal(drops)
		if err != nil {
			return err
		}
		val = string(raw)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return writeDropsMP3(path, val)
	case ".flac":
		return writeDropsFLAC(path, val)
	default:
		return fmt.Errorf("tagwrite: unsupported format %q", filepath.Ext(path))
	}
}

func writeDropsFLAC(path, val string) error {
	f, err := flac.ParseFile(path)
	if err != nil {
		return err
	}
	cmt, idx := findFlacComment(f)
	if cmt == nil {
		cmt = flacvorbis.New()
		idx = -1
	}
	setVorbis(cmt, dropsKey, val)
	block := cmt.Marshal()
	if idx >= 0 {
		f.Meta[idx] = &block
	} else {
		f.Meta = append(f.Meta, &block)
	}
	tmp := tempSibling(path)
	if err := f.Save(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func writeDropsMP3(path, val string) error {
	tmp := tempSibling(path)
	if err := copyFile(path, tmp); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	tag, err := id3v2.Open(tmp, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	kept := tag.GetFrames("TXXX")[:0:0]
	for _, f := range tag.GetFrames("TXXX") {
		if udf, ok := f.(id3v2.UserDefinedTextFrame); !ok || udf.Description != dropsKey {
			kept = append(kept, f)
		}
	}
	tag.DeleteFrames("TXXX")
	for _, f := range kept {
		if udf, ok := f.(id3v2.UserDefinedTextFrame); ok {
			tag.AddUserDefinedTextFrame(udf)
		}
	}
	if val != "" {
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
			Encoding: id3v2.EncodingUTF8, Description: dropsKey, Value: val})
	}
	if err := tag.Save(); err != nil {
		_ = tag.Close()
		return err
	}
	if err := tag.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
