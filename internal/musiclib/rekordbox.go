package musiclib

import (
	"encoding/xml"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RekordboxInstall is a discovered Rekordbox exported-collection XML. The live master.db
// (SQLCipher) and USB export.pdb are separate sources; users can also export via
// File → Export Collection in xml format, which we consume here.
type RekordboxInstall struct {
	XML string // path to the exported collection XML
}

// rbImportTrack decodes the TRACK fields + child nodes we map. Distinct from the
// export-side rbXMLTrack (attrs-only) because import also needs TEMPO + POSITION_MARK.
type rbImportTrack struct {
	TrackID    int     `xml:"TrackID,attr"` // collection-local id; playlist TRACK refs key on it
	Name       string  `xml:"Name,attr"`
	Artist     string  `xml:"Artist,attr"`
	Composer   string  `xml:"Composer,attr"`
	Album      string  `xml:"Album,attr"`
	Genre      string  `xml:"Genre,attr"`
	Comments   string  `xml:"Comments,attr"`
	TotalTime  float64 `xml:"TotalTime,attr"`
	BitRate    int     `xml:"BitRate,attr"`
	Rating     int     `xml:"Rating,attr"`
	AverageBpm float64 `xml:"AverageBpm,attr"`
	DateAdded  string  `xml:"DateAdded,attr"`
	Year       string  `xml:"Year,attr"`
	Size       int     `xml:"Size,attr"`
	PlayCount  int     `xml:"PlayCount,attr"`
	Tonality   string  `xml:"Tonality,attr"`
	Location   string  `xml:"Location,attr"`
	Tempos     []struct {
		Inizio float64 `xml:"Inizio,attr"` // seconds
		Bpm    float64 `xml:"Bpm,attr"`
	} `xml:"TEMPO"`
	Marks []struct {
		Name  string  `xml:"Name,attr"`
		Type  int     `xml:"Type,attr"`
		Start float64 `xml:"Start,attr"` // seconds
		End   float64 `xml:"End,attr"`   // seconds (loops)
		Num   int     `xml:"Num,attr"`   // hotcue slot; -1 = memory cue
	} `xml:"POSITION_MARK"`
}

func (t *rbImportTrack) toTrack() Track {
	tr := Track{
		Path:        locationToPath(t.Location),
		Title:       t.Name,
		Artist:      t.Artist,
		Album:       t.Album,
		Genre:       t.Genre,
		Label:       t.Composer, // Rekordbox stores our Label in Composer (see export.go)
		Comment:     t.Comments,
		Key:         t.Tonality,
		BPM:         t.AverageBpm,
		DurationSec: t.TotalTime,
		BitrateBps:  t.BitRate * 1000,
		FileSizeKB:  t.Size / 1024,
		PlayCount:   t.PlayCount,
		Rating:      t.Rating / 51, // Rekordbox 0-255 → 0-5
		ImportDate:  t.DateAdded,
		ReleaseDate: t.Year,
	}
	for _, tm := range t.Tempos {
		tr.Beatgrid = append(tr.Beatgrid, GridMarker{PositionMs: tm.Inizio * 1000, BPM: tm.Bpm})
	}
	for _, m := range t.Marks {
		c := CuePoint{
			Name:    m.Name,
			Kind:    rekordboxCueKind(m.Type, m.Num),
			Type:    m.Type,
			StartMs: m.Start * 1000,
			Hotcue:  m.Num,
		}
		if m.End > m.Start {
			c.LenMs = (m.End - m.Start) * 1000
		}
		tr.Cues = append(tr.Cues, c)
	}
	return tr
}

// ParseRekordboxXML streams a Rekordbox exported collection XML, calling onTrack for each
// COLLECTION > TRACK. Token-streamed so large libraries stay flat in memory. Returns the
// number of tracks emitted.
func ParseRekordboxXML(r io.Reader, onTrack func(Track)) (int, error) {
	dec := xml.NewDecoder(r)
	inCollection := false
	n := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "COLLECTION":
				inCollection = true
			case "TRACK":
				if !inCollection {
					continue // TRACK also appears inside PLAYLISTS as a key ref
				}
				var t rbImportTrack
				if dec.DecodeElement(&t, &se) != nil {
					continue
				}
				if t.Location == "" {
					continue
				}
				onTrack(t.toTrack())
				n++
			}
		case xml.EndElement:
			if se.Name.Local == "COLLECTION" {
				inCollection = false
			}
		}
	}
}

// rbNode is one PLAYLISTS-tree node. Type 0 = folder (has child NODEs), 1 = playlist (has
// TRACK refs). KeyType 0 = TRACK Key is a collection TrackID; 1 = Key is a Location URL.
type rbNode struct {
	Type    int           `xml:"Type,attr"`
	Name    string        `xml:"Name,attr"`
	KeyType int           `xml:"KeyType,attr"`
	Nodes   []rbNode      `xml:"NODE"`
	Tracks  []rbNodeTrack `xml:"TRACK"`
}

type rbNodeTrack struct {
	Key string `xml:"Key,attr"`
}

// ParseRekordboxLibrary streams a Rekordbox exported XML into tracks + playlists in one pass.
// COLLECTION (large) is token-streamed; the PLAYLISTS subtree (small, always after COLLECTION)
// is decoded whole and its TRACK Key refs resolved to file paths via the collection's TrackID
// index. Rekordbox XML carries no play-history timestamps - sessions come from master.db/PDB.
func ParseRekordboxLibrary(r io.Reader) ([]Track, []Playlist, error) {
	dec := xml.NewDecoder(r)
	var tracks []Track
	var playlists []Playlist
	idIndex := map[string]string{} // TrackID → resolved path
	inCollection := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return tracks, playlists, nil
		}
		if err != nil {
			return tracks, playlists, err
		}
		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "COLLECTION":
				inCollection = true
			case "TRACK":
				if !inCollection {
					continue // playlist TRACK refs are handled in the PLAYLISTS decode
				}
				var t rbImportTrack
				if dec.DecodeElement(&t, &se) != nil || t.Location == "" {
					continue
				}
				tr := t.toTrack()
				tracks = append(tracks, tr)
				if t.TrackID != 0 {
					idIndex[strconv.Itoa(t.TrackID)] = tr.Path
				}
			case "PLAYLISTS":
				var pl struct {
					Nodes []rbNode `xml:"NODE"`
				}
				if dec.DecodeElement(&pl, &se) != nil {
					continue
				}
				for _, n := range pl.Nodes {
					playlists = append(playlists, flattenRekordboxNodes(n, "", idIndex)...)
				}
			}
		case xml.EndElement:
			if se.Name.Local == "COLLECTION" {
				inCollection = false
			}
		}
	}
}

// flattenRekordboxNodes walks the PLAYLISTS tree into flat Playlists. Folder NODEs (Type 0)
// contribute their name to the folder path (the synthetic "ROOT" node excepted); playlist
// NODEs (Type 1) become a Playlist with resolved track paths. Empty playlists are kept.
func flattenRekordboxNodes(n rbNode, folder string, idx map[string]string) []Playlist {
	if n.Type == 1 {
		paths := make([]string, 0, len(n.Tracks))
		for _, t := range n.Tracks {
			if p := resolveRekordboxKey(t.Key, n.KeyType, idx); p != "" {
				paths = append(paths, p)
			}
		}
		return []Playlist{{Name: n.Name, Folder: folder, Paths: paths}}
	}
	sub := folder
	if n.Name != "" && !strings.EqualFold(n.Name, "ROOT") {
		if sub == "" {
			sub = n.Name
		} else {
			sub += "/" + n.Name
		}
	}
	var out []Playlist
	for _, c := range n.Nodes {
		out = append(out, flattenRekordboxNodes(c, sub, idx)...)
	}
	return out
}

// resolveRekordboxKey maps a playlist TRACK Key to a file path: KeyType 1 (or a file:// key)
// is a Location URL; otherwise the key is a collection TrackID looked up in idx.
func resolveRekordboxKey(key string, keyType int, idx map[string]string) string {
	if keyType == 1 || strings.HasPrefix(key, "file://") {
		return locationToPath(key)
	}
	return idx[key]
}

// locationToPath inverts trackLocation: "file://localhost/<url-encoded fwd path>" → OS path.
func locationToPath(loc string) string {
	s := strings.TrimPrefix(loc, "file://localhost")
	s = strings.TrimPrefix(s, "file://")
	if dec, err := url.PathUnescape(s); err == nil {
		s = dec
	}
	// Windows: "/C:/Music/x.mp3" → "C:/Music/x.mp3"; drop the leading slash before a drive.
	if len(s) >= 3 && s[0] == '/' && s[2] == ':' {
		s = s[1:]
	}
	return filepath.FromSlash(s)
}

// DiscoverRekordbox locates exported Rekordbox collection XML files in the default
// per-OS Pioneer directory. Returns empty (not an error) when none are found; the UI offers
// a Browse… picker fallback.
func DiscoverRekordbox() ([]RekordboxInstall, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var roots []string
	switch {
	case dirExists(filepath.Join(home, "Library", "Pioneer", "rekordbox")):
		roots = append(roots, filepath.Join(home, "Library", "Pioneer", "rekordbox"))
	default:
		if ad := os.Getenv("APPDATA"); ad != "" {
			roots = append(roots, filepath.Join(ad, "Pioneer", "rekordbox"))
		}
		roots = append(roots, filepath.Join(home, "Documents", "rekordbox"))
	}
	var out []RekordboxInstall
	for _, root := range roots {
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".xml") {
				out = append(out, RekordboxInstall{XML: filepath.Join(root, e.Name())})
			}
		}
	}
	return out, nil
}
