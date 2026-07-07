// Package waveform resolves per-track waveform peak overviews for the now-playing overlay
// renderers (native deck card + browser overlay). It decodes the loaded track to mono PCM via
// ffmpeg and folds it into uint8 max-abs peak buckets, cached on disk keyed by the deck's stable
// ArtKey - mirroring internal/overlayart.
//
// Extraction is fully async + single-flight: the render loop calls Get on every frame and never
// blocks on ffmpeg. A miss kicks off a background decode and Get returns not-ready until the
// peaks land, after which every later frame is an in-memory hit. This is the "generate it while
// the track is playing, then add it as soon as it's available" behaviour the overlay wants.
package waveform

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/sysexec"
)

const (
	source        = "waveform"
	decodeRate    = 8000    // mono decode sample rate (matches the worker probe peaks)
	bucketsPerSec = 60      // peak resolution - enough detail for a tight zoom window
	minBuckets    = 1024    //
	maxBuckets    = 1 << 16 // cap memory + the on-disk overview size
	cacheExt      = ".wave" //
	missCool      = 30 * time.Second
	decodeTimeout = 180 * time.Second // long DJ tracks decode slower; never block a render loop on it
)

// Peaks is a resolved waveform overview: max-abs uint8 buckets spanning DurationMs.
type Peaks struct {
	Data       []byte
	DurationMs uint32
}

// Resolver caches waveform overviews on disk keyed by DeckSnapshot.ArtKey, with an in-memory hot
// cache + single-flight decode + a negative cache (so a broken decode isn't retried every frame).
type Resolver struct {
	dir string
	log *logbus.Bus

	pathFn func(artist, title string) (string, bool) // resolve a file path by name (library), optional

	mu       sync.Mutex
	mem      map[string]*Peaks      // fileKey → ready peaks
	inflight map[string]struct{}    // fileKey → a decode is running
	misses   map[string]time.Time   // fileKey → last failed decode
	resolved map[string]resolvedKey // artKey → resolved file path (cached so we query the DB once)
}

// resolvedKey caches a per-deck-track path lookup. A negative ("" path) is retried after missCool;
// a positive is permanent (a track's file doesn't change mid-session).
type resolvedKey struct {
	path string
	at   time.Time
}

// New builds a resolver caching overviews into dir (created on first write).
func New(dir string, log *logbus.Bus) *Resolver {
	return &Resolver{
		dir:      dir,
		log:      log,
		mem:      map[string]*Peaks{},
		inflight: map[string]struct{}{},
		misses:   map[string]time.Time{},
		resolved: map[string]resolvedKey{},
	}
}

// resolvePath returns the deck's audio file. It PREFERS the library's canonical path (by
// artist+title, cached) because a live deck's own path is unreliable - Traktor reports a
// volume-stripped path like "/Music/…" without the drive letter, which ffmpeg can't open. The
// deck's path is only a fallback for tracks not in the library. Mirrors the cover-art resolver,
// which resolves by name rather than trusting the deck path.
func (r *Resolver) resolvePath(d session.DeckSnapshot) string {
	if p := r.libPath(d); p != "" {
		return p
	}
	return d.Path
}

// libPath is the cached library lookup by artist+title (queried at most once per track).
func (r *Resolver) libPath(d session.DeckSnapshot) string {
	if r.pathFn == nil || d.ArtKey == "" {
		return ""
	}
	r.mu.Lock()
	if rk, ok := r.resolved[d.ArtKey]; ok && (rk.path != "" || time.Since(rk.at) < missCool) {
		r.mu.Unlock()
		return rk.path
	}
	r.mu.Unlock()
	p, ok := r.pathFn(d.Artist, d.Title)
	if !ok {
		p = ""
	}
	r.mu.Lock()
	r.resolved[d.ArtKey] = resolvedKey{path: p, at: time.Now()}
	r.mu.Unlock()
	return p
}

// SetPathResolver installs a library lookup that maps a track's artist+title to a local file path,
// so peaks can be generated before the live deck reports its own path (Traktor sends it ~90s in -
// the same window the cover-art resolver bridges). Optional.
func (r *Resolver) SetPathResolver(fn func(artist, title string) (string, bool)) { r.pathFn = fn }

// fileKey is the cache key: a hash of the resolved audio file path. Keying by the FILE (not the
// deck's ArtKey) means the cache survives the ArtKey flip when Traktor's path finally arrives
// (meta-based → path-based) - the same file resolves to the same key, so no re-decode + no gap.
func fileKey(path string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	return strconv.FormatUint(h.Sum64(), 16)
}

// CachePath is the on-disk overview path for key (whether or not it exists yet). The browser
// overlay's /peaks handler streams this file directly.
func (r *Resolver) CachePath(key string) string {
	if key == "" {
		return ""
	}
	return filepath.Join(r.dir, key+cacheExt)
}

// Get returns cached peaks for the deck's track. ready=false kicks off a background decode and
// returns immediately - it never blocks the caller on ffmpeg. The audio file is the deck's path,
// or (before the deck reports it) a unique library match by artist+title.
func (r *Resolver) Get(d session.DeckSnapshot) (*Peaks, bool) {
	path := r.resolvePath(d)
	if path == "" {
		return nil, false // no file yet - caller shows a placeholder
	}
	key := fileKey(path)
	r.mu.Lock()
	if p, ok := r.mem[key]; ok {
		r.mu.Unlock()
		return p, true
	}
	if _, busy := r.inflight[key]; busy {
		r.mu.Unlock()
		return nil, false
	}
	if t, missed := r.misses[key]; missed && time.Since(t) < missCool {
		r.mu.Unlock()
		return nil, false
	}
	r.mu.Unlock()

	if p, ok := r.loadDisk(key); ok {
		r.mu.Lock()
		r.mem[key] = p
		r.mu.Unlock()
		return p, true
	}
	r.startDecode(key, path)
	return nil, false
}

// startDecode claims the single-flight slot and spawns the ffmpeg decode (no-op if already busy).
func (r *Resolver) startDecode(key, path string) {
	r.mu.Lock()
	if _, busy := r.inflight[key]; busy {
		r.mu.Unlock()
		return
	}
	r.inflight[key] = struct{}{}
	r.mu.Unlock()

	go func() {
		p, err := decode(path)
		r.mu.Lock()
		delete(r.inflight, key)
		if err != nil {
			r.misses[key] = time.Now()
			r.mu.Unlock()
			if r.log != nil {
				r.log.Debug(source, "waveform decode failed", map[string]any{"error": err.Error()})
			}
			return
		}
		r.mem[key] = p
		r.mu.Unlock()
		if err := r.writeDisk(key, p); err != nil && r.log != nil {
			r.log.Debug(source, "waveform cache write failed", map[string]any{"error": err.Error()})
		}
	}()
}

// decode runs ffmpeg to mono s16le PCM and folds it into ~bucketsPerSec max-abs buckets.
func decode(path string) (*Peaks, error) {
	bin, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, fmt.Errorf("ffmpeg not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), decodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-loglevel", "error",
		"-i", path, "-map", "a:0", "-ac", "1", "-ar", strconv.Itoa(decodeRate), "-f", "s16le", "-")
	sysexec.Hide(cmd)
	sysexec.LowPriority(cmd) // background decode - keep it off the foreground's back
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pcm, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("decode %q: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	samples := len(pcm) / 2
	if samples == 0 {
		return nil, fmt.Errorf("no audio decoded")
	}
	durSec := float64(samples) / decodeRate
	n := min(max(int(durSec*bucketsPerSec), minBuckets), maxBuckets)
	return &Peaks{Data: bucketPeaks(pcm, n), DurationMs: uint32(durSec * 1000)}, nil
}

// bucketPeaks folds little-endian s16 PCM into n uint8 max-abs buckets (mirrors worker.bucketPeaks).
func bucketPeaks(pcm []byte, n int) []byte {
	samples := len(pcm) / 2
	if samples < n {
		n = samples
	}
	if n <= 0 {
		return nil
	}
	out := make([]byte, n)
	for b := 0; b < n; b++ {
		lo, hi := b*samples/n, (b+1)*samples/n
		peak := 0
		for i := lo; i < hi; i++ {
			v := int(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
		if peak > 32767 {
			peak = 32767
		}
		out[b] = byte(peak >> 7) // 0..32767 → 0..255
	}
	return out
}

// on-disk overview: [uint32 LE durationMs][peak bytes]. The browser /peaks handler serves it raw.

func (r *Resolver) loadDisk(key string) (*Peaks, bool) {
	path := r.CachePath(key)
	if path == "" {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 4 {
		return nil, false
	}
	return &Peaks{DurationMs: binary.LittleEndian.Uint32(data[:4]), Data: data[4:]}, true
}

func (r *Resolver) writeDisk(key string, p *Peaks) error {
	path := r.CachePath(key)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	buf := make([]byte, 4+len(p.Data))
	binary.LittleEndian.PutUint32(buf[:4], p.DurationMs)
	copy(buf[4:], p.Data)
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(buf); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > 0 {
			return nil // a peer writer won - fine
		}
		return err
	}
	return nil
}
