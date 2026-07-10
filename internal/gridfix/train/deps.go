package train

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"rave.page/mate/internal/sysexec"
)

// InstallTrainingDeps ensures the engine env can run trainer.py. Research
// verdict: the fine-tune loop needs ONLY what EnvManager.Install already ships
// (torch + beat-this with torchaudio/einops/soxr/rotary-embedding, soundfile);
// trainer.py deliberately avoids beat_this.model.pl_module, whose
// pytorch_lightning + mir_eval imports would be the extra deps. So this
// verifies imports instead of pip-installing anything.
func InstallTrainingDeps(ctx context.Context, python string, progress func(string)) error {
	if python == "" {
		return fmt.Errorf("gridfix train: no python configured")
	}
	emit := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	emit("verifying training dependencies...")
	check := "import torch, soundfile, soxr, einops, beat_this.inference, beat_this.model.loss, beat_this.preprocessing"
	cmd := exec.CommandContext(ctx, python, "-c", check)
	sysexec.Hide(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(out))
		if len(detail) > 300 {
			detail = detail[len(detail)-300:]
		}
		return fmt.Errorf("training deps missing - (re)install the beat engine first: %s", detail)
	}
	emit("training dependencies present (no extra installs needed)")
	return nil
}
