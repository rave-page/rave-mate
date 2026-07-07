package musiclib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const playlistNML = `<?xml version="1.0" encoding="UTF-8" standalone="no" ?>
<NML VERSION="20"><HEAD COMPANY="www.native-instruments.com" PROGRAM="Traktor"></HEAD>
<COLLECTION ENTRIES="1">
 <ENTRY TITLE="A"><LOCATION DIR="/:Music/:" FILE="a.mp3" VOLUME="C:"></LOCATION></ENTRY>
</COLLECTION>
<PLAYLISTS>
 <NODE TYPE="FOLDER" NAME="$ROOT">
  <SUBNODES COUNT="3">
   <NODE TYPE="PLAYLIST" NAME="Root List">
    <PLAYLIST ENTRIES="2" TYPE="LIST" UUID="u1">
     <ENTRY><PRIMARYKEY TYPE="TRACK" KEY="C:/:Music/:a.mp3"></PRIMARYKEY></ENTRY>
     <ENTRY><PRIMARYKEY TYPE="TRACK" KEY="D:/:Other/:b.flac"></PRIMARYKEY></ENTRY>
    </PLAYLIST>
   </NODE>
   <NODE TYPE="FOLDER" NAME="Sets">
    <SUBNODES COUNT="2">
     <NODE TYPE="FOLDER" NAME="2026">
      <SUBNODES COUNT="1">
       <NODE TYPE="PLAYLIST" NAME="Festival">
        <PLAYLIST ENTRIES="1" TYPE="LIST" UUID="u2">
         <ENTRY><PRIMARYKEY TYPE="TRACK" KEY="C:/:Music/:c.wav"></PRIMARYKEY></ENTRY>
        </PLAYLIST>
       </NODE>
      </SUBNODES>
     </NODE>
     <NODE TYPE="PLAYLIST" NAME="Empty">
      <PLAYLIST ENTRIES="0" TYPE="LIST" UUID="u3"></PLAYLIST>
     </NODE>
    </SUBNODES>
   </NODE>
   <NODE TYPE="SMARTLIST" NAME="Traktor Smart">
    <SMARTLIST UUID="u4"></SMARTLIST>
   </NODE>
  </SUBNODES>
 </NODE>
</PLAYLISTS>
</NML>`

func TestParseNMLPlaylists(t *testing.T) {
	pls, err := ParseNMLPlaylists(strings.NewReader(playlistNML))
	if err != nil {
		t.Fatal(err)
	}
	if len(pls) != 3 {
		t.Fatalf("want 3 playlists, got %d: %+v", len(pls), pls)
	}
	sep := string(os.PathSeparator)
	root := pls[0]
	if root.Name != "Root List" || root.Folder != "" || len(root.Paths) != 2 {
		t.Fatalf("root: %+v", root)
	}
	if root.Paths[0] != filepath.Join("C:"+sep+"Music", "a.mp3") {
		t.Fatalf("path resolve: %q", root.Paths[0])
	}
	fest := pls[1]
	if fest.Name != "Festival" || fest.Folder != "Sets/2026" || len(fest.Paths) != 1 {
		t.Fatalf("festival: %+v", fest)
	}
	if pls[2].Name != "Empty" || len(pls[2].Paths) != 0 {
		t.Fatalf("empty: %+v", pls[2])
	}
}

func TestParseNMLPlaylistsAbsent(t *testing.T) {
	pls, err := ParseNMLPlaylists(strings.NewReader(`<NML VERSION="20"><COLLECTION ENTRIES="0"></COLLECTION></NML>`))
	if err != nil || pls != nil {
		t.Fatalf("absent: %v %v", pls, err)
	}
}

func TestSmartRules(t *testing.T) {
	tracks := []Track{
		{Title: "One", Artist: "A", Genre: "Hard Techno", BPM: 150, Rating: 5, PlayCount: 9, Key: "Ebm"},
		{Title: "Two", Artist: "B", Genre: "House", BPM: 124, Rating: 3, PlayCount: 1, Key: "Am"},
		{Title: "Three", Artist: "C", Genre: "Trance", BPM: 138, Rating: 4, PlayCount: 5, Key: "F#m"},
	}
	if got := FilterSmart(tracks, SmartRules{Genres: []string{"techno", "trance"}}); len(got) != 2 {
		t.Fatalf("genres: %+v", got)
	}
	if got := FilterSmart(tracks, SmartRules{BPMMin: 130, BPMMax: 145}); len(got) != 1 || got[0].Title != "Three" {
		t.Fatalf("bpm: %+v", got)
	}
	if got := FilterSmart(tracks, SmartRules{RatingMin: 4, PlayCountMin: 6}); len(got) != 1 || got[0].Title != "One" {
		t.Fatalf("rating+plays: %+v", got)
	}
	// Traktor 0-255 ranking normalizes to stars (204 = 4★)
	if got := FilterSmart([]Track{{Rating: 204}, {Rating: 102}}, SmartRules{RatingMin: 4}); len(got) != 1 {
		t.Fatalf("star norm: %+v", got)
	}
	if StarRating(255) != 5 || StarRating(51) != 1 || StarRating(3) != 3 || StarRating(-1) != 0 {
		t.Fatal("StarRating")
	}
	if got := FilterSmart(tracks, SmartRules{KeyContains: "m", Search: "two"}); len(got) != 1 || got[0].Title != "Two" {
		t.Fatalf("key+search: %+v", got)
	}
	if !(SmartRules{}).Empty() || (SmartRules{BPMMin: 1}).Empty() {
		t.Fatal("Empty()")
	}
	d := SmartRules{Genres: []string{"Techno"}, BPMMin: 138, BPMMax: 150, RatingMin: 4}.Describe()
	for _, want := range []string{"Techno", "138–150", "★≥4"} {
		if !strings.Contains(d, want) {
			t.Fatalf("describe %q missing %q", d, want)
		}
	}
}
