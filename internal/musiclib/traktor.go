package musiclib

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TraktorInstall is one discovered Traktor version directory.
type TraktorInstall struct {
	Version    string // "4.2.0"
	Dir        string // <Documents>/Native Instruments/Traktor 4.2.0
	Collection string // collection.nml path (may be "")
	HistoryDir string // History/ path (may be "")
}

// DiscoverTraktor finds every "Native Instruments/Traktor X.Y.Z" directory under the
// user's Documents, newest version first. Multiple versions are returned so the user can
// pick (or merge) - collections diverge across versions.
func DiscoverTraktor() ([]TraktorInstall, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, "Documents", "Native Instruments")
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var installs []TraktorInstall
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "Traktor ") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		in := TraktorInstall{
			Version: strings.TrimSpace(strings.TrimPrefix(e.Name(), "Traktor")),
			Dir:     dir,
		}
		if p := filepath.Join(dir, "collection.nml"); fileExists(p) {
			in.Collection = p
		}
		if p := filepath.Join(dir, "History"); dirExists(p) {
			in.HistoryDir = p
		}
		installs = append(installs, in)
	}
	sort.Slice(installs, func(i, j int) bool { return versionLess(installs[j].Version, installs[i].Version) })
	return installs, nil
}

// ── NML decode structs (only the fields we map) ──────────────────────────────

type nmlEntry struct {
	Title    string `xml:"TITLE,attr"`
	Artist   string `xml:"ARTIST,attr"`
	Location struct {
		Dir    string `xml:"DIR,attr"`
		File   string `xml:"FILE,attr"`
		Volume string `xml:"VOLUME,attr"`
	} `xml:"LOCATION"`
	Album struct {
		Title string `xml:"TITLE,attr"`
	} `xml:"ALBUM"`
	Info struct {
		Bitrate     int     `xml:"BITRATE,attr"`
		Genre       string  `xml:"GENRE,attr"`
		Label       string  `xml:"LABEL,attr"`
		Comment     string  `xml:"COMMENT,attr"`
		Key         string  `xml:"KEY,attr"`
		PlaytimeF   float64 `xml:"PLAYTIME_FLOAT,attr"`
		Playtime    int     `xml:"PLAYTIME,attr"`
		ImportDate  string  `xml:"IMPORT_DATE,attr"`
		ReleaseDate string  `xml:"RELEASE_DATE,attr"`
		LastPlayed  string  `xml:"LAST_PLAYED,attr"`
		PlayCount   int     `xml:"PLAYCOUNT,attr"`
		Ranking     int     `xml:"RANKING,attr"`
		FileSize    int     `xml:"FILESIZE,attr"`
	} `xml:"INFO"`
	Tempo struct {
		BPM float64 `xml:"BPM,attr"`
	} `xml:"TEMPO"`
	MusicalKey struct {
		Value string `xml:"VALUE,attr"` // "0"-"23"; string so absent ≠ 0 (C major)
	} `xml:"MUSICAL_KEY"`
	Cues []struct {
		Name   string  `xml:"NAME,attr"`
		Type   int     `xml:"TYPE,attr"`
		Start  float64 `xml:"START,attr"`
		Len    float64 `xml:"LEN,attr"`
		Hotcue int     `xml:"HOTCUE,attr"`
		Grid   struct {
			BPM float64 `xml:"BPM,attr"`
		} `xml:"GRID"` // TYPE-4 only: per-marker BPM (flexible grids carry one per segment)
	} `xml:"CUE_V2"`
	// History/playlist refs (present in PLAYLIST entries, not collection tracks)
	PrimaryKey struct {
		Key string `xml:"KEY,attr"`
	} `xml:"PRIMARYKEY"`
	Extended struct {
		Deck      int     `xml:"DECK,attr"`
		Duration  float64 `xml:"DURATION,attr"`
		StartDate int     `xml:"STARTDATE,attr"`
		StartTime int     `xml:"STARTTIME,attr"`
		Public    int     `xml:"PLAYEDPUBLIC,attr"`
	} `xml:"EXTENDEDDATA"`
}

func (e *nmlEntry) toTrack() Track {
	dur := e.Info.PlaytimeF
	if dur == 0 {
		dur = float64(e.Info.Playtime)
	}
	t := Track{
		Path:        resolveLocation(e.Location.Volume, e.Location.Dir, e.Location.File),
		Title:       e.Title,
		Artist:      e.Artist,
		Album:       e.Album.Title,
		Genre:       e.Info.Genre,
		Label:       e.Info.Label,
		Comment:     e.Info.Comment,
		Key:         e.Info.Key,
		BPM:         e.Tempo.BPM,
		DurationSec: dur,
		BitrateBps:  e.Info.Bitrate,
		FileSizeKB:  e.Info.FileSize,
		PlayCount:   e.Info.PlayCount,
		Rating:      e.Info.Ranking,
		ImportDate:  e.Info.ImportDate,
		ReleaseDate: e.Info.ReleaseDate,
		LastPlayed:  e.Info.LastPlayed,
	}
	if t.Key == "" && e.MusicalKey.Value != "" {
		if k, ok := KeyFromTraktorValue(atoiSafe(e.MusicalKey.Value)); ok {
			t.Key = k.Name()
		}
	}
	for _, c := range e.Cues {
		kind := traktorCueKind(c.Type, c.Hotcue)
		t.Cues = append(t.Cues, CuePoint{Name: c.Name, Kind: kind, Type: c.Type, StartMs: c.Start, LenMs: c.Len, Hotcue: c.Hotcue})
		if c.Type == 4 { // every TYPE-4 anchors the grid, padded (kind CueHot) or not
			bpm := c.Grid.BPM // per-marker GRID child BPM - flexible grids differ per segment
			if bpm <= 0 {
				bpm = e.Tempo.BPM
			}
			t.Beatgrid = append(t.Beatgrid, GridMarker{PositionMs: c.Start, BPM: bpm})
		}
	}
	return t
}

// ParseCollection streams a collection.nml (can be hundreds of MB), calling onTrack for
// each track ENTRY. Streaming (token-by-token) so memory stays flat regardless of size.
// Returns the number of tracks emitted.
func ParseCollection(r io.Reader, onTrack func(Track)) (int, error) {
	dec := xml.NewDecoder(r)
	n := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "ENTRY" {
			continue
		}
		var e nmlEntry
		if dec.DecodeElement(&e, &se) != nil {
			continue
		}
		if e.Location.File == "" {
			continue // not a track (e.g. a playlist ref entry)
		}
		onTrack(e.toTrack())
		n++
	}
}

// ParseHistory parses a Traktor history NML into a Session: the embedded COLLECTION holds
// each played track's metadata (LOCATION), and the PLAYLIST holds the play order +
// timestamps (PRIMARYKEY refs + EXTENDEDDATA). Small files - token streamed all the same.
func ParseHistory(name string, r io.Reader) (Session, error) {
	s := Session{Name: name}
	dec := xml.NewDecoder(r)
	// Two entry kinds share the ENTRY tag: COLLECTION entries carry the metadata (LOCATION + TITLE/…),
	// PLAYLIST entries carry the play order + timestamps (PRIMARYKEY ref + EXTENDEDDATA). Collect the
	// metadata keyed by the Traktor internal key (volume+dir+file, "/:"-joined - same format PRIMARYKEY
	// uses) and join after the stream, since ordering across the two blocks isn't guaranteed.
	type pending struct {
		pt  PlayedTrack
		key string // raw PRIMARYKEY, matched against a collection entry's location key
	}
	var plays []pending
	meta := map[string]nmlEntry{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return s, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "ENTRY" {
			continue
		}
		var e nmlEntry
		if dec.DecodeElement(&e, &se) != nil {
			continue
		}
		switch {
		case e.PrimaryKey.Key != "": // PLAYLIST entry (a play)
			plays = append(plays, pending{
				pt: PlayedTrack{
					Path:        resolveKey(e.PrimaryKey.Key),
					Deck:        e.Extended.Deck,
					StartedAt:   decodeHistoryTime(e.Extended.StartDate, e.Extended.StartTime),
					DurationSec: e.Extended.Duration,
					Public:      e.Extended.Public == 1,
				},
				key: e.PrimaryKey.Key,
			})
		case e.Location.File != "": // COLLECTION entry (the metadata)
			meta[e.Location.Volume+e.Location.Dir+e.Location.File] = e
		}
	}
	for _, p := range plays {
		if e, ok := meta[p.key]; ok {
			t := e.toTrack()
			p.pt.Title, p.pt.Artist, p.pt.Album, p.pt.Key, p.pt.BPM = t.Title, t.Artist, t.Album, t.Key, t.BPM
		}
		s.Played = append(s.Played, p.pt)
	}
	return s, nil
}

// ── path resolution (Traktor "/:" separators + VOLUME prefix) ────────────────

// resolveLocation rebuilds an OS path from a Traktor LOCATION (volume="C:",
// dir="/:Music/:Sub/:", file="t.mp3" → "C:\Music\Sub\t.mp3" on Windows).
func resolveLocation(volume, dir, file string) string {
	parts := splitNML(dir)
	parts = append(parts, file)
	sep := string(os.PathSeparator)
	if volume != "" {
		return volume + sep + strings.Join(parts, sep)
	}
	return sep + strings.Join(parts, sep)
}

// resolveKey rebuilds a path from a PRIMARYKEY ("B:/:Music/:Sub/:t.flac"): the first
// "/:"-segment carries the volume.
func resolveKey(key string) string {
	segs := splitNML(key)
	if len(segs) == 0 {
		return ""
	}
	volume := segs[0]
	rest := segs[1:]
	sep := string(os.PathSeparator)
	return volume + sep + strings.Join(rest, sep)
}

// splitNML splits a Traktor "/:"-delimited path into clean segments.
func splitNML(p string) []string {
	var out []string
	for seg := range strings.SplitSeq(p, "/:") {
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// ── helpers ──────────────────────────────────────────────────────────────────

func fileExists(p string) bool { fi, err := os.Stat(p); return err == nil && !fi.IsDir() }
func dirExists(p string) bool  { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

// versionLess compares dotted versions numerically (4.1.1 < 4.2.0 < 4.10.0).
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, nb := atoiSafe(pa[i]), atoiSafe(pb[i])
		if na != nb {
			return na < nb
		}
	}
	return len(pa) < len(pb)
}

// decodeHistoryTime turns Traktor history EXTENDEDDATA STARTDATE/STARTTIME into wall-clock.
// STARTDATE packs the date as (year<<16)|(month<<8)|day; STARTTIME is seconds since local
// midnight. Returns zero time if the stamp is absent or implausible. (Verified against a real
// history file: STARTDATE=132777475 STARTTIME=80690 → 2026-06-03 22:24:50.)
func decodeHistoryTime(startDate, startTime int) time.Time {
	if startDate == 0 {
		return time.Time{}
	}
	year := startDate >> 16
	month := (startDate >> 8) & 0xFF
	day := startDate & 0xFF
	if year < 1980 || year > 3000 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local).
		Add(time.Duration(startTime) * time.Second)
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
