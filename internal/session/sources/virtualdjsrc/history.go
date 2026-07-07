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

// newestHistory returns the newest .txt/.m3u tracklist in dir by mtime.
func newestHistory(dir string) (string, time.Time, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}, false
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if !strings.HasSuffix(name, ".txt") && !strings.HasSuffix(name, ".m3u") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = filepath.Join(dir, e.Name()), info.ModTime()
		}
	}
	if best == "" {
		return "", time.Time{}, false
	}
	return best, bestMod, true
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
	return splitArtistTitle(lastLine)
}
