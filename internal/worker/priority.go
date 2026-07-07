package worker

import (
	"os"
	"os/exec"
	"strings"

	"rave.page/mate/internal/sysexec"
)

const (
	workerPriorityEnv        = "RAVE_MATE_WORKER_PRIORITY"
	workerPriorityBackground = "background"
)

func workerEnv(background bool) []string {
	env := os.Environ()
	out := env[:0]
	prefix := workerPriorityEnv + "="
	for _, v := range env {
		if !strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	if background {
		out = append(out, prefix+workerPriorityBackground)
	}
	return out
}

func backgroundWorker() bool {
	return os.Getenv(workerPriorityEnv) == workerPriorityBackground
}

func prepareCmd(c *exec.Cmd) {
	sysexec.Hide(c)
	if backgroundWorker() {
		sysexec.LowPriority(c)
	}
}
