package musiclib

import (
	"encoding/xml"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// Traktor NML write structs (only the fields we emit).

type nmlLocation struct {
	Dir    string `xml:"DIR,attr"`
	File   string `xml:"FILE,attr"`
	Volume string `xml:"VOLUME,attr"`
}

type nmlAlbum struct {
	Title string `xml:"TITLE,attr,omitempty"`
}

type nmlInfo struct {
	Bitrate     int    `xml:"BITRATE,attr,omitempty"`
	Genre       string `xml:"GENRE,attr,omitempty"`
	Label       string `xml:"LABEL,attr,omitempty"`
	Comment     string `xml:"COMMENT,attr,omitempty"`
	Key         string `xml:"KEY,attr,omitempty"`
	Playtime    int    `xml:"PLAYTIME,attr,omitempty"`
	PlaytimeF   string `xml:"PLAYTIME_FLOAT,attr,omitempty"`
	ImportDate  string `xml:"IMPORT_DATE,attr,omitempty"`
	ReleaseDate string `xml:"RELEASE_DATE,attr,omitempty"`
	PlayCount   int    `xml:"PLAYCOUNT,attr,omitempty"`
	Ranking     int    `xml:"RANKING,attr,omitempty"`
	FileSize    int    `xml:"FILESIZE,attr,omitempty"`
}

type nmlTempo struct {
	BPM string `xml:"BPM,attr"`
}

type nmlCue struct {
	Name   string `xml:"NAME,attr"`
	Type   int    `xml:"TYPE,attr"`
	Start  string `xml:"START,attr"`
	Len    string `xml:"LEN,attr"`
	Hotcue int    `xml:"HOTCUE,attr"`
}

type nmlExportEntry struct {
	XMLName  xml.Name    `xml:"ENTRY"`
	Title    string      `xml:"TITLE,attr"`
	Artist   string      `xml:"ARTIST,attr"`
	Location nmlLocation `xml:"LOCATION"`
	Album    *nmlAlbum   `xml:"ALBUM,omitempty"`
	Info     nmlInfo     `xml:"INFO"`
	Tempo    *nmlTempo   `xml:"TEMPO,omitempty"`
	Cues     []nmlCue    `xml:"CUE_V2"`
}

// ExportTraktorNML writes a Traktor-importable collection.nml from a Library. This makes
// Rekordbox/VirtualDJ → Traktor migration possible (the inverse of ParseCollection). Cues
// and beatgrid markers round-trip via CUE_V2.
func ExportTraktorNML(lib Library, w io.Writer) error {
	type head struct {
		Company string `xml:"COMPANY,attr"`
		Program string `xml:"PROGRAM,attr"`
	}
	type collection struct {
		Entries int              `xml:"ENTRIES,attr"`
		Tracks  []nmlExportEntry `xml:"ENTRY"`
	}
	root := struct {
		XMLName    xml.Name   `xml:"NML"`
		Version    int        `xml:"VERSION,attr"`
		Head       head       `xml:"HEAD"`
		Collection collection `xml:"COLLECTION"`
	}{
		Version: 20,
		Head:    head{Company: "rave.page", Program: "rave-mate"},
	}
	for _, t := range lib.Tracks {
		e := nmlExportEntry{Title: t.Title, Artist: t.Artist, Location: pathToLocation(t.Path)}
		if t.Album != "" {
			e.Album = &nmlAlbum{Title: t.Album}
		}
		e.Info = nmlInfo{
			Bitrate: t.BitrateBps, Genre: t.Genre, Label: t.Label, Comment: t.Comment,
			Key: t.Key, Playtime: int(t.DurationSec), ImportDate: t.ImportDate,
			ReleaseDate: t.ReleaseDate, PlayCount: t.PlayCount, Ranking: t.Rating,
			FileSize: t.FileSizeKB,
		}
		if t.DurationSec > 0 {
			e.Info.PlaytimeF = strconv.FormatFloat(t.DurationSec, 'f', 6, 64)
		}
		if t.BPM > 0 {
			e.Tempo = &nmlTempo{BPM: strconv.FormatFloat(t.BPM, 'f', 6, 64)}
		}
		e.Cues = nmlCues(t)
		root.Collection.Tracks = append(root.Collection.Tracks, e)
	}
	root.Collection.Entries = len(root.Collection.Tracks)

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(root); err != nil {
		return err
	}
	return enc.Close()
}

// nmlCues converts portable cues + beatgrid into Traktor CUE_V2 nodes (grid markers become
// TYPE-4 cues).
func nmlCues(t Track) []nmlCue {
	var out []nmlCue
	for _, g := range t.Beatgrid {
		out = append(out, nmlCue{Name: "Beat Marker", Type: traktorCueType(CueGrid),
			Start: strconv.FormatFloat(g.PositionMs, 'f', 6, 64), Len: "0", Hotcue: -1})
	}
	for _, c := range t.Cues {
		out = append(out, nmlCue{
			Name:   c.Name,
			Type:   traktorCueType(c.Kind),
			Start:  strconv.FormatFloat(c.StartMs, 'f', 6, 64),
			Len:    strconv.FormatFloat(c.LenMs, 'f', 6, 64),
			Hotcue: c.Hotcue,
		})
	}
	return out
}

// pathToLocation inverts resolveLocation: an OS path → Traktor LOCATION (volume + "/:"-
// delimited dir + file). On Windows "C:\Music\x.mp3" → {Volume:"C:", Dir:"/:Music/:", File:"x.mp3"}.
func pathToLocation(path string) nmlLocation {
	vol := filepath.VolumeName(path) // "C:" on Windows, "" on unix
	rest := strings.TrimPrefix(path, vol)
	rest = filepath.ToSlash(rest)
	rest = strings.TrimPrefix(rest, "/")
	segs := strings.Split(rest, "/")
	if len(segs) == 0 {
		return nmlLocation{Volume: vol}
	}
	file := segs[len(segs)-1]
	dirSegs := segs[:len(segs)-1]
	dir := "/:"
	if len(dirSegs) > 0 {
		dir = "/:" + strings.Join(dirSegs, "/:") + "/:"
	}
	return nmlLocation{Dir: dir, File: file, Volume: vol}
}
