// Package cuesheet parses the .cue sidecars rave-mate writes for recordings back into a track list.
package cuesheet

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Track is one CUE entry; Start derived from INDEX 01 (mm:ss:ff, 75 frames/sec).
type Track struct {
	Num       int
	Title     string
	Performer string
	Start     time.Duration
}

// Sheet is a parsed CUE: header PERFORMER/TITLE/FILE plus per-track entries.
type Sheet struct {
	Performer string
	Title     string
	File      string
	Tracks    []Track
}

// Parse reads a CUE sheet; tolerant of unknown lines, errors only on malformed INDEX/TRACK.
func Parse(r io.Reader) (Sheet, error) {
	var sh Sheet
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	cur := -1 // index into sh.Tracks of the current track, -1 = still in header
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		kw, rest := splitKeyword(line)
		switch kw {
		case "PERFORMER":
			if v, ok := quoted(rest); ok {
				if cur < 0 {
					sh.Performer = v
				} else {
					sh.Tracks[cur].Performer = v
				}
			}
		case "TITLE":
			if v, ok := quoted(rest); ok {
				if cur < 0 {
					sh.Title = v
				} else {
					sh.Tracks[cur].Title = v
				}
			}
		case "FILE":
			if v, ok := quoted(rest); ok {
				sh.File = v
			}
		case "TRACK":
			num, err := parseTrack(rest)
			if err != nil {
				return Sheet{}, err
			}
			sh.Tracks = append(sh.Tracks, Track{Num: num})
			cur = len(sh.Tracks) - 1
		case "INDEX":
			if cur < 0 {
				return Sheet{}, fmt.Errorf("cuesheet: INDEX before any TRACK")
			}
			d, ok, err := parseIndex01(rest)
			if err != nil {
				return Sheet{}, err
			}
			if ok {
				sh.Tracks[cur].Start = d
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Sheet{}, err
	}
	return sh, nil
}

// ParseFile parses the CUE sheet at path.
func ParseFile(path string) (Sheet, error) {
	f, err := os.Open(path)
	if err != nil {
		return Sheet{}, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// splitKeyword splits a line into its leading uppercase keyword and the remainder.
func splitKeyword(line string) (kw, rest string) {
	i := strings.IndexByte(line, ' ')
	if i < 0 {
		return strings.ToUpper(line), ""
	}
	return strings.ToUpper(line[:i]), strings.TrimSpace(line[i+1:])
}

// quoted extracts the value of a `"value"` token; ok=false if not double-quoted.
func quoted(s string) (string, bool) {
	a := strings.IndexByte(s, '"')
	if a < 0 {
		return "", false
	}
	b := strings.IndexByte(s[a+1:], '"')
	if b < 0 {
		return "", false
	}
	return s[a+1 : a+1+b], true
}

// parseTrack parses `NN AUDIO` → track number.
func parseTrack(rest string) (int, error) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, fmt.Errorf("cuesheet: malformed TRACK %q", rest)
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("cuesheet: malformed TRACK number %q: %w", fields[0], err)
	}
	return n, nil
}

// parseIndex01 parses `01 mm:ss:ff`; ok=false (no error) for non-01 indices.
func parseIndex01(rest string) (time.Duration, bool, error) {
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return 0, false, fmt.Errorf("cuesheet: malformed INDEX %q", rest)
	}
	if fields[0] != "01" {
		return 0, false, nil
	}
	d, err := parseMSF(fields[1])
	if err != nil {
		return 0, false, err
	}
	return d, true, nil
}

// parseMSF converts mm:ss:ff to a duration (mm uncapped, 75 frames/sec).
func parseMSF(s string) (time.Duration, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("cuesheet: malformed INDEX time %q", s)
	}
	mm, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("cuesheet: bad minutes %q: %w", parts[0], err)
	}
	ss, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("cuesheet: bad seconds %q: %w", parts[1], err)
	}
	ff, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, fmt.Errorf("cuesheet: bad frames %q: %w", parts[2], err)
	}
	if mm < 0 || ss < 0 || ff < 0 {
		return 0, fmt.Errorf("cuesheet: negative INDEX time %q", s)
	}
	return time.Duration(mm)*time.Minute + time.Duration(ss)*time.Second + time.Duration(ff)*(time.Second/75), nil
}
