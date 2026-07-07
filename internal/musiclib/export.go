package musiclib

import (
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// ExportM3U writes an extended M3U8 playlist to w.
// Format: #EXTM3U header, then per track: #EXTINF:<sec>,<Artist> - <Title> + path line.
func ExportM3U(tracks []Track, w io.Writer) error {
	if _, err := fmt.Fprintln(w, "#EXTM3U"); err != nil {
		return err
	}
	for _, t := range tracks {
		dur := int(t.DurationSec)
		artist := t.Artist
		if artist == "" {
			artist = "Unknown Artist"
		}
		title := t.Title
		if title == "" {
			title = filepath.Base(t.Path)
		}
		if _, err := fmt.Fprintf(w, "#EXTINF:%d,%s - %s\n%s\n", dur, artist, title, t.Path); err != nil {
			return err
		}
	}
	return nil
}

// csvHeader is the canonical column order for ExportCSV.
var csvHeader = []string{
	"path", "title", "artist", "album", "genre", "label",
	"key", "bpm", "durationSec", "bitrateBps", "fileSizeKB",
	"playCount", "rating", "importDate", "releaseDate", "lastPlayed",
}

// ExportCSV writes a portable metadata backup as CSV to w.
// Header row + one row per track; all normalized fields included.
func ExportCSV(tracks []Track, w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, t := range tracks {
		row := []string{
			t.Path,
			t.Title,
			t.Artist,
			t.Album,
			t.Genre,
			t.Label,
			t.Key,
			strconv.FormatFloat(t.BPM, 'f', 2, 64),
			strconv.FormatFloat(t.DurationSec, 'f', 3, 64),
			strconv.Itoa(t.BitrateBps),
			strconv.Itoa(t.FileSizeKB),
			strconv.Itoa(t.PlayCount),
			strconv.Itoa(t.Rating),
			t.ImportDate,
			t.ReleaseDate,
			t.LastPlayed,
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// ---- Rekordbox XML structs ----
// Field mapping (Rekordbox → Track):
//   TrackID     = auto-incremented index (1-based)
//   Name        = Title
//   Artist      = Artist
//   Album       = Album
//   Genre       = Genre
//   Label       = Label (Rekordbox: "Composer" field is repurposed - see comment below)
//   AverageBpm  = BPM (formatted to 2 dp)
//   Tonality    = Key
//   TotalTime   = DurationSec (integer seconds)
//   BitRate     = BitrateBps / 1000 (kbps)
//   Size        = FileSizeKB * 1024 (bytes)
//   PlayCount   = PlayCount
//   Rating      = Rating * 51 (Rekordbox uses 0-255; we map 0-5 → 0,51,102,153,204,255)
//   DateAdded   = ImportDate
//   Year        = first 4 chars of ReleaseDate if it starts with a year
//   Location    = file://localhost/<url-encoded forward-slash path>
//
// Rekordbox has no "Label" field; we put it in Composer (Label field absent in Rekordbox XML).
// Cue points (POSITION_MARK) and beatgrid (TEMPO) are exported from Track.Cues/Beatgrid.

type rbXMLTempo struct {
	XMLName xml.Name `xml:"TEMPO"`
	Inizio  string   `xml:"Inizio,attr"` // seconds
	Bpm     string   `xml:"Bpm,attr"`
	Metro   string   `xml:"Metro,attr"`   // e.g. "4/4"
	Battito int      `xml:"Battito,attr"` // beat-in-bar (1..4)
}

type rbXMLMark struct {
	XMLName xml.Name `xml:"POSITION_MARK"`
	Name    string   `xml:"Name,attr"`
	Type    int      `xml:"Type,attr"`
	Start   string   `xml:"Start,attr"` // seconds
	End     string   `xml:"End,attr,omitempty"`
	Num     int      `xml:"Num,attr"` // hotcue slot; -1 = memory cue
}

type rbXMLTrack struct {
	XMLName    xml.Name     `xml:"TRACK"`
	TrackID    int          `xml:"TrackID,attr"`
	Name       string       `xml:"Name,attr"`
	Artist     string       `xml:"Artist,attr"`
	Composer   string       `xml:"Composer,attr"` // Label stored here (Rekordbox has no Label attr)
	Album      string       `xml:"Album,attr"`
	Genre      string       `xml:"Genre,attr"`
	TotalTime  int          `xml:"TotalTime,attr"`
	BitRate    int          `xml:"BitRate,attr"`
	Rating     int          `xml:"Rating,attr"`
	AverageBpm string       `xml:"AverageBpm,attr"`
	DateAdded  string       `xml:"DateAdded,attr"`
	Year       string       `xml:"Year,attr"`
	Size       int          `xml:"Size,attr"`
	PlayCount  int          `xml:"PlayCount,attr"`
	Tonality   string       `xml:"Tonality,attr"`
	Location   string       `xml:"Location,attr"`
	Tempos     []rbXMLTempo `xml:""`
	Marks      []rbXMLMark  `xml:""`
}

type rbXMLCollection struct {
	XMLName xml.Name     `xml:"COLLECTION"`
	Entries int          `xml:"Entries,attr"`
	Tracks  []rbXMLTrack `xml:",omitempty"`
}

type rbXMLProduct struct {
	XMLName xml.Name `xml:"PRODUCT"`
	Name    string   `xml:"Name,attr"`
	Version string   `xml:"Version,attr"`
	Company string   `xml:"Company,attr"`
}

type rbXMLRoot struct {
	XMLName    xml.Name        `xml:"DJ_PLAYLISTS"`
	Version    string          `xml:"Version,attr"`
	Product    rbXMLProduct    `xml:"PRODUCT"`
	Collection rbXMLCollection `xml:"COLLECTION"`
	// PLAYLISTS node is required by Rekordbox; kept empty for collection-only export.
	Playlists struct {
		XMLName xml.Name `xml:"PLAYLISTS"`
	} `xml:"PLAYLISTS"`
}

// trackLocation converts an OS path to a Rekordbox file:// URL.
// Rekordbox expects: file://localhost/<forward-slash path> with URL-encoded segments.
func trackLocation(path string) string {
	// Normalise to forward slashes.
	fwd := filepath.ToSlash(path)
	// Split and URL-encode each segment (handles spaces, #, %, etc.).
	parts := strings.Split(fwd, "/")
	encoded := make([]string, len(parts))
	for i, p := range parts {
		encoded[i] = url.PathEscape(p)
	}
	joined := strings.Join(encoded, "/")
	// On Windows the path is e.g. "C:/Music/..." → needs leading slash for file://localhost/.
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return "file://localhost" + joined
}

// bpmStr formats BPM to 2 decimal places (Rekordbox expects "128.00").
func bpmStr(bpm float64) string {
	return strconv.FormatFloat(bpm, 'f', 2, 64)
}

// rbRating maps 0-5 star rating to Rekordbox 0-255 scale.
func rbRating(r int) int {
	if r < 0 {
		r = 0
	}
	if r > 5 {
		r = 5
	}
	return r * 51
}

// yearFromDate extracts "YYYY" from "YYYY/M/D", "YYYY-MM-DD", etc.
func yearFromDate(d string) string {
	if len(d) >= 4 {
		candidate := d[:4]
		if _, err := strconv.Atoi(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// rbTempos builds Rekordbox TEMPO nodes from a track's beatgrid; when no grid is present it
// falls back to a single anchor at 0.0 using the track BPM.
func rbTempos(t Track) []rbXMLTempo {
	if len(t.Beatgrid) == 0 {
		if t.BPM <= 0 {
			return nil
		}
		return []rbXMLTempo{{Inizio: "0.000", Bpm: bpmStr(t.BPM), Metro: "4/4", Battito: 1}}
	}
	out := make([]rbXMLTempo, len(t.Beatgrid))
	for i, g := range t.Beatgrid {
		out[i] = rbXMLTempo{
			Inizio:  strconv.FormatFloat(g.PositionMs/1000, 'f', 3, 64),
			Bpm:     bpmStr(g.BPM),
			Metro:   "4/4",
			Battito: 1,
		}
	}
	return out
}

// rbMarks builds Rekordbox POSITION_MARK nodes from portable cues (grid cues are emitted as
// TEMPO instead, so they're skipped here).
func rbMarks(cues []CuePoint) []rbXMLMark {
	var out []rbXMLMark
	for _, c := range cues {
		if c.Kind == CueGrid {
			continue
		}
		m := rbXMLMark{
			Name:  c.Name,
			Type:  rekordboxCueType(c.Kind),
			Start: strconv.FormatFloat(c.StartMs/1000, 'f', 3, 64),
			Num:   c.Hotcue,
		}
		if c.LenMs > 0 {
			m.End = strconv.FormatFloat((c.StartMs+c.LenMs)/1000, 'f', 3, 64)
		}
		out = append(out, m)
	}
	return out
}

// ExportRekordboxXML writes a minimal Rekordbox-compatible collection XML to w.
// The output can be imported into Rekordbox via File → Import Collection.
func ExportRekordboxXML(tracks []Track, w io.Writer) error {
	rbTracks := make([]rbXMLTrack, len(tracks))
	for i, t := range tracks {
		rbTracks[i] = rbXMLTrack{
			TrackID:    i + 1,
			Name:       t.Title,
			Artist:     t.Artist,
			Composer:   t.Label, // Label has no Rekordbox counterpart; Composer is closest
			Album:      t.Album,
			Genre:      t.Genre,
			TotalTime:  int(t.DurationSec),
			BitRate:    t.BitrateBps / 1000,
			Rating:     rbRating(t.Rating),
			AverageBpm: bpmStr(t.BPM),
			DateAdded:  t.ImportDate,
			Year:       yearFromDate(t.ReleaseDate),
			Size:       t.FileSizeKB * 1024,
			PlayCount:  t.PlayCount,
			Tonality:   t.Key,
			Location:   trackLocation(t.Path),
			Tempos:     rbTempos(t),
			Marks:      rbMarks(t.Cues),
		}
	}

	root := rbXMLRoot{
		Version: "1.0.0",
		Product: rbXMLProduct{
			Name:    "rave-mate",
			Version: "1.0",
			Company: "rave.page",
		},
		Collection: rbXMLCollection{
			Entries: len(tracks),
			Tracks:  rbTracks,
		},
	}

	if _, err := fmt.Fprintf(w, "%s\n", xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(root); err != nil {
		return err
	}
	return enc.Close()
}
