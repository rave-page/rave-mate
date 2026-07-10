package train

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CheckpointInfo describes one fine-tuned checkpoint on disk.
type CheckpointInfo struct {
	Path string
	Name string // file base name without .ckpt
	At   time.Time
}

// ListCheckpoints returns the .ckpt files under modelsDir, newest first.
func ListCheckpoints(modelsDir string) []CheckpointInfo {
	ents, err := os.ReadDir(modelsDir)
	if err != nil {
		return nil
	}
	var out []CheckpointInfo
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ckpt") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, CheckpointInfo{
			Path: filepath.Join(modelsDir, e.Name()),
			Name: strings.TrimSuffix(e.Name(), ".ckpt"),
			At:   info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}
