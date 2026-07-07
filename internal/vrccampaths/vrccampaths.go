// Package vrccampaths organizes + annotates VRChat camera (dolly) path files. VRChat saves these
// as JSON in Documents\VRChat\CameraPaths with timestamp names and NO world reference - so we pair
// each file's save time with the world the user was in then (via the vrcloc timeline) to name +
// group them per world, summarize them, and load one back into VRChat over OSC (/dolly/Import).
//
// Schema confirmed from a real file: a top-level JSON array of Point objects.
package vrccampaths

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/osc"
	"rave.page/mate/internal/vrcloc"
)

// Vec3 is a position (meters) or rotation (euler degrees) triple. Lowercase JSON keys match
// VRChat's dolly file format on both decode (case-insensitive) and encode (exact).
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Point is one camera-path keyframe (VRChat's exact JSON field names; case-insensitive match).
type Point struct {
	IsLocal         bool
	Position        Vec3
	Rotation        Vec3 // euler degrees
	FocalDistance   float64
	Aperture        float64
	Hue             float64
	Saturation      float64
	Lightness       float64
	LookAtMeXOffset float64
	LookAtMeYOffset float64
	Zoom            float64
	Exposure        float64
	Speed           float64
	Duration        float64
	Index           int
	PathIndex       int
}

// LoadPoints parses a camera-path JSON file into its keyframes.
func LoadPoints(file string) ([]Point, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var pts []Point
	if err := json.Unmarshal(data, &pts); err != nil {
		return nil, fmt.Errorf("parse camera path: %w", err)
	}
	return pts, nil
}

// PlayerRelativeFolder groups camera paths whose points are player-relative (IsLocal) rather than
// world-anchored - they're not tied to any world, so they get their own clearly-named folder.
const PlayerRelativeFolder = "Player-Relative Paths"

// Path is a summarized camera-path file + the world it belongs to (resolved from the timeline).
type Path struct {
	File        string    `json:"file"`        // absolute path
	Name        string    `json:"name"`        // display name (file base, no ext)
	SavedAt     time.Time `json:"savedAt"`     // file mtime
	Points      int       `json:"points"`      // keyframe count
	DurationSec float64   `json:"durationSec"` // sum of per-point Duration
	Local       bool      `json:"local"`       // points are player-relative (IsLocal), not world-anchored
	WorldID     string    `json:"worldId,omitempty"`
	WorldName   string    `json:"worldName,omitempty"`
}

// Folder is the organize/group folder for this path: player-relative paths group together;
// world-anchored paths group by world (sanitized; "Unknown World" when unknown).
func (p Path) Folder() string {
	if p.Local {
		return PlayerRelativeFolder
	}
	if p.WorldName != "" {
		return vrcloc.SanitizeName(p.WorldName, "Unknown World")
	}
	return "Unknown World"
}

// summarize builds a Path (without world) from a file + its info. Local is true when the path has
// keyframes and every one is player-relative (IsLocal).
func summarize(file string, info os.FileInfo) Path {
	p := Path{
		File:    file,
		Name:    strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)),
		SavedAt: info.ModTime(),
	}
	if pts, err := LoadPoints(file); err == nil {
		p.Points = len(pts)
		local := len(pts) > 0
		for _, pt := range pts {
			p.DurationSec += pt.Duration
			if !pt.IsLocal {
				local = false
			}
		}
		p.Local = local
	}
	return p
}

// DefaultDir is VRChat's camera-path directory on Windows.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Documents", "VRChat", "CameraPaths")
}

// Scan lists camera-path files under dir (recursively, so already-organized paths in
// Organized/<world>/ still appear alongside loose ones), summarized + world-tagged. World comes from
// the organized sidecar when present (survives timeline loss), else the timeline by save time. Files
// are deduped by base name (an organized copy + its loose original collapse to one, keeping the entry
// that knows its world). Newest first.
func Scan(dir string, tl *vrcloc.Timeline) []Path {
	var out []Path
	seen := map[string]int{} // lower base name → index in out
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		low := strings.ToLower(name)
		if !strings.HasSuffix(low, ".json") || strings.HasSuffix(low, ".meta.json") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		path := summarize(p, info)
		if sc, ok := readSidecar(p); ok && sc.WorldName != "" {
			path.WorldID, path.WorldName = sc.WorldID, sc.WorldName
		} else if tl != nil {
			if loc, ok := tl.At(path.SavedAt); ok {
				path.WorldID, path.WorldName = loc.WorldID, loc.WorldName
			}
		}
		if idx, dup := seen[low]; dup {
			if out[idx].WorldName == "" && path.WorldName != "" {
				out[idx] = path // keep the copy that knows its world
			}
			return nil
		}
		seen[low] = len(out)
		out = append(out, path)
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].SavedAt.After(out[j].SavedAt) })
	return out
}

// readSidecar reads the <file>.meta.json world sidecar written by Organize, if present.
func readSidecar(pathFile string) (Sidecar, bool) {
	data, err := os.ReadFile(pathFile + ".meta.json")
	if err != nil {
		return Sidecar{}, false
	}
	var sc Sidecar
	if json.Unmarshal(data, &sc) != nil {
		return Sidecar{}, false
	}
	return sc, true
}

// WorldFolder is the per-world organize folder name for a path (resolved from the timeline by save
// time). "Unknown World" when the world isn't known.
func WorldFolder(savedAt time.Time, tl *vrcloc.Timeline) string {
	if tl != nil {
		if loc, ok := tl.At(savedAt); ok && loc.WorldName != "" {
			return vrcloc.SanitizeName(loc.WorldName, "Unknown World")
		}
	}
	return "Unknown World"
}

const organizedDir = "Organized"

// Organizer sorts path files into Organized/<world>/ under the camera-paths dir, writing a sidecar
// with the world id/name + save time so the association survives even if the timeline is lost.
type Organizer struct {
	dir  string
	tl   *vrcloc.Timeline
	move bool
	log  func(string)
}

// New builds an organizer. move=true relocates files; else copies.
func New(dir string, tl *vrcloc.Timeline, move bool, log func(string)) *Organizer {
	return &Organizer{dir: dir, tl: tl, move: move, log: log}
}

// Sidecar is the per-path metadata we persist alongside an organized file.
type Sidecar struct {
	WorldID      string    `json:"worldId,omitempty"`
	WorldName    string    `json:"worldName,omitempty"`
	SavedAt      time.Time `json:"savedAt"`
	WorldVersion int       `json:"worldVersion,omitempty"` // filled when the VRChat API is queried
}

// Organize sorts every path file in dir into Organized/<world>/. Returns the count organized.
func (o *Organizer) Organize() int {
	ents, err := os.ReadDir(o.dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		src := filepath.Join(o.dir, e.Name())
		p := summarize(src, info)
		if o.tl != nil {
			if loc, ok := o.tl.At(p.SavedAt); ok {
				p.WorldID, p.WorldName = loc.WorldID, loc.WorldName
			}
		}
		destDir := filepath.Join(o.dir, organizedDir, p.Folder())
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			continue
		}
		dest := filepath.Join(destDir, e.Name())
		if err := placeFile(src, dest, o.move); err != nil {
			continue
		}
		o.writeSidecar(dest, info.ModTime())
		n++
		if o.log != nil {
			o.log(fmt.Sprintf("%s → %s", e.Name(), p.Folder()))
		}
	}
	return n
}

func (o *Organizer) writeSidecar(dest string, savedAt time.Time) {
	sc := Sidecar{SavedAt: savedAt}
	if o.tl != nil {
		if loc, ok := o.tl.At(savedAt); ok {
			sc.WorldID, sc.WorldName = loc.WorldID, loc.WorldName
		}
	}
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(dest+".meta.json", data, 0o644)
}

// Load sends a camera path to VRChat over OSC: /dolly/Import <absolute path> then /dolly/Play true.
// Requires OSC enabled in VRChat. addr is the OSC target (default 127.0.0.1:9000 when empty).
func Load(addr, pathFile string) error {
	c, err := osc.New(addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	abs, err := filepath.Abs(pathFile)
	if err != nil {
		return err
	}
	if err := c.Send("/dolly/Import", abs); err != nil {
		return err
	}
	return c.Send("/dolly/Play", true)
}

func placeFile(src, dst string, move bool) error {
	if move {
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if move {
		return os.Remove(src)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
