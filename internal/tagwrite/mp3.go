package tagwrite

import (
	"os"

	id3v2 "github.com/bogem/id3v2/v2"
)

// ID3v2.4 frame IDs for the canonical fields.
var mp3Frame = map[string]string{
	FieldTitle:  "TIT2",
	FieldArtist: "TPE1",
	FieldAlbum:  "TALB",
	FieldGenre:  "TCON",
	FieldBPM:    "TBPM",
	FieldKey:    "TKEY",
}

func readMP3(path string) (Tags, error) {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tag.Close() }()
	t := Tags{}
	for field, id := range mp3Frame {
		if f := tag.GetTextFrame(id); f.Text != "" {
			t[field] = f.Text
		}
	}
	if frames := tag.GetFrames("COMM"); len(frames) > 0 {
		if c, ok := frames[0].(id3v2.CommentFrame); ok && c.Text != "" {
			t[FieldComment] = c.Text
		}
	}
	return t, nil
}

func writeMP3(path string, vals Tags) error {
	// Stage on a temp copy so the original is replaced atomically (id3v2.Save rewrites in
	// place; we never modify the user's file until the final rename).
	tmp := tempSibling(path)
	if err := copyFile(path, tmp); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()

	tag, err := id3v2.Open(tmp, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	enc := id3v2.EncodingUTF8
	for field, v := range vals {
		if field == FieldComment {
			tag.DeleteFrames("COMM")
			if v != "" {
				tag.AddCommentFrame(id3v2.CommentFrame{Encoding: enc, Language: "eng", Text: v})
			}
			continue
		}
		id, ok := mp3Frame[field]
		if !ok {
			continue
		}
		tag.DeleteFrames(id) // remove existing so set/clear is exact (no duplicate frames)
		if v != "" {
			tag.AddTextFrame(id, enc, v)
		}
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
