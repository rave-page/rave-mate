// Package vfx locates + drives the rave-mate-vfx effects child (native/zigvfx):
// open-standard video effects (frei0r now, ISF next) hosted out-of-process so a
// plugin crash never reaches the daemon or a worker.
package vfx

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"rave.page/mate/internal/appdir"
	"rave.page/mate/internal/sysexec"
)

// ExeName is the child binary's base name.
const ExeName = "rave-mate-vfx"

func exeFile() string {
	if runtime.GOOS == "windows" {
		return ExeName + ".exe"
	}
	return ExeName
}

// ExePath locates the child: RAVE_MATE_VFX_EXE override, else the install dir
// (appdir - NOT os.Executable(): workers relaunch from per-role hardlinks in the
// proc cache dir, which has no sidecars, see #49), else the in-repo zig-out
// (dev/test runs).
func ExePath() (string, error) {
	if p := os.Getenv("RAVE_MATE_VFX_EXE"); p != "" {
		return p, nil
	}
	if p := appdir.SidecarPath(exeFile()); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; d = filepath.Dir(d) {
			p := filepath.Join(d, "native", "zigvfx", "zig-out", "bin", exeFile())
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return "", errors.New(exeFile() + " not found (build native/zigvfx or set RAVE_MATE_VFX_EXE)")
}

// Param is one plugin parameter as reported by --list.
type Param struct {
	Name string     `json:"name"`
	Type string     `json:"type"` // bool|double|color|position|string
	Def  [3]float64 `json:"def"`  // double/bool [0]; position x,y; color r,g,b
}

// Plugin is one discovered effect.
type Plugin struct {
	Kind       string  `json:"kind"`       // frei0r|isf
	PlugType   string  `json:"type"`       // filter|generator|mixer
	Categories string  `json:"categories"` // ISF CATEGORIES joined ", " ("" = none)
	Ref        string  `json:"ref"`        // absolute plugin path
	Name       string  `json:"name"`
	Author     string  `json:"author"`
	Desc       string  `json:"desc"`
	Params     []Param `json:"params"`
}

//go:embed isfseed/*.fs
var isfSeed embed.FS

// PluginDirs returns (and creates) the per-user effect plugin dirs; the ISF dir
// is seeded once with the bundled MIT starter shaders (existing files never
// overwritten - users may edit or delete them). The fetched Vidvox pack subdir
// joins the scan when present (--list doesn't recurse).
func PluginDirs(configDir string) []string {
	frei0rDir := filepath.Join(configDir, "vfx", "frei0r")
	isfDir := filepath.Join(configDir, "vfx", "isf")
	_ = os.MkdirAll(frei0rDir, 0o755)
	if err := os.MkdirAll(isfDir, 0o755); err == nil {
		seedISF(isfDir)
	}
	dirs := []string{frei0rDir, isfDir}
	if st, err := os.Stat(filepath.Join(isfDir, PackDirName)); err == nil && st.IsDir() {
		dirs = append(dirs, filepath.Join(isfDir, PackDirName))
	}
	// bundled frei0r plugins ship in <install>/frei0r (built by scripts/build-frei0r.sh,
	// packaged by the installer); user drop-ins in the config dir take the same scan
	if p := appdir.SidecarPath("frei0r"); p != "" {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			dirs = append(dirs, p)
		}
	}
	return dirs
}

// ISFDir returns the per-user ISF shader dir (created).
func ISFDir(configDir string) string {
	d := filepath.Join(configDir, "vfx", "isf")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// seedISF writes each bundled shader if absent.
func seedISF(dir string) {
	entries, err := isfSeed.ReadDir("isfseed")
	if err != nil {
		return
	}
	for _, e := range entries {
		dst := filepath.Join(dir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if data, err := isfSeed.ReadFile("isfseed/" + e.Name()); err == nil {
			_ = os.WriteFile(dst, data, 0o644)
		}
	}
}

// List runs `--list` discovery over dirs.
func List(ctx context.Context, dirs []string) ([]Plugin, error) {
	exe, err := ExePath()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, "--list", strings.Join(dirs, ";"))
	sysexec.Hide(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("vfx --list: %w (%s)", err, lastLine(errb.String()))
	}
	var parsed struct {
		Plugins []Plugin `json:"plugins"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("vfx --list output: %w", err)
	}
	return parsed.Plugins, nil
}

// Fx is one chain stage handed to the child (Off entries already dropped,
// refs resolved to absolute paths).
type Fx struct {
	Kind   string             `json:"kind"`
	Ref    string             `json:"ref"`
	Params map[string]float64 `json:"params,omitempty"`
}

// Chain is the spec consumed by --frame/--pipe.
type Chain struct {
	W   int     `json:"w"`
	H   int     `json:"h"`
	FPS float64 `json:"fps"`
	Fx  []Fx    `json:"fx"`
}

// WriteFile marshals the chain into dir (caller removes the temp dir).
func (c Chain) WriteFile(dir string) (string, error) {
	if c.W <= 0 || c.H <= 0 {
		return "", errors.New("chain w/h missing")
	}
	if c.FPS <= 0 {
		c.FPS = 30
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "chain.json")
	return p, os.WriteFile(p, raw, 0o644)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
