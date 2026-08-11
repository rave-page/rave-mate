// Package caprecover registers capture files a crash orphaned. A daemon death mid-set leaves
// three shapes behind (2026-08-10 incident): a finished OBS recording whose finish event was
// never observed (row never written), a killed native ffmpeg capture (no finalize, no row,
// FLAC STREAMINFO left blank so duration probes N/A), and an icecast start-row with ended_at
// still empty. The startup sweep scans known capture dirs for untracked media files, probes
// durations (repairing unfinalized FLAC headers), and registers them as UNLINKED rows -
// app.linkCaptures' relink then time-matches them to recorded sets.
package caprecover

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

const source = "caprecover"

const (
	settleAge      = 2 * time.Minute     // younger files may still be written - next sweep gets them
	maxAge         = 45 * 24 * time.Hour // older untracked files are archive, not crash fallout
	maxPerSweep    = 20                  // newest-first cap per sweep (dropped rest is logged)
	minBytes       = 1 << 20             // ignore stubs
	maxRepairBytes = 8 << 30             // FLAC re-encode above this would run for ages - log a hint instead
	probeTimeout   = 30 * time.Second
	repairTimeout  = 30 * time.Minute
)

var videoExts = map[string]bool{".mp4": true, ".mkv": true, ".mov": true}
var audioExts = map[string]bool{".flac": true, ".wav": true, ".mp3": true, ".m4a": true}

// captureNameRe: exactly the timestamped basename OBS (default) and audiorec write. The dirs
// also hold user-made cuts/exports ("… 23-00-16-cut.mp4", "MySet.flac") - registering those as
// recovered captures pollutes the Unlinked list, so discovery is opt-in by naming convention.
var captureNameRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}-\d{2}-\d{2}$`)

// Sweep backfills crash-open rows (ended_at from the file's mtime) and registers untracked
// capture files as unlinked set recordings. extraDirs joins the parent dirs of existing rows
// (the native capture dir, whose first capture may predate any row). Returns rows changed;
// the caller re-runs its relink when > 0. Blocking (probes + possible FLAC repair) - run off
// the event loop.
func Sweep(ctx context.Context, log *logbus.Bus, lib *libdb.DB, extraDirs []string) int {
	if lib == nil {
		return 0
	}
	rows, err := lib.ListSetRecordings(500)
	if err != nil {
		log.Warn(source, "list set recordings failed", map[string]any{"error": err.Error()})
		return 0
	}
	known := map[string]bool{}
	dirs := map[string]string{} // normPath key → one concrete spelling (rows may mix separators)
	for _, r := range rows {
		known[normPath(r.Path)] = true
		if d := filepath.Dir(r.Path); d != "." && d != "" {
			dirs[normPath(d)] = d
		}
	}
	for _, d := range extraDirs {
		if strings.TrimSpace(d) != "" {
			dirs[normPath(d)] = d
		}
	}

	now := time.Now()
	changed := 0

	// Backfill rows a crash left open: capture start was persisted, the end never came. The
	// file's mtime IS the moment the last byte was written - the real capture end.
	for _, r := range rows {
		if !r.EndedAt.IsZero() {
			continue
		}
		fi, err := os.Stat(r.Path)
		if err != nil {
			continue // file gone; row stays invisible, nothing to recover
		}
		if now.Sub(fi.ModTime()) < settleAge {
			continue // still being written (a live capture) - not ours
		}
		r.EndedAt = fi.ModTime()
		r.Bytes = fi.Size()
		if err := lib.SaveSetRecording(r); err != nil {
			log.Warn(source, "backfill capture end failed", map[string]any{"id": r.ID, "error": err.Error()})
			continue
		}
		changed++
		log.Info(source, "backfilled crash-open capture end from file mtime", map[string]any{"id": r.ID, "path": r.Path, "ended": r.EndedAt.Format(time.RFC3339)})
	}

	// Discover untracked files in the capture dirs.
	type cand struct {
		path string
		mod  time.Time
		size int64
	}
	var cands []cand
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !captureName(e.Name()) {
				continue
			}
			p := filepath.Join(d, e.Name())
			if known[normPath(p)] {
				continue
			}
			fi, err := e.Info()
			if err != nil || fi.Size() < minBytes || now.Sub(fi.ModTime()) < settleAge ||
				now.Sub(fi.ModTime()) > maxAge {
				continue
			}
			cands = append(cands, cand{p, fi.ModTime(), fi.Size()})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	if len(cands) > maxPerSweep {
		log.Info(source, "capping recovery sweep - older untracked files skipped this run", map[string]any{"skipped": len(cands) - maxPerSweep})
		cands = cands[:maxPerSweep]
	}

	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		dur := probeDuration(ctx, c.path)
		if dur <= 0 && strings.EqualFold(filepath.Ext(c.path), ".flac") {
			// A killed capture leaves FLAC STREAMINFO blank; -c copy would keep it verbatim,
			// so the repair is a lossless re-ENCODE (bit-identical audio, rebuilt header).
			if repairFlac(ctx, log, c.path, c.size, c.mod) {
				dur = probeDuration(ctx, c.path)
				if fi, err := os.Stat(c.path); err == nil {
					c.size = fi.Size()
				}
			}
		}
		if dur <= 0 {
			log.Warn(source, "untracked capture file has no probeable duration - cannot time-place it", map[string]any{"path": c.path})
			continue
		}
		ended := c.mod
		started := ended.Add(-time.Duration(dur * float64(time.Second)))
		sr := libdb.SetRecording{
			Path: c.path, StartedAt: started, EndedAt: ended, Bytes: c.size,
			Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(c.path)), "."),
		}
		if videoExts[strings.ToLower(filepath.Ext(c.path))] {
			sr.ID = "obs_" + strconv.FormatInt(started.UnixNano(), 10)
			sr.Kind = libdb.SetKindOBS
			sr.Mount = "obs"
		} else {
			sr.ID = "native-rcvr-" + strconv.FormatInt(started.UnixNano(), 10)
			sr.Kind = libdb.SetKindNative
		}
		if err := lib.SaveSetRecording(sr); err != nil {
			log.Warn(source, "register recovered capture failed", map[string]any{"path": c.path, "error": err.Error()})
			continue
		}
		changed++
		log.Info(source, "recovered crash-orphaned capture file", map[string]any{
			"path": c.path, "kind": sr.Kind, "durSec": int(dur),
			"started": started.Format(time.RFC3339), "ended": ended.Format(time.RFC3339)})
	}
	return changed
}

// captureExt reports whether name has a capture container extension.
func captureExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return videoExts[ext] || audioExts[ext]
}

// captureName reports whether name looks like a capture output (timestamp basename + capture
// extension) rather than a user-made cut/export sharing the dir.
func captureName(name string) bool {
	return captureExt(name) && captureNameRe.MatchString(strings.TrimSuffix(name, filepath.Ext(name)))
}

// normPath folds a path for identity comparison (Windows: case + separator insensitive).
func normPath(p string) string {
	return strings.ToLower(filepath.Clean(filepath.ToSlash(p)))
}

// probeDuration returns the media duration in seconds via ffprobe; 0 = unknown, -1 = no ffprobe.
func probeDuration(ctx context.Context, path string) float64 {
	ffprobe, ok := mediatools.Resolve("ffprobe")
	if !ok {
		return -1
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(pctx, ffprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	sysexec.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// repairFlac rebuilds path with a valid STREAMINFO (lossless re-encode), keeping the original
// as path+".orig" and restoring mod (the true capture end) on the result. False = untouched.
func repairFlac(ctx context.Context, log *logbus.Bus, path string, size int64, mod time.Time) bool {
	if size > maxRepairBytes {
		log.Warn(source, "unfinalized FLAC too large for auto-repair - re-encode it manually (ffmpeg -i in.flac -c:a flac out.flac)", map[string]any{"path": path, "bytes": size})
		return false
	}
	orig := path + ".orig"
	if _, err := os.Stat(orig); err == nil {
		log.Warn(source, "FLAC repair skipped - .orig sidecar already exists", map[string]any{"path": path})
		return false
	}
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return false
	}
	tmp := strings.TrimSuffix(path, filepath.Ext(path)) + ".repair.flac"
	rctx, cancel := context.WithTimeout(ctx, repairTimeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, ffmpeg, "-v", "error", "-y", "-i", path, "-c:a", "flac", "-compression_level", "5", tmp)
	sysexec.Hide(cmd)
	sysexec.BelowNormalPriority(cmd)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		log.Warn(source, "FLAC header repair failed", map[string]any{"path": path, "error": err.Error()})
		return false
	}
	if err := os.Rename(path, orig); err != nil {
		_ = os.Remove(tmp)
		log.Warn(source, "FLAC repair: keep-original rename failed", map[string]any{"path": path, "error": err.Error()})
		return false
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Rename(orig, path) // restore
		_ = os.Remove(tmp)
		log.Warn(source, "FLAC repair: swap failed", map[string]any{"path": path, "error": err.Error()})
		return false
	}
	_ = os.Chtimes(path, mod, mod) // mtime is the capture end - later sweeps derive times from it
	log.Info(source, "repaired unfinalized FLAC header (original kept as .orig)", map[string]any{"path": path})
	return true
}
