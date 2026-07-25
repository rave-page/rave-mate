// Package musiclib is the DJ-software-agnostic music library model + importers. It parses
// external libraries (Traktor first) into a normalized in-app model - tracks, playlists,
// play history, file locations - for the library browser / playlist manager. This is
// purely local (no API); the goal is library management + portability (migrating between
// DJ softwares, metadata backups). Fingerprint→API ingest is a separate feature.
package musiclib

import "time"

// Source identifies where a library was imported from. App is one of "traktor",
// "rekordbox", "virtualdj", "enginedj", "serato", or "folder" (a loose directory
// imported straight into the collection - no DJ software involved).
type Source struct {
	App     string `json:"app"`     // e.g. "traktor"
	Version string `json:"version"` // e.g. "4.2.0"
	Path    string `json:"path"`    // the collection/db file
}

// Track is one normalized library track. Fields absent in the source stay zero.
type Track struct {
	Path        string       `json:"path"` // resolved absolute file path
	Title       string       `json:"title"`
	Artist      string       `json:"artist"`
	Album       string       `json:"album"`
	Genre       string       `json:"genre"`
	Label       string       `json:"label"`
	Comment     string       `json:"comment"`
	Key         string       `json:"key"` // readable, e.g. "Ebm"
	BPM         float64      `json:"bpm"`
	DurationSec float64      `json:"durationSec"`
	BitrateBps  int          `json:"bitrateBps"`
	FileSizeKB  int          `json:"fileSizeKB"`
	PlayCount   int          `json:"playCount"`
	Rating      int          `json:"rating"`     // 0-5 (source-dependent; 0 if absent)
	ImportDate  string       `json:"importDate"` // raw source date (YYYY/M/D)
	ReleaseDate string       `json:"releaseDate"`
	LastPlayed  string       `json:"lastPlayed"`
	Cues        []CuePoint   `json:"cues,omitempty"`
	Beatgrid    []GridMarker `json:"beatgrid,omitempty"`
}

// CueKind is a portable, source-agnostic cue classification so cue semantics survive
// migration between DJ softwares (each app numbers its own cue types differently).
type CueKind string

const (
	CuePlain CueKind = "cue"  // generic marker
	CueHot   CueKind = "hot"  // hotcue (mapped to a pad/slot)
	CueLoad  CueKind = "load" // load/start marker
	CueLoop  CueKind = "loop" // loop in (LenMs > 0)
	CueGrid  CueKind = "grid" // beatgrid anchor
	CueFade  CueKind = "fade" // fade in/out marker
)

// CuePoint is a hotcue / loop / grid marker (positions in ms). Kind is the portable
// classification; Type retains the source app's raw enum for round-trip fidelity.
type CuePoint struct {
	Name    string  `json:"name"`
	Kind    CueKind `json:"kind"`
	Type    int     `json:"type"`            // source raw enum (Traktor CUE_V2 TYPE, etc.)
	StartMs float64 `json:"startMs"`         // position in ms
	LenMs   float64 `json:"lenMs,omitempty"` // loop length in ms (0 = point cue)
	Hotcue  int     `json:"hotcue"`          // pad/slot index; -1 = not a hotcue
	Sw      string  `json:"sw,omitempty"`    // software scope: "" = every DJ software, else "traktor"|"rekordbox"|"serato"|"virtualdj" - only that app's write-back exports it
}

// MusicalCues counts non-grid cues (hotcues / memory cues / loops - what the editors flag).
func MusicalCues(cues []CuePoint) int {
	n := 0
	for _, c := range cues {
		if c.Kind != CueGrid {
			n++
		}
	}
	return n
}

// GridMarker is one beatgrid anchor: a downbeat position + the tempo from there. A single
// marker plus a constant BPM describes Traktor's usual fixed grid; multiple markers handle
// variable-tempo grids (Rekordbox TEMPO nodes).
type GridMarker struct {
	PositionMs float64 `json:"positionMs"`
	BPM        float64 `json:"bpm"`
}

// PlayedTrack is one entry in a play-history session (a ref into the collection + when). Title/Artist
// etc. come from the history file's own embedded COLLECTION block (ParseHistory joins them), so a
// tracklist can be reconstructed even when the played file isn't in the live library (the "unknown
// track" case: a deck loaded from a USB/incoming folder never imported into the collection).
type PlayedTrack struct {
	Path        string    `json:"path"` // resolved file path (matches a Track.Path)
	Deck        int       `json:"deck"`
	StartedAt   time.Time `json:"startedAt"` // decoded from the source stamp (zero if undecodable)
	DurationSec float64   `json:"durationSec"`
	Public      bool      `json:"public"`
	Title       string    `json:"title,omitempty"`  // from the history's embedded COLLECTION metadata
	Artist      string    `json:"artist,omitempty"` //
	Album       string    `json:"album,omitempty"`  //
	Key         string    `json:"key,omitempty"`    //
	BPM         float64   `json:"bpm,omitempty"`    //
}

// Playlist is an ordered named list of track paths (Traktor PLAYLIST node).
// Folder is the source folder path ("Foo/Sub"), "" at root.
type Playlist struct {
	Name   string   `json:"name"`
	Folder string   `json:"folder,omitempty"`
	Paths  []string `json:"paths"`
}

// Session is one play-history file (a DJ set / protocol playlist).
type Session struct {
	Name      string        `json:"name"`
	Played    []PlayedTrack `json:"played"`
	StartedAt time.Time     `json:"startedAt,omitempty"` // earliest play, if known
}

// Library is the imported, normalized result.
type Library struct {
	Source    Source     `json:"source"`
	Tracks    []Track    `json:"tracks"`
	Playlists []Playlist `json:"playlists,omitempty"`
	Sessions  []Session  `json:"sessions,omitempty"`
}
