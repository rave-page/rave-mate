package musiclib

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// VirtualDJ database.xml schema (the fields we map):
//
//	<VirtualDJ_Database Version="…">
//	  <Song FilePath="C:\Music\x.mp3" FileSize="…">
//	    <Tags Author Title Album Genre Bpm Key Year/>
//	    <Infos SongLength Bitrate PlayCount FirstSeen LastPlay/>
//	    <Poi Pos="12.3" Type="cue" Num="1" Name="Drop"/>
//	  </Song>
//	</VirtualDJ_Database>
//
// Bpm is stored as seconds-per-beat (e.g. "0.461538" = 130 BPM); we convert defensively.

type vdjSong struct {
	FilePath string `xml:"FilePath,attr"`
	FileSize int    `xml:"FileSize,attr"`
	Tags     struct {
		Author string `xml:"Author,attr"`
		Title  string `xml:"Title,attr"`
		Album  string `xml:"Album,attr"`
		Genre  string `xml:"Genre,attr"`
		Label  string `xml:"Label,attr"`
		Bpm    string `xml:"Bpm,attr"`
		Key    string `xml:"Key,attr"`
		Year   string `xml:"Year,attr"`
	} `xml:"Tags"`
	Infos struct {
		SongLength float64 `xml:"SongLength,attr"`
		Bitrate    int     `xml:"Bitrate,attr"`
		PlayCount  int     `xml:"PlayCount,attr"`
		FirstSeen  string  `xml:"FirstSeen,attr"`
		LastPlay   string  `xml:"LastPlay,attr"`
	} `xml:"Infos"`
	Pois []vdjPoi `xml:"Poi"`
}

type vdjPoi struct {
	Pos  float64 `xml:"Pos,attr"` // seconds
	Type string  `xml:"Type,attr"`
	Num  int     `xml:"Num,attr"`
	Name string  `xml:"Name,attr"`
	Bpm  string  `xml:"Bpm,attr"` // present on beatgrid POIs
}

// vdjBPM converts VirtualDJ's seconds-per-beat (or raw BPM) to BPM. Values <10 are treated
// as seconds-per-beat; larger values are already BPM.
func vdjBPM(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0
	}
	if v < 10 {
		return 60 / v
	}
	return v
}

func (s *vdjSong) toTrack() Track {
	t := Track{
		Path:        s.FilePath,
		Title:       s.Tags.Title,
		Artist:      s.Tags.Author,
		Album:       s.Tags.Album,
		Genre:       s.Tags.Genre,
		Label:       s.Tags.Label,
		Key:         s.Tags.Key,
		BPM:         vdjBPM(s.Tags.Bpm),
		DurationSec: s.Infos.SongLength,
		BitrateBps:  s.Infos.Bitrate * 1000,
		FileSizeKB:  s.FileSize / 1024,
		PlayCount:   s.Infos.PlayCount,
		ImportDate:  s.Infos.FirstSeen,
		ReleaseDate: s.Tags.Year,
		LastPlayed:  s.Infos.LastPlay,
	}
	for _, p := range s.Pois {
		kind := virtualdjCueKind(p.Type)
		if kind == CueGrid {
			t.Beatgrid = append(t.Beatgrid, GridMarker{PositionMs: p.Pos * 1000, BPM: vdjBPM(p.Bpm)})
			continue
		}
		t.Cues = append(t.Cues, CuePoint{Name: p.Name, Kind: kind, StartMs: p.Pos * 1000, Hotcue: p.Num})
	}
	return t
}

// ParseVirtualDJ streams a VirtualDJ database.xml, calling onTrack for each Song. Returns
// the number of tracks emitted.
func ParseVirtualDJ(r io.Reader, onTrack func(Track)) (int, error) {
	dec := xml.NewDecoder(r)
	n := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Song" {
			continue
		}
		var s vdjSong
		if dec.DecodeElement(&s, &se) != nil {
			continue
		}
		if s.FilePath == "" {
			continue
		}
		onTrack(s.toTrack())
		n++
	}
}

// ExportVirtualDJ writes a VirtualDJ-compatible database.xml. BPM is written as
// seconds-per-beat to match VirtualDJ's native encoding.
func ExportVirtualDJ(tracks []Track, w io.Writer) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	root := struct {
		XMLName xml.Name  `xml:"VirtualDJ_Database"`
		Version string    `xml:"Version,attr"`
		Songs   []vdjSong `xml:"Song"`
	}{Version: "8.5"}
	for _, t := range tracks {
		s := vdjSong{FilePath: t.Path, FileSize: t.FileSizeKB * 1024}
		s.Tags.Author, s.Tags.Title, s.Tags.Album = t.Artist, t.Title, t.Album
		s.Tags.Genre, s.Tags.Label, s.Tags.Key, s.Tags.Year = t.Genre, t.Label, t.Key, t.ReleaseDate
		if t.BPM > 0 {
			s.Tags.Bpm = strconv.FormatFloat(60/t.BPM, 'f', 6, 64)
		}
		s.Infos.SongLength = t.DurationSec
		s.Infos.Bitrate = t.BitrateBps / 1000
		s.Infos.PlayCount = t.PlayCount
		s.Infos.FirstSeen, s.Infos.LastPlay = t.ImportDate, t.LastPlayed
		for _, g := range t.Beatgrid {
			bpm := ""
			if g.BPM > 0 {
				bpm = strconv.FormatFloat(60/g.BPM, 'f', 6, 64)
			}
			s.Pois = append(s.Pois, vdjPoi{Pos: g.PositionMs / 1000, Type: "beatgrid", Bpm: bpm})
		}
		for _, c := range t.Cues {
			s.Pois = append(s.Pois, vdjPoi{Pos: c.StartMs / 1000, Type: virtualdjPoiType(c.Kind), Num: c.Hotcue, Name: c.Name})
		}
		root.Songs = append(root.Songs, s)
	}
	if err := enc.Encode(root); err != nil {
		return err
	}
	return enc.Close()
}

// DiscoverVirtualDJ locates the default VirtualDJ database.xml. Returns "" (no error) when
// absent so the UI can offer a Browse… fallback.
func DiscoverVirtualDJ() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, "Documents", "VirtualDJ", "database.xml")
	if fileExists(p) {
		return p, nil
	}
	return "", nil
}
