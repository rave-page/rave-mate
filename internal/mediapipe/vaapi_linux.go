//go:build linux

package mediapipe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/sysexec"
)

// vaapi_linux.go - deliberate VA-API render-node selection. A box can expose several render
// nodes (discrete GPU + iGPU); not every node's driver has the encode entrypoint, so we don't
// blindly use /dev/dri/renderD128. We enumerate the nodes and pick the first whose h264_vaapi
// test-encode actually passes, once, cached. RAVE_VAAPI_DEVICE overrides for a manual pin.

var (
	vaapiOnce sync.Once
	vaapiNode string // resolved render node ("" = none usable)
)

// renderNodes lists /dev/dri/renderD* sorted ascending (renderD128, renderD129, ...).
func renderNodes() []string {
	m, _ := filepath.Glob("/dev/dri/renderD*")
	sort.Strings(m)
	return m
}

// ensureVAAPINode resolves (once) the render node to encode on and returns it. RAVE_VAAPI_DEVICE
// overrides; otherwise the first render node whose h264_vaapi test-encode passes wins - a node
// whose driver really has the VA-API encode entrypoint, not a guess. "" when none works (no
// VA-API, no render node, or a software-only ffmpeg).
func ensureVAAPINode(ctx context.Context, ffmpeg string) string {
	vaapiOnce.Do(func() {
		if env := strings.TrimSpace(os.Getenv("RAVE_VAAPI_DEVICE")); env != "" {
			if _, err := os.Stat(env); err == nil {
				vaapiNode = env
				return
			}
		}
		for _, n := range renderNodes() {
			if vaapiNodeEncodes(ctx, ffmpeg, n) {
				vaapiNode = n
				return
			}
		}
	})
	return vaapiNode
}

// vaapiDevice returns the cached render-node pick without probing ("" if unresolved/none).
func vaapiDevice() string { return vaapiNode }

// vaapiNodeEncodes test-encodes 3 frames of h264_vaapi on one render node; true = the node's
// driver exposes the VA-API encode entrypoint.
func vaapiNodeEncodes(ctx context.Context, ffmpeg, node string) bool {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, ffmpeg, vaapiTestEncodeArgs(node, "h264_vaapi")...)
	sysexec.Hide(cmd)
	cmd.Cancel = func() error { sysexec.KillTree(cmd.Process); return nil }
	if err := cmd.Start(); err != nil {
		return false
	}
	sysexec.AssignToJobClass(cmd.Process, sysexec.JobBatch) // deferrable diagnostic: stay CPU-capped
	return cmd.Wait() == nil
}
