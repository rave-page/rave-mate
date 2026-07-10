package seratolib

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/serato"
)

// ReadDatabase reads `database V2` under seratoDir ("" = default _Serato_ dir) into the
// normalized library model. Track paths are resolved absolute (Serato stores them
// drive-relative on Windows).
func ReadDatabase(seratoDir string) (musiclib.Library, error) {
	dir, err := resolveDir(seratoDir)
	if err != nil {
		return musiclib.Library{}, err
	}
	tracks, _, err := serato.LoadCollection(dir)
	if err != nil {
		return musiclib.Library{}, err
	}
	lib := musiclib.Library{Source: musiclib.Source{App: "serato", Path: filepath.Join(dir, "database V2")}}
	for _, t := range tracks {
		lib.Tracks = append(lib.Tracks, musiclib.Track{
			Path:        resolvePath(dir, t.Path),
			Title:       t.Title,
			Artist:      t.Artist,
			Album:       t.Album,
			Genre:       t.Genre,
			Key:         t.Key,
			Comment:     t.Comment,
			BPM:         t.BPM,
			DurationSec: float64(t.LengthSec),
		})
	}
	return lib, nil
}

// ReadCrates reads Subcrates/*.crate under seratoDir into playlists (paths resolved absolute;
// nested crate names like "House%%Deep" become folder paths).
func ReadCrates(seratoDir string) ([]musiclib.Playlist, error) {
	dir, err := resolveDir(seratoDir)
	if err != nil {
		return nil, err
	}
	_, crates, err := serato.LoadCollection(dir)
	if err != nil {
		return nil, err
	}
	var out []musiclib.Playlist
	for _, c := range crates {
		pl := musiclib.Playlist{Name: c.Name}
		// Serato encodes crate nesting in the file name with "%%" separators.
		if folder, name, ok := splitCrateName(c.Name); ok {
			pl.Folder, pl.Name = folder, name
		}
		for _, p := range c.TrackPaths {
			pl.Paths = append(pl.Paths, resolvePath(dir, p))
		}
		out = append(out, pl)
	}
	return out, nil
}

// ReadLibrary reads tracks + crates in one call (the hub-merge import shape).
func ReadLibrary(seratoDir string) (musiclib.Library, error) {
	lib, err := ReadDatabase(seratoDir)
	if err != nil {
		return musiclib.Library{}, err
	}
	pls, err := ReadCrates(seratoDir)
	if err != nil {
		return musiclib.Library{}, err
	}
	lib.Playlists = pls
	return lib, nil
}

// AttachBeatgrids reads each track's file-level Serato beatgrid into Track.Beatgrid (grids
// live in the audio files, not in database V2). Unsupported/missing/broken files are skipped;
// returns how many tracks got a grid. This opens every file - call it deliberately.
func AttachBeatgrids(tracks []musiclib.Track) int {
	n := 0
	for i := range tracks {
		markers, found, err := ReadBeatgrid(tracks[i].Path)
		if err != nil || !found {
			continue
		}
		tracks[i].Beatgrid = markers
		n++
	}
	return n
}

// ApplyGridFixesSerato writes each fix's constant grid into the track FILE's Serato tag
// (Serato reads grids from files; there is nothing to rewrite in database V2). Refuses while
// Serato runs. Unsupported formats are skipped; per-file failures are joined into the
// returned error while the rest still apply. GridFixUpdate.Lock is ignored (Serato's
// grid-lock lives in database V2, which we deliberately do not rewrite).
func ApplyGridFixesSerato(seratoDir string, fixes []musiclib.GridFixUpdate) (musiclib.WritebackResult, error) {
	var res musiclib.WritebackResult
	if len(fixes) == 0 {
		return res, nil
	}
	if _, err := resolveDir(seratoDir); err != nil {
		return res, err
	}
	if seratoRunning() {
		return res, ErrSeratoRunning
	}
	var errs []error
	for _, fx := range fixes {
		if fx.Path == "" {
			continue
		}
		err := writeBeatgridFile(fx.Path, []musiclib.GridMarker{{PositionMs: fx.StartMs, BPM: fx.BPM}})
		if err != nil {
			if errors.Is(err, ErrUnsupported) {
				continue // e.g. .m4a - leave the file alone, not an apply failure
			}
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(fx.Path), err))
			continue
		}
		res.Updated++
	}
	return res, errors.Join(errs...)
}

// resolveDir validates seratoDir ("" = serato.DefaultDir()) and requires it to exist.
func resolveDir(seratoDir string) (string, error) {
	if seratoDir == "" {
		d, err := serato.DefaultDir()
		if err != nil {
			return "", err
		}
		seratoDir = d
	}
	st, err := os.Stat(seratoDir)
	if err != nil {
		return "", fmt.Errorf("seratolib: serato dir: %w", err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("seratolib: %s is not a directory", seratoDir)
	}
	return seratoDir, nil
}

// resolvePath makes a Serato drive-relative track path absolute against the volume holding
// the _Serato_ dir ("Users/x/Music/a.mp3" + "C:\...\_Serato_" -> "C:\Users\x\Music\a.mp3").
// Already-absolute paths (incl. other-volume Windows paths) pass through cleaned.
func resolvePath(seratoDir, p string) string {
	if p == "" {
		return ""
	}
	p = filepath.FromSlash(p)
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return filepath.Clean(p)
	}
	vol := filepath.VolumeName(filepath.Clean(seratoDir))
	return filepath.Clean(vol + string(filepath.Separator) + p)
}

// splitCrateName splits Serato's "%%"-nested crate name into (folder, leaf) - ok=false when flat.
func splitCrateName(name string) (folder, leaf string, ok bool) {
	const sep = "%%"
	i := strings.LastIndex(name, sep)
	if i < 0 {
		return "", name, false
	}
	return strings.ReplaceAll(name[:i], sep, "/"), name[i+len(sep):], true
}
