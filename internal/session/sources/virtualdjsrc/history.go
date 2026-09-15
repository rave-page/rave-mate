package virtualdjsrc

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/session"
	"rave.page/mate/internal/virtualdj"
)

const (
	historyConfidence = 0.5
	historyPoll       = 3 * time.Second
)

// runHistory polls the newest tracklist file under <dir>\History and, on mtime change, emits
// the last logged track (master title/artist). Laggy - the lowest-confidence fallback.
func (s *Source) runHistory(ctx context.Context, emit func(session.Observation)) {
	dir := s.cfg.DatabaseDir
	if dir == "" {
		if d, err := virtualdj.DefaultDir(); err == nil {
			dir = d
		}
	}
	if dir == "" {
		s.log.Warn(historyTag, "no VirtualDJ dir; tracklist fallback idle", nil)
		return
	}
	histDir := filepath.Join(dir, "History")

	var lastFile, lastSig string
	var lastMod time.Time
	t := time.NewTicker(historyPoll)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		f, mod, ok := newestHistory(histDir)
		if !ok || (f == lastFile && !mod.After(lastMod)) {
			continue
		}
		lastFile, lastMod = f, mod

		artist, title := lastTrack(f)
		if title == "" && artist == "" {
			continue
		}
		sig := artist + "|" + title
		if sig == lastSig {
			continue
		}
		lastSig = sig

		fields := map[string]any{}
		if title != "" {
			fields[session.FieldTitle] = title
		}
		if artist != "" {
			fields[session.FieldArtist] = artist
		}
		emit(session.Observation{
			Source:     session.SourceVDJHistory,
			Scope:      session.Scope{Kind: session.ScopeMaster},
			Fields:     fields,
			Confidence: historyConfidence,
			Loaded:     true,
		})
	}
}

// newestHistory returns the newest tracklist in dir, preferring per-session .m3u files over
// any .txt. VDJ's default History\Tracklist.txt is a lifetime rolling log whose leading-
// timestamp line format splitArtistTitle mis-parses; the per-session .m3u carries clean
// #EXTINF metadata. Falls back to the newest .txt only when no .m3u exists.
func newestHistory(dir string) (string, time.Time, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}, false
	}
	var bestM3U, bestTxt string
	var modM3U, modTxt time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		isM3U := strings.HasSuffix(name, ".m3u")
		if !isM3U && !strings.HasSuffix(name, ".txt") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		full, mod := filepath.Join(dir, e.Name()), info.ModTime()
		if isM3U {
			if bestM3U == "" || mod.After(modM3U) {
				bestM3U, modM3U = full, mod
			}
		} else if bestTxt == "" || mod.After(modTxt) {
			bestTxt, modTxt = full, mod
		}
	}
	if bestM3U != "" {
		return bestM3U, modM3U, true
	}
	if bestTxt != "" {
		return bestTxt, modTxt, true
	}
	return "", time.Time{}, false
}

// lastTrack returns the artist/title of the last entry in a tracklist file. .txt: last
// non-comment "artist - title" line. .m3u: the last #EXTINF:dur,Artist - Title.
func lastTrack(path string) (artist, title string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer func() { _ = f.Close() }()

	isM3U := strings.HasSuffix(strings.ToLower(path), ".m3u")
	var lastLine string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if isM3U {
			if strings.HasPrefix(line, "#EXTINF:") {
				if i := strings.Index(line, ","); i >= 0 {
					lastLine = strings.TrimSpace(line[i+1:])
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		lastLine = line
	}
	if !isM3U {
		lastLine = stripLeadingTime(lastLine)
	}
	return splitArtistTitle(lastLine)
}

// stripLeadingTime removes a leading "HH:MM[:SS]" timestamp + its separator (" - ", " : ", or
// tab) from a Tracklist.txt line so splitArtistTitle doesn't read the time as the artist.
// VDJ's default Tracklist.txt line carries a leading time token (e.g. "22:15:03 : Artist -
// Title"); exact default format is unconfirmed without a live VDJ install, so this is tolerant
// of "-"/":"/tab separators. No leading time token → unchanged.
func stripLeadingTime(s string) string {
	t := strings.TrimLeft(s, " \t")
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' { // HH (1-2 digits)
		i++
	}
	if i == 0 || i > 2 || i >= len(t) || t[i] != ':' {
		return s
	}
	i++ // past first colon
	d := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' { // MM (exactly 2)
		i++
		d++
	}
	if d != 2 {
		return s
	}
	if i < len(t) && t[i] == ':' { // optional :SS (exactly 2)
		j, d2 := i+1, 0
		for j < len(t) && t[j] >= '0' && t[j] <= '9' {
			j++
			d2++
		}
		if d2 == 2 {
			i = j
		}
	}
	rest := t[i:]
	switch {
	case strings.HasPrefix(rest, " - "):
		return strings.TrimSpace(rest[3:])
	case strings.HasPrefix(rest, " : "):
		return strings.TrimSpace(rest[3:])
	case strings.HasPrefix(rest, "\t"):
		return strings.TrimSpace(rest[1:])
	}
	return s // time token but no recognized separator → leave untouched
}
