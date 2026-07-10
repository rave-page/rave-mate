package tagwrite

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	flacvorbis "github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

// Vorbis-comment keys for the canonical fields. Key writes BOTH INITIALKEY (Mixed-In-Key /
// Serato convention) and KEY, label writes BOTH LABEL and ORGANIZATION (official field),
// for max reader compatibility; reads prefer the first key. Rating is special-cased
// (0-255 canonical ↔ 0-100 on disk).
var flacKeys = map[string][]string{
	FieldTitle:   {"TITLE"},
	FieldArtist:  {"ARTIST"},
	FieldAlbum:   {"ALBUM"},
	FieldGenre:   {"GENRE"},
	FieldComment: {"COMMENT"},
	FieldBPM:     {"BPM"},
	FieldKey:     {"INITIALKEY", "KEY"},
	FieldYear:    {"DATE"},
	FieldLabel:   {"LABEL", "ORGANIZATION"},
}

func readFLAC(path string) (Tags, error) {
	f, err := flac.ParseFile(path)
	if err != nil {
		return nil, err
	}
	cmt, _ := findFlacComment(f)
	t := Tags{}
	if cmt == nil {
		return t, nil
	}
	get := func(key string) string {
		if vs, err := cmt.Get(key); err == nil && len(vs) > 0 {
			return vs[0]
		}
		return ""
	}
	for field, keys := range flacKeys {
		for _, k := range keys {
			if v := get(k); v != "" {
				t[field] = v
				break
			}
		}
	}
	if v := get("RATING"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			t[FieldRating] = strconv.Itoa(ratingTo255(n))
		}
	}
	return t, nil
}

// ratingTo255 normalizes a FLAC RATING to canonical 0-255: values ≤100 are the
// conventional 0-100 scale (scaled up), >100 are already 0-255 (capped).
func ratingTo255(n int) int {
	if n > 100 {
		if n > 255 {
			return 255
		}
		return n
	}
	return (n*255 + 50) / 100
}

func writeFLAC(path string, vals Tags) error {
	f, err := flac.ParseFile(path)
	if err != nil {
		return err
	}
	cmt, idx := findFlacComment(f)
	if cmt == nil {
		cmt = flacvorbis.New()
		idx = -1
	}
	for field, v := range vals {
		if field == FieldRating {
			out := ""
			if v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || n > 255 {
					return fmt.Errorf("tagwrite: rating %q not in 0-255", v)
				}
				out = strconv.Itoa((n*100 + 127) / 255) // 0-255 → conventional 0-100
			}
			setVorbis(cmt, "RATING", out)
			continue
		}
		for _, k := range flacKeys[field] {
			setVorbis(cmt, k, v)
		}
	}
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

// findFlacComment returns the file's vorbis-comment block + its index, or (nil, -1).
func findFlacComment(f *flac.File) (*flacvorbis.MetaDataBlockVorbisComment, int) {
	for i, m := range f.Meta {
		if m.Type == flac.VorbisComment {
			if c, err := flacvorbis.ParseFromMetaDataBlock(*m); err == nil {
				return c, i
			}
		}
	}
	return nil, -1
}

// setVorbis replaces all entries for key (case-insensitive) with a single "KEY=v", or
// removes them when v=="" (so a revert can clear a field that wasn't there before).
func setVorbis(c *flacvorbis.MetaDataBlockVorbisComment, key, v string) {
	prefix := strings.ToUpper(key) + "="
	kept := c.Comments[:0]
	for _, e := range c.Comments {
		if !strings.HasPrefix(strings.ToUpper(e), prefix) {
			kept = append(kept, e)
		}
	}
	c.Comments = kept
	if v != "" {
		c.Comments = append(c.Comments, strings.ToUpper(key)+"="+v)
	}
}
