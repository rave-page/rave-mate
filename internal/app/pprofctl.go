package app

// Profile-grade attribution over ctl: runtime/pprof CPU/heap captures written to .pprof files
// + inline goroutine dumps, local (ctl pprof-cpu/pprof-heap/goroutines) and via a paired peer
// (ctl remote-pprof-*, riding the remotectl app.pprof* methods). For when `ctl perf` shows
// churn none of the instrumented subsystems owns.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/sysexec"
)

const (
	pprofDefaultSeconds = 10
	pprofMaxSeconds     = 60
	// Remote capture blocks the remotectl handler, which serveTimeout kills at 60s - clamp
	// tighter so capture + encode + reply always fit.
	pprofRemoteMaxSeconds = 45
)

// clampPprofSeconds normalizes a requested CPU-capture duration into [1, max] (≤0 ⇒ default).
func clampPprofSeconds(s, max int) int {
	if s <= 0 {
		return pprofDefaultSeconds
	}
	if s > max {
		return max
	}
	return s
}

// capturePprofCPU profiles this process's CPU for d (ctx cancel stops early). Errors if a CPU
// profile is already running (concurrent ctl calls).
func capturePprofCPU(ctx context.Context, d time.Duration) ([]byte, error) {
	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		return nil, err
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
	pprof.StopCPUProfile()
	return buf.Bytes(), nil
}

// capturePprofHeap snapshots the heap profile (GC first so it reflects live objects).
func capturePprofHeap() ([]byte, error) {
	runtime.GC()
	var buf bytes.Buffer
	if err := pprof.WriteHeapProfile(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// goroutineDump renders the full goroutine profile (debug=1: stacks grouped with counts).
func goroutineDump() string {
	p := pprof.Lookup("goroutine")
	if p == nil {
		return "goroutine profile unavailable"
	}
	var buf bytes.Buffer
	_ = p.WriteTo(&buf, 1)
	return strings.TrimRight(buf.String(), "\n")
}

// pprofFilePath is <configDir>/<name> (bare name = daemon cwd if the config dir is unavailable).
func pprofFilePath(name string) string {
	dir, err := config.Dir()
	if err != nil {
		return name
	}
	return filepath.Join(dir, name)
}

// pprofTopSummary shells to `go tool pprof -top -nodecount=15` when a Go toolchain is on PATH.
// Best-effort: "" if go is absent or the tool fails - the .pprof file stays the artifact.
func pprofTopSummary(path string) string {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "tool", "pprof", "-top", "-nodecount=15", path)
	sysexec.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// writeProfileReply persists profile bytes at path and formats the ctl reply: "ok <path> (<n>
// bytes)" + a top summary (best-effort local go-tool run when none was supplied).
func writeProfileReply(path string, data []byte) string {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "error: write: " + err.Error()
	}
	reply := fmt.Sprintf("ok %s (%d bytes)", path, len(data))
	if sum := pprofTopSummary(path); sum != "" {
		return reply + "\n" + sum
	}
	return reply + "\nanalyze: go tool pprof -top " + path + " (go toolchain not on PATH here)"
}

// ── Control surface (local) ───────────────────────────────────────────────────

// PprofCPU profiles the daemon's CPU for seconds (default 10, cap 60), writes
// <configDir>/rave-mate_cpu.pprof (overwrite) and returns path + top-15 summary.
func (c *appControl) PprofCPU(seconds int) string {
	sec := clampPprofSeconds(seconds, pprofMaxSeconds)
	data, err := capturePprofCPU(context.Background(), time.Duration(sec)*time.Second)
	if err != nil {
		return "error: " + err.Error()
	}
	return writeProfileReply(pprofFilePath("rave-mate_cpu.pprof"), data)
}

// PprofHeap writes <configDir>/rave-mate_heap.pprof (overwrite) and returns path + top summary.
func (c *appControl) PprofHeap() string {
	data, err := capturePprofHeap()
	if err != nil {
		return "error: " + err.Error()
	}
	return writeProfileReply(pprofFilePath("rave-mate_heap.pprof"), data)
}

// Goroutines returns the full goroutine dump inline.
func (c *appControl) Goroutines() string { return goroutineDump() }

// ── remotectl.Profiler (this box as the controlled peer) ──────────────────────

// CPUProfile implements remotectl.Profiler: remote-clamped capture that stops early on ctx cancel.
func (c *appControl) CPUProfile(ctx context.Context, seconds int) ([]byte, error) {
	sec := clampPprofSeconds(seconds, pprofRemoteMaxSeconds)
	return capturePprofCPU(ctx, time.Duration(sec)*time.Second)
}

// HeapProfile implements remotectl.Profiler.
func (c *appControl) HeapProfile() ([]byte, error) { return capturePprofHeap() }

// ── Control surface (remote: drive a paired peer) ─────────────────────────────

// remotePeerClient resolves nodeID (""=first connected) to a typed peer client; non-"" second
// return is the error reply.
func (c *appControl) remotePeerClient(nodeID string) (*remotectl.Client, string) {
	if c.peerMgr == nil || c.remoteCtl == nil {
		return nil, "error: peer link unavailable"
	}
	if nodeID == "" {
		for _, p := range c.peerMgr.Connections() {
			if p.Status == peerlink.StatusConnected {
				nodeID = p.NodeID
				break
			}
		}
	}
	if nodeID == "" {
		return nil, "error: no connected peer (run `ctl list-peers`)"
	}
	client := remotectl.NewClient(c.remoteCtl, nodeID)
	if client == nil {
		return nil, "error: invalid peer"
	}
	return client, ""
}

// RemotePprofCPU captures a paired peer's CPU profile (seconds clamped ≤45) and writes it to
// localPath (the ctl caller's cwd). Reply: path + size + best-effort local top summary.
func (c *appControl) RemotePprofCPU(nodeID, localPath string, seconds int) string {
	client, errStr := c.remotePeerClient(nodeID)
	if errStr != "" {
		return errStr
	}
	sec := clampPprofSeconds(seconds, pprofRemoteMaxSeconds)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(sec)*time.Second+15*time.Second)
	defer cancel()
	data, err := client.PprofCPU(ctx, sec)
	if err != nil {
		return "error: " + err.Error()
	}
	return writeProfileReply(localPath, data)
}

// RemotePprofHeap captures a paired peer's heap profile and writes it to localPath.
func (c *appControl) RemotePprofHeap(nodeID, localPath string) string {
	client, errStr := c.remotePeerClient(nodeID)
	if errStr != "" {
		return errStr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	data, err := client.PprofHeap(ctx)
	if err != nil {
		return "error: " + err.Error()
	}
	return writeProfileReply(localPath, data)
}

// RemoteGoroutines fetches a paired peer's goroutine dump (inline text).
func (c *appControl) RemoteGoroutines(nodeID string) string {
	client, errStr := c.remotePeerClient(nodeID)
	if errStr != "" {
		return errStr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	text, err := client.Goroutines(ctx)
	if err != nil {
		return "error: " + err.Error()
	}
	return text
}
