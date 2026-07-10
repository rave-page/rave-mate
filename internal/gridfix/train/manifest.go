package train

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"rave.page/mate/internal/gridfix"
)

// TrainOptions tunes one fine-tune run; zero values pick safe defaults.
type TrainOptions struct {
	BaseCheckpoint string  // "" = "final0" (or a prior fine-tuned .ckpt path)
	Epochs         int     // <=0 = 3
	LR             float64 // <=0 = 5e-5 (low: fine-tune, don't retrain)
	ValSplit       float64 // <=0 = 0.1, split by track
	Seed           int64   // <=0 = 1 (deterministic splits/shuffles)
}

func (o TrainOptions) withDefaults() TrainOptions {
	if o.BaseCheckpoint == "" {
		o.BaseCheckpoint = "final0"
	}
	if o.Epochs <= 0 {
		o.Epochs = 3
	}
	if o.LR <= 0 {
		o.LR = 5e-5
	}
	if o.ValSplit <= 0 {
		o.ValSplit = 0.1
	}
	if o.Seed <= 0 {
		o.Seed = 1
	}
	return o
}

// manifest is trainer.py's input document.
type manifest struct {
	Audio          []manifestAudio `json:"audio"`
	OutDir         string          `json:"outDir"`
	BaseCheckpoint string          `json:"baseCheckpoint"`
	Epochs         int             `json:"epochs"`
	LR             float64         `json:"lr"`
	ValSplit       float64         `json:"valSplit"`
	Seed           int64           `json:"seed"`
}

type manifestAudio struct {
	Path    string  `json:"path"`
	BPM     float64 `json:"bpm"`
	StartMs float64 `json:"startMs"`
}

// BuildManifest writes outDir/train-manifest.json from the verified grids,
// skipping entries whose audio file no longer exists (or has bpm<=0). Returns
// the manifest path and how many entries were skipped. Checkpoints land in
// outDir too.
func BuildManifest(verified []gridfix.VerifiedGrid, outDir string, opts TrainOptions) (manifestPath string, skipped int, err error) {
	opts = opts.withDefaults()
	m := manifest{
		OutDir:         outDir,
		BaseCheckpoint: opts.BaseCheckpoint,
		Epochs:         opts.Epochs,
		LR:             opts.LR,
		ValSplit:       opts.ValSplit,
		Seed:           opts.Seed,
	}
	for _, v := range verified {
		if v.BPM <= 0 {
			skipped++
			continue
		}
		if st, statErr := os.Stat(v.Path); statErr != nil || st.IsDir() {
			skipped++
			continue
		}
		m.Audio = append(m.Audio, manifestAudio{Path: v.Path, BPM: v.BPM, StartMs: v.StartMs})
	}
	if len(m.Audio) < 2 {
		return "", skipped, fmt.Errorf("gridfix train: need at least 2 verified tracks with audio present, have %d (%d skipped)", len(m.Audio), skipped)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", skipped, err
	}
	raw, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return "", skipped, err
	}
	p := filepath.Join(outDir, "train-manifest.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return "", skipped, err
	}
	return p, skipped, nil
}
