// Package nmlsrc reads Traktor's NML files (collection.nml + History/*.nml) to enrich the
// live session with collection-accurate metadata (album, genre, label, key, BPM) - filling
// fields the live deck feed often lacks, including for decks C/D. NML is authoritative for
// descriptive metadata but lags real time, so it's a high-priority *metadata* source, not a
// now-playing source.
package nmlsrc

import (
	"encoding/xml"
	"os"
	"strings"
)

// Meta is the collection metadata for one track.
type Meta struct {
	Title  string
	Artist string
	Album  string
	Genre  string
	Label  string
	Key    string
	BPM    float64
	Path   string // resolved file path (volume + dir + file), best-effort
}

// ── NML XML shapes (attributes only; Traktor uses attribute-heavy elements) ────

type nmlDoc struct {
	XMLName    xml.Name    `xml:"NML"`
	Collection nmlEntries  `xml:"COLLECTION"`
	Playlists  nmlPlaylist `xml:"PLAYLISTS"`
}

type nmlEntries struct {
	Entries []nmlEntry `xml:"ENTRY"`
}

type nmlEntry struct {
	Title    string      `xml:"TITLE,attr"`
	Artist   string      `xml:"ARTIST,attr"`
	Album    nmlAlbum    `xml:"ALBUM"`
	Info     nmlInfo     `xml:"INFO"`
	Tempo    nmlTempo    `xml:"TEMPO"`
	Location nmlLocation `xml:"LOCATION"`
	// History entries carry the play timestamp in EXTENDEDDATA / a STARTDATE attr.
	StartDate string `xml:"STARTDATE,attr"`
}

type nmlAlbum struct {
	Title string `xml:"TITLE,attr"`
}

type nmlInfo struct {
	Genre    string `xml:"GENRE,attr"`
	Label    string `xml:"LABEL,attr"`
	Key      string `xml:"KEY,attr"`
	PlayTime int    `xml:"PLAYTIME,attr"`
}

type nmlTempo struct {
	BPM float64 `xml:"BPM,attr"`
}

type nmlLocation struct {
	Dir    string `xml:"DIR,attr"`
	File   string `xml:"FILE,attr"`
	Volume string `xml:"VOLUME,attr"`
}

// nmlPlaylist is only needed to confirm a doc is a history file (presence of playlists).
type nmlPlaylist struct {
	Nodes []struct {
		Name string `xml:"NAME,attr"`
	} `xml:"NODE"`
}

// metaFromEntry converts a parsed entry to Meta.
func metaFromEntry(e nmlEntry) Meta {
	return Meta{
		Title:  strings.TrimSpace(e.Title),
		Artist: strings.TrimSpace(e.Artist),
		Album:  strings.TrimSpace(e.Album.Title),
		Genre:  strings.TrimSpace(e.Info.Genre),
		Label:  strings.TrimSpace(e.Info.Label),
		Key:    strings.TrimSpace(e.Info.Key),
		BPM:    e.Tempo.BPM,
		Path:   resolvePath(e.Location),
	}
}

// resolvePath best-effort reconstructs a filesystem path from a Traktor LOCATION. Traktor
// encodes path separators as "/:" inside DIR; we normalise to native-ish slashes.
func resolvePath(l nmlLocation) string {
	if l.File == "" {
		return ""
	}
	dir := strings.ReplaceAll(l.Dir, "/:", "/")
	return strings.TrimSuffix(dir, "/") + "/" + l.File
}

// Key is the normalized lookup key for a (title, artist) pair.
func Key(title, artist string) string {
	return strings.ToLower(strings.TrimSpace(title)) + "|" + strings.ToLower(strings.TrimSpace(artist))
}

// ParseCollection reads an NML file and returns a title|artist → Meta index. Works for
// both collection.nml and history files (both hold ENTRY lists). Last entry wins on dup.
func ParseCollection(path string) (map[string]Meta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return IndexBytes(raw)
}

// IndexBytes parses NML bytes into a metadata index (exported for tests).
func IndexBytes(raw []byte) (map[string]Meta, error) {
	var doc nmlDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	idx := make(map[string]Meta, len(doc.Collection.Entries))
	for _, e := range doc.Collection.Entries {
		m := metaFromEntry(e)
		if m.Title == "" {
			continue
		}
		idx[Key(m.Title, m.Artist)] = m
	}
	return idx, nil
}
