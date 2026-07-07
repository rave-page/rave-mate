package webui

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // register GIF decoder
	"image/jpeg"  // JPEG encode + decode
	_ "image/png" // register PNG decoder (VRChat screenshots)
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Loopback media endpoint for the unified player's embedded <video>: streams local files
// into the webview with HTTP Range support (http.ServeContent → seeking works). This is
// plain file IO - no decode, no frames - so it stays in-daemon (the featurehost isolation
// rule targets media processing, not sendfile). Security: 127.0.0.1 listener only, no
// directory access - only files explicitly registered by the player are reachable, each
// behind an unguessable random token. Token table is bounded (mpMediaMaxTokens, FIFO evict).
//
// The same listener also serves /img/<token>: a CACHED, RESIZED image endpoint. VRChat
// thumbnails (and other grids) register a (path, maxW) pair → a stable short URL the browser
// caches by URL, instead of re-embedding a base64 data-URI in every DOM patch (the old
// vrcMakeThumb path re-shipped kilobytes per photo per patch and upscaled a 160px nearest-
// neighbour raster → blur+lag). Decode+area-average downscale happens once per (path,maxW),
// off the UI thread, and the encoded JPEG is cached in-memory (bounded).

const (
	mpMediaMaxTokens = 32
	mpImgMaxTokens   = 256
	mpImgCacheMax    = 128
)

type imgReq struct {
	path string
	w    int // target max width in px; 0 = original size
}

type mpMediaSrv struct {
	mu     sync.Mutex
	port   int
	tokens map[string]string // token → absolute file path (raw stream)
	order  []string          // FIFO for eviction
	byPath map[string]string // path → token (stable URLs per file)

	imgTokens     map[string]imgReq // token → resize request
	imgByKey      map[string]string // key → token (stable per (path,w) OR per content hash)
	imgKeyByTok   map[string]string // token → key (reverse map so eviction removes the exact key)
	imgOrder      []string          // FIFO eviction of img tokens
	imgCache      map[string][]byte // token → encoded JPEG bytes
	imgCacheOrder []string          // FIFO eviction of cached bytes
}

var mpMediaFS = &mpMediaSrv{
	tokens: map[string]string{}, byPath: map[string]string{},
	imgTokens: map[string]imgReq{}, imgByKey: map[string]string{}, imgKeyByTok: map[string]string{}, imgCache: map[string][]byte{},
}

// randToken returns an unguessable 128-bit hex token ("" on RNG failure).
func randToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// ensureListenLocked lazily starts the loopback listener. Caller holds s.mu. Returns ok.
func (s *mpMediaSrv) ensureListenLocked(u *UI) bool {
	if s.port != 0 {
		return true
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		u.logErr("media http listen", err)
		return false
	}
	s.port = ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return true
}

// mpMediaURL returns a loopback URL streaming path ("" on failure). Lazily starts the
// listener; repeated calls for the same path reuse the token.
func (u *UI) mpMediaURL(path string) string {
	s := mpMediaFS
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ensureListenLocked(u) {
		return ""
	}
	if tok, ok := s.byPath[path]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d/m/%s", s.port, tok)
	}
	tok := randToken()
	if tok == "" {
		return ""
	}
	s.tokens[tok] = path
	s.byPath[path] = tok
	s.order = append(s.order, tok)
	if len(s.order) > mpMediaMaxTokens { // bounded: evict oldest registration
		old := s.order[0]
		s.order = s.order[1:]
		delete(s.byPath, s.tokens[old])
		delete(s.tokens, old)
	}
	return fmt.Sprintf("http://127.0.0.1:%d/m/%s", s.port, tok)
}

// imgURL returns a loopback URL serving path resized to maxW px wide (cached, JPEG). maxW<=0
// serves the original size. "" on failure. Stable per (path,maxW) so the browser caches it.
func (u *UI) imgURL(path string, maxW int) string {
	s := mpMediaFS
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ensureListenLocked(u) {
		return ""
	}
	key := path + "\x00" + strconv.Itoa(maxW)
	if tok, ok := s.imgByKey[key]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d/img/%s", s.port, tok)
	}
	tok := randToken()
	if tok == "" {
		return ""
	}
	s.imgTokens[tok] = imgReq{path: path, w: maxW}
	s.imgByKey[key] = tok
	s.imgKeyByTok[tok] = key
	s.imgOrder = append(s.imgOrder, tok)
	s.evictImgLocked()
	return fmt.Sprintf("http://127.0.0.1:%d/img/%s", s.port, tok)
}

// imgBytesURL registers pre-encoded JPEG bytes under the same /img/ loopback server and
// returns a stable, content-keyed URL. Reuses the P1 (VRChat) endpoint + eviction: the
// browser caches by URL, so DOM patches carry a short URL instead of re-shipping a base64
// data-URI every patch. Content-keyed (sha256) → identical frames reuse the URL (cache hit).
// "" on failure. Used by the Motion static/paused avatar-mesh preview (rendered in-memory,
// not a file, so it cannot go through imgURL).
func (u *UI) imgBytesURL(jpegBytes []byte) string {
	if len(jpegBytes) == 0 {
		return ""
	}
	s := mpMediaFS
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ensureListenLocked(u) {
		return ""
	}
	sum := sha256.Sum256(jpegBytes)
	key := "b\x00" + hex.EncodeToString(sum[:])
	if tok, ok := s.imgByKey[key]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d/img/%s", s.port, tok)
	}
	tok := randToken()
	if tok == "" {
		return ""
	}
	s.imgTokens[tok] = imgReq{} // placeholder; bytes pre-cached below (serveImg serves cache directly)
	s.imgByKey[key] = tok
	s.imgKeyByTok[tok] = key
	s.imgOrder = append(s.imgOrder, tok)
	s.imgCache[tok] = jpegBytes
	s.imgCacheOrder = append(s.imgCacheOrder, tok)
	if len(s.imgCacheOrder) > mpImgCacheMax { // bounded byte cache
		old := s.imgCacheOrder[0]
		s.imgCacheOrder = s.imgCacheOrder[1:]
		delete(s.imgCache, old)
	}
	s.evictImgLocked()
	return fmt.Sprintf("http://127.0.0.1:%d/img/%s", s.port, tok)
}

// evictImgLocked FIFO-evicts the oldest img token (+ its key + cache) past the cap. Caller
// holds s.mu. Uses imgKeyByTok so the exact registration key is removed regardless of scheme.
func (s *mpMediaSrv) evictImgLocked() {
	for len(s.imgOrder) > mpImgMaxTokens {
		old := s.imgOrder[0]
		s.imgOrder = s.imgOrder[1:]
		if k, ok := s.imgKeyByTok[old]; ok {
			delete(s.imgByKey, k)
		}
		delete(s.imgKeyByTok, old)
		delete(s.imgTokens, old)
		delete(s.imgCache, old)
	}
}

func (s *mpMediaSrv) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/img/") {
		s.serveImg(w, r)
		return
	}
	tok := strings.TrimPrefix(r.URL.Path, "/m/")
	s.mu.Lock()
	path, ok := s.tokens[tok]
	s.mu.Unlock()
	if !ok || tok == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f) // Range-capable
}

// serveImg serves a token's resized JPEG (decode+resize once, then cache the bytes).
func (s *mpMediaSrv) serveImg(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.URL.Path, "/img/")
	s.mu.Lock()
	req, ok := s.imgTokens[tok]
	cached := s.imgCache[tok]
	s.mu.Unlock()
	if !ok || tok == "" {
		http.NotFound(w, r)
		return
	}
	if cached == nil {
		enc, err := renderResizedJPEG(req.path, req.w)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.imgCache[tok] = enc
		s.imgCacheOrder = append(s.imgCacheOrder, tok)
		if len(s.imgCacheOrder) > mpImgCacheMax { // bounded byte cache
			old := s.imgCacheOrder[0]
			s.imgCacheOrder = s.imgCacheOrder[1:]
			delete(s.imgCache, old)
		}
		s.mu.Unlock()
		cached = enc
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(cached)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(cached)
}

// renderResizedJPEG decodes path, area-average downscales to maxW px wide, and JPEG-encodes.
func renderResizedJPEG(path string, maxW int) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	src, _, err := image.Decode(fh)
	if err != nil {
		return nil, err
	}
	dst := resizeAreaAvg(src, maxW)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resizeAreaAvg box-average downscales src to maxW px wide (keeps aspect). maxW<=0 or an
// already-smaller source returns src unchanged. Area-average (vs nearest-neighbour) avoids
// the aliasing/blur of the old thumbnailer. Stdlib only.
func resizeAreaAvg(src image.Image, maxW int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 || maxW <= 0 || sw <= maxW {
		return src
	}
	tw := maxW
	th := sh * maxW / sw
	if th < 1 {
		th = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for ty := 0; ty < th; ty++ {
		sy0 := b.Min.Y + ty*sh/th
		sy1 := b.Min.Y + (ty+1)*sh/th
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for tx := 0; tx < tw; tx++ {
			sx0 := b.Min.X + tx*sw/tw
			sx1 := b.Min.X + (tx+1)*sw/tw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var rs, gs, bs, as, cnt uint64
			for yy := sy0; yy < sy1; yy++ {
				for xx := sx0; xx < sx1; xx++ {
					cr, cg, cb, ca := src.At(xx, yy).RGBA() // 16-bit premult
					rs += uint64(cr)
					gs += uint64(cg)
					bs += uint64(cb)
					as += uint64(ca)
					cnt++
				}
			}
			if cnt == 0 {
				cnt = 1
			}
			dst.Set(tx, ty, color.RGBA64{R: uint16(rs / cnt), G: uint16(gs / cnt), B: uint16(bs / cnt), A: uint16(as / cnt)})
		}
	}
	return dst
}
