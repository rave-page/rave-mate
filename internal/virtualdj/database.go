// Package virtualdj parses the VirtualDJ database.xml collection. VirtualDJ stores one
// database.xml per location: the main one under Documents\VirtualDJ plus a
// <drive>:\VirtualDJ\database.xml at the root of each drive holding indexed media (Windows).
// CRITICAL: Bpm (in <Scan> and <Tags>) is seconds-between-beats, NOT bpm - convert
// bpm = 60/value. Prefer Scan.Bpm, fall back to Tags.Bpm.
package virtualdj

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Track is one collection entry, normalized.
type Track struct {
	Path      string
	Title     string
	Artist    string
	Album     string
	Genre     string
	Key       string
	BPM       float64
	LengthSec int
	PlayCount int
}

// XML shapes - attrs read as strings so we control conversion (esp. the BPM units gotcha).
// Parsed by streaming <Song> elements (see Parse), so there's no root-document struct.
type xmlSong struct {
	FilePath string   `xml:"FilePath,attr"`
	FileSize string   `xml:"FileSize,attr"`
	Tags     xmlTags  `xml:"Tags"`
	Scan     xmlScan  `xml:"Scan"`
	Infos    xmlInfos `xml:"Infos"`
}

type xmlTags struct {
	Author string `xml:"Author,attr"`
	Title  string `xml:"Title,attr"`
	Album  string `xml:"Album,attr"`
	Genre  string `xml:"Genre,attr"`
	Key    string `xml:"Key,attr"`
	Bpm    string `xml:"Bpm,attr"` // seconds-between-beats
	Year   string `xml:"Year,attr"`
}

type xmlScan struct {
	Bpm    string `xml:"Bpm,attr"` // seconds-between-beats
	AltBpm string `xml:"AltBpm,attr"`
	Key    string `xml:"Key,attr"`
}

type xmlInfos struct {
	SongLength string `xml:"SongLength,attr"` // seconds (float)
	Bitrate    string `xml:"Bitrate,attr"`
	PlayCount  string `xml:"PlayCount,attr"`
}

// toTrack normalizes one Song. BPM converts seconds-between-beats → bpm; key/bpm prefer Scan.
func (s xmlSong) toTrack() Track {
	return Track{
		Path:      s.FilePath,
		Title:     strings.TrimSpace(s.Tags.Title),
		Artist:    strings.TrimSpace(s.Tags.Author),
		Album:     strings.TrimSpace(s.Tags.Album),
		Genre:     strings.TrimSpace(s.Tags.Genre),
		Key:       firstNonEmpty(s.Scan.Key, s.Tags.Key),
		BPM:       bpmFromSeconds(s.Scan.Bpm, s.Tags.Bpm),
		LengthSec: parseLen(s.Infos.SongLength),
		PlayCount: parseInt(s.Infos.PlayCount),
	}
}

// ParseDatabase streams a database.xml, returning one Track per <Song>.
func ParseDatabase(r io.Reader) ([]Track, error) {
	dec := xml.NewDecoder(r)
	var tracks []Track
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return tracks, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Song" {
			continue
		}
		var xs xmlSong
		if err := dec.DecodeElement(&xs, &se); err != nil {
			return tracks, err
		}
		tracks = append(tracks, xs.toTrack())
	}
	return tracks, nil
}

// DefaultDir returns %USERPROFILE%\Documents\VirtualDJ (the main collection location).
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents", "VirtualDJ"), nil
}

// DatabaseFiles returns every existing database.xml to merge: dir\database.xml plus each
// per-drive <X>:\VirtualDJ\database.xml (Windows). Only existing files; deduped.
func DatabaseFiles(dir string) []string {
	var files []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" {
			return
		}
		key := strings.ToLower(p)
		if seen[key] {
			return
		}
		if _, err := os.Stat(p); err == nil {
			seen[key] = true
			files = append(files, p)
		}
	}
	if dir != "" {
		add(filepath.Join(dir, "database.xml"))
	}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			add(filepath.Join(string(c)+":\\", "VirtualDJ", "database.xml"))
		}
	}
	return files
}

// LoadCollection parses + merges every database.xml under dir (empty → DefaultDir),
// deduping by Path. Per-file open/parse errors are skipped; an error is returned only if
// nothing could be read at all.
func LoadCollection(dir string) ([]Track, error) {
	if dir == "" {
		d, err := DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	var all []Track
	seen := map[string]bool{}
	var firstErr error
	for _, f := range DatabaseFiles(dir) {
		fh, err := os.Open(f)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		tracks, perr := ParseDatabase(fh)
		_ = fh.Close()
		if perr != nil && firstErr == nil {
			firstErr = perr
		}
		for _, t := range tracks {
			if t.Path == "" {
				all = append(all, t)
				continue
			}
			key := strings.ToLower(t.Path)
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, t)
		}
	}
	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

// bpmFromSeconds converts the first positive seconds-between-beats value to bpm (60/v).
func bpmFromSeconds(vals ...string) float64 {
	for _, v := range vals {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
			return 60.0 / f
		}
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func parseLen(s string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0
	}
	return int(f + 0.5)
}

func parseInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
