package musiclib

import (
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"strings"
	"testing"
)

// syntheticTracks returns two tracks with edge-case characters.
func syntheticTracks() []Track {
	return []Track{
		{
			Path:        `C:\Music\Techno\01 - Bass & Treble.mp3`,
			Title:       "Bass & Treble",
			Artist:      "DJ Test",
			Album:       "Album One",
			Genre:       "Techno",
			Label:       "Label, Ltd.",
			Key:         "Am",
			BPM:         138.00,
			DurationSec: 360.5,
			BitrateBps:  320000,
			FileSizeKB:  14400,
			PlayCount:   7,
			Rating:      4,
			ImportDate:  "2024/1/15",
			ReleaseDate: "2023/06/01",
			LastPlayed:  "2024/3/10",
		},
		{
			Path:        `C:\Music\House\02 - "Quotes" & <Angles>.flac`,
			Title:       `"Quotes" & <Angles>`,
			Artist:      "Artist, Two",
			Album:       "",
			Genre:       "House",
			Label:       "",
			Key:         "Ebm",
			BPM:         124.50,
			DurationSec: 420.0,
			BitrateBps:  1411000,
			FileSizeKB:  52000,
			PlayCount:   0,
			Rating:      5,
			ImportDate:  "2024/2/20",
			ReleaseDate: "2022/11/30",
			LastPlayed:  "",
		},
	}
}

// ---- M3U ----

func TestExportM3U_Header(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportM3U(syntheticTracks(), &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "#EXTM3U\n") {
		t.Errorf("missing #EXTM3U header; got: %q", out[:min(len(out), 20)])
	}
}

func TestExportM3U_ExtinfAndPath(t *testing.T) {
	tracks := syntheticTracks()
	var buf bytes.Buffer
	if err := ExportM3U(tracks, &buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	// lines[0] = #EXTM3U
	// lines[1] = #EXTINF for track[0], lines[2] = path for track[0]
	// lines[3] = #EXTINF for track[1], lines[4] = path for track[1]
	if len(lines) < 5 {
		t.Fatalf("expected at least 5 lines, got %d:\n%s", len(lines), buf.String())
	}
	if !strings.HasPrefix(lines[1], "#EXTINF:") {
		t.Errorf("line 1 should be #EXTINF, got %q", lines[1])
	}
	if lines[2] != tracks[0].Path {
		t.Errorf("expected path %q, got %q", tracks[0].Path, lines[2])
	}
	if !strings.HasPrefix(lines[3], "#EXTINF:") {
		t.Errorf("line 3 should be #EXTINF, got %q", lines[3])
	}
	if lines[4] != tracks[1].Path {
		t.Errorf("expected path %q, got %q", tracks[1].Path, lines[4])
	}
}

func TestExportM3U_DurationInExtinf(t *testing.T) {
	tracks := syntheticTracks()
	var buf bytes.Buffer
	if err := ExportM3U(tracks, &buf); err != nil {
		t.Fatal(err)
	}
	// track[0] DurationSec=360.5 → int=360
	if !strings.Contains(buf.String(), "#EXTINF:360,") {
		t.Errorf("expected #EXTINF:360, in output; output:\n%s", buf.String())
	}
}

// ---- CSV ----

func TestExportCSV_HeaderAndRows(t *testing.T) {
	tracks := syntheticTracks()
	var buf bytes.Buffer
	if err := ExportCSV(tracks, &buf); err != nil {
		t.Fatal(err)
	}
	r := csv.NewReader(&buf)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	if len(rows) != len(tracks)+1 {
		t.Errorf("expected %d rows (header + %d tracks), got %d", len(tracks)+1, len(tracks), len(rows))
	}
	// Check header
	if rows[0][0] != "path" {
		t.Errorf("first header col should be 'path', got %q", rows[0][0])
	}
	if rows[0][2] != "artist" {
		t.Errorf("third header col should be 'artist', got %q", rows[0][2])
	}
}

func TestExportCSV_CommaInArtist(t *testing.T) {
	// Artist "Artist, Two" contains a comma - must be CSV-quoted, not split.
	tracks := syntheticTracks()
	var buf bytes.Buffer
	if err := ExportCSV(tracks, &buf); err != nil {
		t.Fatal(err)
	}
	r := csv.NewReader(&buf)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv parse after comma-artist: %v", err)
	}
	// row index 2 = track[1] (0=header, 1=track[0], 2=track[1])
	artistCol := 2 // "artist" is index 2 in csvHeader
	if rows[2][artistCol] != "Artist, Two" {
		t.Errorf("artist round-trip: want %q, got %q", "Artist, Two", rows[2][artistCol])
	}
}

func TestExportCSV_AllFieldsPresent(t *testing.T) {
	tracks := syntheticTracks()
	var buf bytes.Buffer
	if err := ExportCSV(tracks, &buf); err != nil {
		t.Fatal(err)
	}
	r := csv.NewReader(&buf)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows[0]) != len(csvHeader) {
		t.Errorf("header col count: want %d, got %d", len(csvHeader), len(rows[0]))
	}
	if rows[1][len(csvHeader)-1] != tracks[0].LastPlayed {
		t.Errorf("lastPlayed round-trip failed")
	}
}

// ---- Rekordbox XML ----

// rbTrackXML is a minimal struct for unmarshalling the TRACK element in tests.
type rbTrackXML struct {
	XMLName    xml.Name `xml:"TRACK"`
	Name       string   `xml:"Name,attr"`
	Artist     string   `xml:"Artist,attr"`
	AverageBpm string   `xml:"AverageBpm,attr"`
	Location   string   `xml:"Location,attr"`
	Tonality   string   `xml:"Tonality,attr"`
}

type rbCollXML struct {
	XMLName xml.Name     `xml:"COLLECTION"`
	Entries int          `xml:"Entries,attr"`
	Tracks  []rbTrackXML `xml:"TRACK"`
}

type rbRootXML struct {
	XMLName    xml.Name  `xml:"DJ_PLAYLISTS"`
	Collection rbCollXML `xml:"COLLECTION"`
}

func TestExportRekordboxXML_Unmarshal(t *testing.T) {
	tracks := syntheticTracks()
	var buf bytes.Buffer
	if err := ExportRekordboxXML(tracks, &buf); err != nil {
		t.Fatal(err)
	}
	var root rbRootXML
	if err := xml.Unmarshal(buf.Bytes(), &root); err != nil {
		t.Fatalf("xml.Unmarshal: %v\noutput:\n%s", err, buf.String())
	}
	if root.Collection.Entries != len(tracks) {
		t.Errorf("Entries attr: want %d, got %d", len(tracks), root.Collection.Entries)
	}
	if len(root.Collection.Tracks) != len(tracks) {
		t.Fatalf("track count: want %d, got %d", len(tracks), len(root.Collection.Tracks))
	}
}

func TestExportRekordboxXML_Fields(t *testing.T) {
	tracks := syntheticTracks()
	var buf bytes.Buffer
	if err := ExportRekordboxXML(tracks, &buf); err != nil {
		t.Fatal(err)
	}
	var root rbRootXML
	if err := xml.Unmarshal(buf.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	tr := root.Collection.Tracks[0]
	if tr.Name != tracks[0].Title {
		t.Errorf("Name: want %q, got %q", tracks[0].Title, tr.Name)
	}
	if tr.Artist != tracks[0].Artist {
		t.Errorf("Artist: want %q, got %q", tracks[0].Artist, tr.Artist)
	}
	if tr.AverageBpm != "138.00" {
		t.Errorf("AverageBpm: want 138.00, got %q", tr.AverageBpm)
	}
	if tr.Tonality != tracks[0].Key {
		t.Errorf("Tonality: want %q, got %q", tracks[0].Key, tr.Tonality)
	}
	if !strings.HasPrefix(tr.Location, "file://") {
		t.Errorf("Location should start with file://, got %q", tr.Location)
	}
}

func TestExportRekordboxXML_AmpersandEscaping(t *testing.T) {
	// Track title "Bass & Treble" and artist "Artist, Two" must be XML-escaped.
	tracks := syntheticTracks()
	var buf bytes.Buffer
	if err := ExportRekordboxXML(tracks, &buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	// After proper XML encoding, & → &amp; in the raw bytes
	if strings.Contains(raw, "Bass & Treble") {
		t.Error("raw XML contains unescaped '&' in title attribute")
	}
	// But unmarshal must recover the original value
	var root rbRootXML
	if err := xml.Unmarshal(buf.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	if root.Collection.Tracks[0].Name != "Bass & Treble" {
		t.Errorf("title round-trip: want 'Bass & Treble', got %q", root.Collection.Tracks[0].Name)
	}
}

func TestExportRekordboxXML_LocationForwardSlashes(t *testing.T) {
	tracks := syntheticTracks()
	var buf bytes.Buffer
	if err := ExportRekordboxXML(tracks, &buf); err != nil {
		t.Fatal(err)
	}
	var root rbRootXML
	xml.Unmarshal(buf.Bytes(), &root) //nolint:errcheck
	loc := root.Collection.Tracks[0].Location
	if strings.Contains(loc, `\`) {
		t.Errorf("Location should use forward slashes, got %q", loc)
	}
}

// min is defined to avoid Go 1.20 builtin dependency issues in tests.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
