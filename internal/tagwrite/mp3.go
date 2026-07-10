package tagwrite

import (
	"fmt"
	"math/big"
	"os"
	"strconv"

	id3v2 "github.com/bogem/id3v2/v2"
)

// popmEmail identifies our POPM frame (per-writer rating slot; foreign frames preserved).
const popmEmail = "rave-mate"

// ID3v2.4 frame IDs for the plain-text canonical fields. Year + rating are special-cased
// (version-dependent frame / POPM binary frame).
var mp3Frame = map[string]string{
	FieldTitle:  "TIT2",
	FieldArtist: "TPE1",
	FieldAlbum:  "TALB",
	FieldGenre:  "TCON",
	FieldBPM:    "TBPM",
	FieldKey:    "TKEY",
	FieldLabel:  "TPUB",
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
	// Year: TDRC (v2.4 recording time) wins, TYER (v2.3) fallback.
	if f := tag.GetTextFrame("TDRC"); f.Text != "" {
		t[FieldYear] = f.Text
	} else if f := tag.GetTextFrame("TYER"); f.Text != "" {
		t[FieldYear] = f.Text
	}
	if r, ok := popmRating(tag); ok {
		t[FieldRating] = strconv.Itoa(int(r))
	}
	for _, f := range tag.GetFrames("TXXX") {
		if udf, ok := f.(id3v2.UserDefinedTextFrame); ok && udf.Description == dropsKey && udf.Value != "" {
			t[FieldDrops] = udf.Value
			break
		}
	}
	if frames := tag.GetFrames("COMM"); len(frames) > 0 {
		if c, ok := frames[0].(id3v2.CommentFrame); ok && c.Text != "" {
			t[FieldComment] = c.Text
		}
	}
	return t, nil
}

// popmRating returns the POPM rating - ours (popmEmail) preferred, else the first frame.
func popmRating(tag *id3v2.Tag) (uint8, bool) {
	frames := tag.GetFrames("POPM")
	var first *id3v2.PopularimeterFrame
	for i := range frames {
		p, ok := frames[i].(id3v2.PopularimeterFrame)
		if !ok {
			continue
		}
		if p.Email == popmEmail {
			return p.Rating, true
		}
		if first == nil {
			first = &p
		}
	}
	if first != nil {
		return first.Rating, true
	}
	return 0, false
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
		switch field {
		case FieldComment:
			tag.DeleteFrames("COMM")
			if v != "" {
				tag.AddCommentFrame(id3v2.CommentFrame{Encoding: enc, Language: "eng", Text: v})
			}
			continue
		case FieldYear:
			tag.DeleteFrames("TDRC")
			tag.DeleteFrames("TYER")
			if v != "" {
				id := "TDRC"
				if tag.Version() == 3 {
					id = "TYER"
				}
				tag.AddTextFrame(id, enc, v)
			}
			continue
		case FieldRating:
			if err := setPOPM(tag, v); err != nil {
				_ = tag.Close()
				return err
			}
			continue
		case FieldDrops:
			setDropsTXXX(tag, v)
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

// setPOPM replaces our POPM frame (v = canonical "0".."255"; "" clears), preserving
// other writers' POPM frames.
func setPOPM(tag *id3v2.Tag, v string) error {
	var keep []id3v2.PopularimeterFrame
	for _, fr := range tag.GetFrames("POPM") {
		if p, ok := fr.(id3v2.PopularimeterFrame); ok && p.Email != popmEmail {
			keep = append(keep, p)
		}
	}
	tag.DeleteFrames("POPM")
	for _, p := range keep {
		tag.AddFrame("POPM", p)
	}
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 255 {
		return fmt.Errorf("tagwrite: rating %q not in 0-255", v)
	}
	tag.AddFrame("POPM", id3v2.PopularimeterFrame{Email: popmEmail, Rating: uint8(n), Counter: big.NewInt(0)})
	return nil
}
