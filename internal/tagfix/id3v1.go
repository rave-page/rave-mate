package tagfix

import (
	"bytes"
	"io"
	"os"
	"strings"
)

// id3v1 holds the decoded 128-byte ID3v1 trailer fields (comment excluded - v1 comments
// are 28-30 byte stubs, not worth promoting).
type id3v1 struct {
	title, artist, album, year, genre string
}

// v1Presence detects an ID3v2 header ("ID3" at offset 0) and an ID3v1 trailer ("TAG" at
// EOF-128), parsing the trailer. Pure stdlib.
func v1Presence(path string) (hasV2, hasV1 bool, v1 id3v1, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, id3v1{}, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return false, false, id3v1{}, err
	}
	head := make([]byte, 3)
	if n, _ := io.ReadFull(f, head); n == 3 && bytes.Equal(head, []byte("ID3")) {
		hasV2 = true
	}
	if st.Size() < 128 {
		return hasV2, false, id3v1{}, nil
	}
	trailer := make([]byte, 128)
	if _, err := f.ReadAt(trailer, st.Size()-128); err != nil {
		return hasV2, false, id3v1{}, err
	}
	if !bytes.Equal(trailer[:3], []byte("TAG")) {
		return hasV2, false, id3v1{}, nil
	}
	v1 = id3v1{
		title:  v1Text(trailer[3:33]),
		artist: v1Text(trailer[33:63]),
		album:  v1Text(trailer[63:93]),
		year:   v1Text(trailer[93:97]),
	}
	if g := int(trailer[127]); g < len(id3v1Genres) {
		v1.genre = id3v1Genres[g]
	}
	return hasV2, true, v1, nil
}

// v1Text decodes a fixed-width ID3v1 field: cut at first NUL, latin1 bytes → runes,
// trim padding.
func v1Text(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	rs := make([]rune, 0, len(b))
	for _, c := range b {
		rs = append(rs, rune(c))
	}
	return strings.TrimSpace(string(rs))
}

// id3v1Genres: standard IDs 0-79 + Winamp extension 80-147 (133/134 omitted). A genre
// byte beyond the table (or an omitted slot) proposes no genre.
var id3v1Genres = []string{
	"Blues", "Classic Rock", "Country", "Dance", "Disco", "Funk", "Grunge", "Hip-Hop",
	"Jazz", "Metal", "New Age", "Oldies", "Other", "Pop", "R&B", "Rap",
	"Reggae", "Rock", "Techno", "Industrial", "Alternative", "Ska", "Death Metal", "Pranks",
	"Soundtrack", "Euro-Techno", "Ambient", "Trip-Hop", "Vocal", "Jazz+Funk", "Fusion", "Trance",
	"Classical", "Instrumental", "Acid", "House", "Game", "Sound Clip", "Gospel", "Noise",
	"AlternRock", "Bass", "Soul", "Punk", "Space", "Meditative", "Instrumental Pop", "Instrumental Rock",
	"Ethnic", "Gothic", "Darkwave", "Techno-Industrial", "Electronic", "Pop-Folk", "Eurodance", "Dream",
	"Southern Rock", "Comedy", "Cult", "Gangsta", "Top 40", "Christian Rap", "Pop/Funk", "Jungle",
	"Native American", "Cabaret", "New Wave", "Psychadelic", "Rave", "Showtunes", "Trailer", "Lo-Fi",
	"Tribal", "Acid Punk", "Acid Jazz", "Polka", "Retro", "Musical", "Rock & Roll", "Hard Rock",
	"Folk", "Folk-Rock", "National Folk", "Swing", "Fast Fusion", "Bebob", "Latin", "Revival",
	"Celtic", "Bluegrass", "Avantgarde", "Gothic Rock", "Progressive Rock", "Psychedelic Rock", "Symphonic Rock", "Slow Rock",
	"Big Band", "Chorus", "Easy Listening", "Acoustic", "Humour", "Speech", "Chanson", "Opera",
	"Chamber Music", "Sonata", "Symphony", "Booty Bass", "Primus", "Porn Groove", "Satire", "Slow Jam",
	"Club", "Tango", "Samba", "Folklore", "Ballad", "Power Ballad", "Rhythmic Soul", "Freestyle",
	"Duet", "Punk Rock", "Drum Solo", "A capella", "Euro-House", "Dance Hall", "Goa", "Drum & Bass",
	"Club-House", "Hardcore", "Terror", "Indie", "BritPop", "", "", "Beat",
	"Christian Gangsta Rap", "Heavy Metal", "Black Metal", "Crossover", "Contemporary Christian", "Christian Rock", "Merengue", "Salsa",
	"Thrash Metal", "Anime", "JPop", "Synthpop",
}
