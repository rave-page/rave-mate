// Package overlayart resolves cover art for the OBS overlay renderers (native PNG, browser
// overlay, obs-websocket). It extracts embedded art from the loaded track file (no ffmpeg -
// pure tag read), normalizes it to a cached JPEG, and falls back to an injectable source
// (the rave.page artwork API) when a track has no embedded picture. Results are cached on
// disk keyed by the deck's stable ArtKey, so a repeated track change is a free cache hit and
// a miss is remembered (negative cache) to avoid re-probing every overlay tick.
package overlayart

import (
	"bytes"
	"context"
	"image"
	_ "image/gif" // decode support
	"image/jpeg"
	_ "image/png" // decode support
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/dhowden/tag"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/sysexec"
)

const (
	source   = "overlayart"
	maxDim   = 512              // cover thumbnails for overlays; plenty at any deck-card size
	jpegQ    = 88               //
	missCool = 20 * time.Second // re-probe a known miss at most this often (short: a transient miss
	//                            on an on-air deck self-heals fast; a real no-art track lands a
	//                            persistent DB "none" marker that short-circuits before this anyway)
	cacheExt = ".jpg"
	jpegMIME = "image/jpeg"
	noneMIME = "none" // store marker: file probed, no usable embedded art
)

// Fallback fetches art bytes for a track the local file didn't provide (e.g. the rave.page
// artwork API by resolved track id). ok=false when nothing is available.
type Fallback func(ctx context.Context, d session.DeckSnapshot) ([]byte, bool)

// Store persists extracted cover art across restarts (the library DB), so art survives and each
// file is probed at most once - populated at collection import + lazily on first play. Optional:
// without it the resolver still works off the disk cache + in-memory negative cache.
type Store interface {
	// Get returns stored art for an audio file path. analyzed=true when the path was processed; a
	// hit with mime "none" / empty data is a definitive no-art result (not "unknown").
	Get(path string) (data []byte, mime string, analyzed bool)
	// GetByMeta resolves cover bytes by a UNIQUE artist+title match (used before Traktor sends the
	// file path). ok=false on 0, >1, or no-art matches - the caller then waits for the path.
	GetByMeta(artist, title string) (data []byte, ok bool)
	// Put records extracted art (data nil + mime "none" = definitive no-art marker). artist/title
	// are stored so a later play resolves by name before its path arrives.
	Put(path, artist, title string, data []byte, mime, src string)
}

// Resolver caches normalized cover art on disk keyed by DeckSnapshot.ArtKey, backed by an optional
// persistent Store (keyed by file path) that is the source of truth across restarts.
type Resolver struct {
	dir      string
	log      *logbus.Bus
	fallback Fallback
	store    Store

	mu       sync.Mutex
	misses   map[string]time.Time // artKey → last miss (in-session negative cache)
	inflight map[string]struct{}  // artKey → an extraction is in progress (single-flight)
}

// New builds a resolver caching into dir (created on first write).
func New(dir string, log *logbus.Bus) *Resolver {
	return &Resolver{dir: dir, log: log, misses: map[string]time.Time{}}
}

// SetFallback installs the no-local-art fallback (e.g. the artwork API). Optional.
func (r *Resolver) SetFallback(fn Fallback) { r.fallback = fn }

// SetStore installs the persistent art store (the library DB). Optional.
func (r *Resolver) SetStore(s Store) { r.store = s }

// CachePath is the on-disk path art for key would live at (whether or not it exists yet).
func (r *Resolver) CachePath(key string) string {
	if key == "" {
		return ""
	}
	return filepath.Join(r.dir, key+cacheExt)
}

// Ensure makes sure art for the deck's track is cached on disk and returns the cache file path.
// ok=false when the track has no embedded art and no fallback provided one. Lookup order: disk
// cache → persistent Store (by file path) → extract (embedded → ffmpeg → fallback). A fresh
// extraction is normalized, written back to the Store (so it survives restarts + isn't re-probed)
// and mirrored to the disk cache that the overlay HTTP server serves from.
func (r *Resolver) Ensure(ctx context.Context, d session.DeckSnapshot) (string, bool) {
	key := d.ArtKey
	if key == "" {
		return "", false
	}
	path := r.CachePath(key)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return path, true
	}

	// Persistent store (source of truth): a hit means already analyzed - serve it or honour the
	// definitive no-art marker. Mirror image bytes to the disk cache so /art serving is a file hit.
	if r.store != nil && d.Path != "" {
		if data, mime, analyzed := r.store.Get(d.Path); analyzed {
			if mime == noneMIME || len(data) == 0 {
				return "", false
			}
			if err := r.writeCache(path, data); err != nil {
				r.log.Warn(source, "mirror store art to cache failed", map[string]any{"error": err.Error()})
				return "", false
			}
			return path, true
		}
	}

	// Name-based resolution: before Traktor sends the file path (≈first 90s of a track), the deck
	// has no path but does have artist+title. A UNIQUE name match in the store IS the cover - serve
	// it now instead of waiting for the path (user's "one exact match = canonical" rule).
	if r.store != nil {
		if data, ok := r.store.GetByMeta(d.Artist, d.Title); ok && len(data) > 0 {
			if err := r.writeCache(path, data); err == nil {
				return path, true
			}
		}
	}

	if r.recentlyMissed(key) {
		return "", false
	}

	// Single-flight the (expensive) extraction: the browser overlay retries every SSE tick and the
	// PNG/Spout sinks resolve the same key too, so without this they'd each spawn a concurrent ffmpeg
	// on the same file - a herd that thrashes IO until one wins (the multi-minute stall). Losers
	// return a quick miss and pick up the winner's result (disk/DB hit) on the next tick.
	if !r.beginExtract(key) {
		return "", false
	}
	defer r.endExtract(key)
	// A winner may have finished between our checks above and acquiring - re-check disk + store.
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return path, true
	}
	if r.store != nil && d.Path != "" {
		if data, mime, analyzed := r.store.Get(d.Path); analyzed {
			if mime == noneMIME || len(data) == 0 {
				return "", false
			}
			if err := r.writeCache(path, data); err == nil {
				return path, true
			}
			return "", false
		}
	}

	data, src, ok := r.extract(ctx, d)
	if !ok {
		// Negative-cache in memory only. Do NOT persist a "none" marker from the live path: an
		// extraction can fail transiently (timeout under load) on a file that DOES have art, and a
		// false "none" would 404 it until overwritten. The backfill (EnsurePath, no herd) owns the
		// authoritative persistent no-art determination.
		r.markMiss(key)
		return "", false
	}
	norm, ok := normalize(data)
	if !ok {
		r.markMiss(key) // bytes existed but unusable - could be transient, don't persist a marker
		return "", false
	}
	if r.store != nil && d.Path != "" {
		r.store.Put(d.Path, d.Artist, d.Title, norm, jpegMIME, src)
	}
	if err := r.writeCache(path, norm); err != nil {
		r.log.Warn(source, "cache art failed", map[string]any{"error": err.Error()})
		return "", false
	}
	return path, true
}

// EnsurePath extracts + persists cover art for an audio file path into the Store if not already
// analyzed. Used by collection import/analysis to pre-populate the DB so first play is an instant
// hit. Returns true when usable art bytes were stored (false = no-art marker written / no store).
func (r *Resolver) EnsurePath(ctx context.Context, filePath, artist, title string) bool {
	if r.store == nil || filePath == "" {
		return false
	}
	if _, _, analyzed := r.store.Get(filePath); analyzed {
		return false
	}
	data, src, ok := extractFile(ctx, filePath)
	if !ok {
		r.store.Put(filePath, artist, title, nil, noneMIME, "")
		return false
	}
	norm, ok := normalize(data)
	if !ok {
		r.store.Put(filePath, artist, title, nil, noneMIME, "")
		return false
	}
	r.store.Put(filePath, artist, title, norm, jpegMIME, src)
	return true
}

// extract returns raw (un-normalized) picture bytes + their source: embedded art from the file
// first, then the injectable fallback. ok=false when neither yields a picture.
func (r *Resolver) extract(ctx context.Context, d session.DeckSnapshot) ([]byte, string, bool) {
	if data, src, ok := extractFile(ctx, d.Path); ok {
		return data, src, true
	}
	if r.fallback != nil {
		if b, ok := r.fallback(ctx, d); ok && len(b) > 0 {
			return b, "api", true
		}
	}
	return nil, "", false
}

// extractFile pulls embedded cover bytes from a local audio file: pure-Go tag read first
// (ID3v2/MP4/FLAC/Ogg), then ffmpeg for formats/tag layouts the pure reader misses (what Traktor
// reads). ok=false (with no path or no embedded picture).
func extractFile(ctx context.Context, filePath string) ([]byte, string, bool) {
	if filePath == "" {
		return nil, "", false
	}
	if pic, ok := embeddedArt(filePath); ok {
		return pic, "embedded", true
	}
	if pic, ok := ffmpegArt(ctx, filePath); ok {
		return pic, "ffmpeg", true
	}
	return nil, "", false
}

// beginExtract claims the single-flight slot for key; false = another goroutine already holds it.
func (r *Resolver) beginExtract(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inflight == nil {
		r.inflight = map[string]struct{}{}
	}
	if _, busy := r.inflight[key]; busy {
		return false
	}
	r.inflight[key] = struct{}{}
	return true
}

func (r *Resolver) endExtract(key string) {
	r.mu.Lock()
	delete(r.inflight, key)
	r.mu.Unlock()
}

func (r *Resolver) recentlyMissed(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.misses[key]
	return ok && time.Since(t) < missCool
}

func (r *Resolver) markMiss(key string) {
	r.mu.Lock()
	r.misses[key] = time.Now()
	r.mu.Unlock()
}

// writeCache atomically writes the cache file. Multiple sinks (browser/PNG/Spout) AND the /art
// HTTP handler resolve the same key concurrently, so the temp MUST be unique per writer - a shared
// "<path>.tmp" races (one goroutine renames a temp another still holds → "access denied", which
// failed the resolve → 404 → negative-cache lockout). A dest that already exists (a peer writer
// won) is success, not an error.
func (r *Resolver) writeCache(path string, data []byte) error {
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return nil // a concurrent writer already produced it
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
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
			return nil // lost the race but the file is there - fine
		}
		return err
	}
	return nil
}

// embeddedArt reads the first embedded cover picture from an audio file (ID3v2/MP4/FLAC/Ogg)
// via a pure tag read - no subprocess. ok=false when the file has no embedded picture.
func embeddedArt(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil, false
	}
	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, false
	}
	return pic.Data, true
}

// ffmpegArt extracts the first embedded cover picture via ffmpeg - robust across the formats +
// tag layouts the pure-Go reader misses (what Traktor reads). No-op when ffmpeg isn't available.
func ffmpegArt(ctx context.Context, path string) ([]byte, bool) {
	bin, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, false
	}
	tmp, err := os.CreateTemp("", "ravemate-art-*.jpg")
	if err != nil {
		return nil, false
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpName) }()

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second) // large lossless files under backfill IO load
	defer cancel()
	// -map 0:v:0 = the first (cover) video stream; -frames:v 1 = one image; re-encode to jpeg.
	// "0:v:0?" - optional: no hard error when the file has no embedded picture (just no output).
	cmd := exec.CommandContext(cctx, bin, "-hide_banner", "-loglevel", "error",
		"-i", path, "-an", "-map", "0:v:0?", "-frames:v", "1", "-y", tmpName)
	sysexec.Hide(cmd)
	sysexec.LowPriority(cmd) // backfill may probe many files - keep it off the foreground's back
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	data, err := os.ReadFile(tmpName)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// normalize decodes any common picture and re-encodes a ≤maxDim JPEG. A small webp (stdlib
// can't decode it) passes through unchanged. ok=false when the bytes can't be made usable.
func normalize(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return nil, false
	}
	if http.DetectContentType(data) == "image/webp" {
		return data, true // stdlib can't decode; serve as-is (renderers handle webp)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	img = scaleDown(img, maxDim)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQ}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// scaleDown area-average resizes img so max(w,h) ≤ maxDim (no-op when already small).
func scaleDown(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return img
	}
	var ow, oh int
	if w >= h {
		ow, oh = maxDim, max(h*maxDim/w, 1)
	} else {
		ow, oh = max(w*maxDim/h, 1), maxDim
	}
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		sy0, sy1 := b.Min.Y+y*h/oh, b.Min.Y+(y+1)*h/oh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < ow; x++ {
			sx0, sx1 := b.Min.X+x*w/ow, b.Min.X+(x+1)*w/ow
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var rr, gg, bb, aa, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					pr, pg, pb, pa := img.At(sx, sy).RGBA()
					rr += uint64(pr)
					gg += uint64(pg)
					bb += uint64(pb)
					aa += uint64(pa)
					n++
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i] = uint8(rr / n >> 8)
			dst.Pix[i+1] = uint8(gg / n >> 8)
			dst.Pix[i+2] = uint8(bb / n >> 8)
			dst.Pix[i+3] = uint8(aa / n >> 8)
		}
	}
	return dst
}
