package tagwrite

import (
	"os"
	"strings"

	flacvorbis "github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

// Vorbis-comment keys for the canonical fields. Key writes BOTH INITIALKEY (Mixed-In-Key /
// Serato convention) and KEY for max reader compatibility.
var flacKeys = map[string][]string{
	FieldTitle:   {"TITLE"},
	FieldArtist:  {"ARTIST"},
	FieldAlbum:   {"ALBUM"},
	FieldGenre:   {"GENRE"},
	FieldComment: {"COMMENT"},
	FieldBPM:     {"BPM"},
	FieldKey:     {"INITIALKEY", "KEY"},
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
	return t, nil
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
