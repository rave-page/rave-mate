// Package unityproj integrates rave-mate with Unity / VRChat avatar projects:
// discover projects (via the VRChat Creator Companion list), install the embedded
// `page.rave.mate` UPM editor plugin into one, and export recorded motion takes as
// Unity AnimationClips. Pure stdlib; no cgo, no networking.
package unityproj

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"

	"rave.page/mate/internal/vrmotion"
)

//go:embed all:unitypkg
var pkgFS embed.FS

// PluginName is the UPM package id installed under <project>/Packages/.
const PluginName = "page.rave.mate"

// Project describes a candidate Unity project on disk.
type Project struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Valid     bool   `json:"valid"`     // is a Unity project
	HasPlugin bool   `json:"hasPlugin"` // page.rave.mate installed
}

// VCCSettingsPath returns the VRChat Creator Companion settings.json path
// (%LOCALAPPDATA%/VRChatCreatorCompanion/settings.json); empty if LOCALAPPDATA unset.
func VCCSettingsPath() string {
	la := os.Getenv("LOCALAPPDATA")
	if la == "" {
		return ""
	}
	return filepath.Join(la, "VRChatCreatorCompanion", "settings.json")
}

// DiscoverVCCProjects reads the VCC settings and returns its userProjects list
// (nil on any error).
func DiscoverVCCProjects() []string {
	p := VCCSettingsPath()
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var s struct {
		UserProjects []string `json:"userProjects"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	return s.UserProjects
}

// IsUnityProject reports whether dir has both Assets/ and ProjectSettings/ subdirs.
func IsUnityProject(dir string) bool {
	return isDir(filepath.Join(dir, "Assets")) && isDir(filepath.Join(dir, "ProjectSettings"))
}

// Inspect describes the project at dir.
func Inspect(dir string) Project {
	return Project{
		Path:      dir,
		Name:      filepath.Base(dir),
		Valid:     IsUnityProject(dir),
		HasPlugin: isDir(pluginDir(dir)),
	}
}

// InstallPlugin removes any existing page.rave.mate package in projectDir and
// copies the embedded plugin tree into <projectDir>/Packages/page.rave.mate.
func InstallPlugin(projectDir string) error {
	if !IsUnityProject(projectDir) {
		return os.ErrInvalid
	}
	dst := pluginDir(projectDir)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return copyEmbedded("unitypkg", dst)
}

// MotionExportDir is where takes are written: <projectDir>/Assets/rave.page/Motion.
func MotionExportDir(projectDir string) string {
	return filepath.Join(projectDir, "Assets", "rave.page", "Motion")
}

// ExportTake writes rec as a Unity .anim into the project's motion dir and returns
// the file path. Name falls back to "take".
func ExportTake(projectDir string, rec *vrmotion.Recording) (string, error) {
	dir := MotionExportDir(projectDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := "take"
	if rec != nil && rec.Name != "" {
		name = rec.Name
	}
	path := filepath.Join(dir, name+".anim")
	if err := vrmotion.ExportAnim(path, rec, nil); err != nil {
		return "", err
	}
	return path, nil
}

// WorldSyncSourcesPath is <projectDir>/Assets/rave.page/WorldSync/sources.json -
// the file-based handoff the editor plugin reads (mirrors the motion-take flow).
func WorldSyncSourcesPath(projectDir string) string {
	return filepath.Join(projectDir, "Assets", "rave.page", "WorldSync", "sources.json")
}

// WriteWorldSyncSources writes the world-sync source URLs into the project.
func WriteWorldSyncSources(projectDir string, data []byte) error {
	if !IsUnityProject(projectDir) {
		return os.ErrInvalid
	}
	p := WorldSyncSourcesPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// pluginDir is <projectDir>/Packages/page.rave.mate.
func pluginDir(projectDir string) string {
	return filepath.Join(projectDir, "Packages", PluginName)
}

// isDir reports whether p exists and is a directory.
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// copyEmbedded recursively copies embedded tree root → dst on the real fs.
func copyEmbedded(root, dst string) error {
	return fs.WalkDir(pkgFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := pkgFS.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
