package tagwrite

// Drop markers (rave-mate enrichment) as a canonical field: FieldDrops carries the
// JSON []float64 (ms) - mp3 = TXXX frame "RAVEMATE_DROPS", flac = vorbis comment
// RAVEMATE_DROPS. One wire format everywhere; survives re-import on any machine.
// ReadDrops/WriteDrops are the typed convenience pair over Read/Write.

import (
	"encoding/json"
	"fmt"

	id3v2 "github.com/bogem/id3v2/v2"
)

const dropsKey = "RAVEMATE_DROPS"

// ReadDrops returns the drop markers stored in the file (nil when none).
func ReadDrops(path string) ([]float64, error) {
	t, err := Read(path)
	if err != nil {
		return nil, err
	}
	raw := t[FieldDrops]
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
	return Write(path, Tags{FieldDrops: val})
}

// setDropsTXXX replaces OUR TXXX drops frame ("" = remove), preserving every foreign
// TXXX frame (Serato/MIK/etc. also use TXXX - id3v2 deletes by frame ID only).
func setDropsTXXX(tag *id3v2.Tag, val string) {
	frames := tag.GetFrames("TXXX")
	kept := make([]id3v2.UserDefinedTextFrame, 0, len(frames))
	for _, f := range frames {
		if udf, ok := f.(id3v2.UserDefinedTextFrame); ok && udf.Description != dropsKey {
			kept = append(kept, udf)
		}
	}
	tag.DeleteFrames("TXXX")
	for _, udf := range kept {
		tag.AddUserDefinedTextFrame(udf)
	}
	if val != "" {
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
			Encoding: id3v2.EncodingUTF8, Description: dropsKey, Value: val})
	}
}
