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

	"rave.page/mate/internal/store"
)

// Loopback media endpoint for the unified player's embedded <video>: streams local files
// into the webview with HTTP Range support (http.ServeContent → seeking works). This is
// plain file IO - no decode, no frames - so it stays in-daemon (the featurehost isolation
// rule targets media processing, not sendfile). Security: 127.0.0.1 listener only, no
// directory access - only files explicitly registered by the player are reachable, each
// behind an unguessable random token. Token table is bounded (mpMediaMaxTokens; per-owner
// FIFO evict, so a headless remote session's churn never evicts the window's live token).
//
// The same listener also serves /img/<token>: a CACHED, RESIZED image endpoint. VRChat
// thumbnails (and other grids) register a (path, maxW) pair → a stable short URL the browser
// caches by URL, instead of re-embedding a base64 data-URI in every DOM patch (the old
// vrcMakeThumb path re-shipped kilobytes per photo per patch and upscaled a 160px nearest-
// neighbour raster → blur+lag). Decode+area-average downscale happens once per (path,maxW),
// off the UI thread, and the encoded JPEG is cached in-memory (bounded).

const (
	mpMediaMaxTokens = 128 // raised from 32: remote-session churn must not starve the window's tokens
	mpImgMaxTokens   = 256
	mpImgCacheMax    = 128
)

type imgReq struct {
	path string
	w    int // target max width in px; 0 = original size
}

// mediaSessMax keeps the CURRENT plus one PREVIOUS session valid: a <video> mid-play (or an
// in-flight remote-mirror fetch) must survive the window child restarting under it, and the next
// render re-mints the URL anyway.
const mediaSessMax = 2

type mpMediaSrv struct {
	mu   sync.Mutex
	port int
	// sess scopes every /m//mi//img/ URL to one shell session (newest first; see mediaSession).
	// EMPTY = the historic 2-segment URLs, byte-for-byte - the in-proc shell, the Fyne renderer and
	// headless mirror sessions never mint one, so the default path is untouched. procShell mints one
	// per child session and hands it to the child in the init payload: the child's fetches are then
	// authorized by an identity the DAEMON gave it, and a token URL captured from a dead session is
	// refused instead of being served to any loopback caller that still holds the token.
	sess   []string
	tokens map[string]string // token → absolute file path (raw stream)
	owner  map[string]*UI    // token → minting UI: eviction stays within the minter's own tokens
	order  []string          // FIFO for eviction (per-owner first)
	byPath map[string]string // path → token (stable URLs per file)

	imgTokens     map[string]imgReq // token → resize request
	imgByKey      map[string]string // key → token (stable per (path,w) OR per content hash)
	imgKeyByTok   map[string]string // token → key (reverse map so eviction removes the exact key)
	imgOrder      []string          // FIFO eviction of img tokens
	imgCache      map[string][]byte // token → encoded JPEG bytes
	imgCacheOrder []string          // FIFO eviction of cached bytes
}

var mpMediaFS = &mpMediaSrv{
	tokens: map[string]string{}, owner: map[string]*UI{}, byPath: map[string]string{},
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

// newSession mints (and activates) a media session, retiring the oldest past mediaSessMax. Returns
// the session id.
func (s *mpMediaSrv) newSession() string {
	tok := randToken()
	if tok == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sess = append([]string{tok}, s.sess...)
	if len(s.sess) > mediaSessMax {
		s.sess = s.sess[:mediaSessMax]
	}
	return tok
}

// originAndSession reports the loopback origin ("" until the listener starts) and the current
// session id ("" when unsessioned) - the two facts the window child is told explicitly.
func (s *mpMediaSrv) originAndSession() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	origin := ""
	if s.port != 0 {
		origin = fmt.Sprintf("http://127.0.0.1:%d", s.port)
	}
	if len(s.sess) == 0 {
		return origin, ""
	}
	return origin, s.sess[0]
}

// sessPrefixLocked is the path segment URLs carry ("" when unsessioned). Caller holds s.mu.
func (s *mpMediaSrv) sessPrefixLocked() string {
	if len(s.sess) == 0 {
		return ""
	}
	return s.sess[0] + "/"
}

// routeToken strips route from path and validates the session segment. ok=false = refuse (404):
// wrong/absent session while sessions are active, or an empty token.
func (s *mpMediaSrv) routeToken(path, route string) (string, bool) {
	rest := strings.TrimPrefix(path, route)
	s.mu.Lock()
	live := append([]string(nil), s.sess...)
	s.mu.Unlock()
	if len(live) == 0 {
		return rest, rest != ""
	}
	i := strings.IndexByte(rest, '/')
	if i <= 0 {
		return "", false
	}
	got, tok := rest[:i], rest[i+1:]
	for _, ok := range live {
		if got == ok && tok != "" {
			return tok, true
		}
	}
	return "", false
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
		if !u.virtual() { // window reuse re-tags: remote churn must not evict a live window token
			s.owner[tok] = u
		}
		return fmt.Sprintf("http://127.0.0.1:%d/m/%s%s", s.port, s.sessPrefixLocked(), tok)
	}
	tok := randToken()
	if tok == "" {
		return ""
	}
	s.tokens[tok] = path
	s.owner[tok] = u
	s.byPath[path] = tok
	s.order = append(s.order, tok)
	if len(s.order) > mpMediaMaxTokens { // bounded: evict the minter's own oldest first
		s.evictMediaLocked(u)
	}
	return fmt.Sprintf("http://127.0.0.1:%d/m/%s%s", s.port, s.sessPrefixLocked(), tok)
}

// mpIndexURL returns the /mi/ URL serving path's cached fragmented-MP4 stream index
// (mp4frag.Index JSON, consumed by the shell __mse runtime). Same token as the /m/ media
// stream - the index is only rendered into the DOM once the cache is warm (mpLoadFrag), so
// the endpoint can serve straight from the store.
func (u *UI) mpIndexURL(path string) string {
	mediaURL := u.mpMediaURL(path)
	if mediaURL == "" {
		return ""
	}
	return strings.Replace(mediaURL, "/m/", "/mi/", 1)
}

// evictMediaLocked drops one token: the oldest minted by u, else the global oldest. Caller
// holds s.mu. Per-owner first, so a remote session's churn can't evict the host window's
// live <video> token mid-play.
func (s *mpMediaSrv) evictMediaLocked(u *UI) {
	idx := 0
	for i, tok := range s.order {
		if s.owner[tok] == u {
			idx = i
			break
		}
	}
	tok := s.order[idx]
	s.order = append(s.order[:idx], s.order[idx+1:]...)
	delete(s.byPath, s.tokens[tok])
	delete(s.tokens, tok)
	delete(s.owner, tok)
}

// releaseMediaOwner drops every token minted by a retired UI (releaseUIState).
func (s *mpMediaSrv) releaseMediaOwner(u *UI) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.order[:0]
	for _, tok := range s.order {
		if s.owner[tok] == u {
			delete(s.byPath, s.tokens[tok])
			delete(s.tokens, tok)
			delete(s.owner, tok)
			continue
		}
		keep = append(keep, tok)
	}
	s.order = keep
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
		return fmt.Sprintf("http://127.0.0.1:%d/img/%s%s", s.port, s.sessPrefixLocked(), tok)
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
	return fmt.Sprintf("http://127.0.0.1:%d/img/%s%s", s.port, s.sessPrefixLocked(), tok)
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
		return fmt.Sprintf("http://127.0.0.1:%d/img/%s%s", s.port, s.sessPrefixLocked(), tok)
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
	return fmt.Sprintf("http://127.0.0.1:%d/img/%s%s", s.port, s.sessPrefixLocked(), tok)
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
	// Cross-origin fetch support: the webview page has an opaque origin (loaded via SetHtml), so
	// reading loopback bytes with fetch() (the point-cloud viewer) is a CORS request. Loopback-only
	// + unguessable per-file token, so ACAO:* is safe. Also satisfy Chromium's Private-Network
	// preflight (public/opaque → localhost). Media elements (<video>/<img>) don't need this; fetch does.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Range")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		// Range is not CORS-safelisted → every MSE fragment fetch would preflight; cache the
		// verdict (Chromium caps at 2 h) so steady-state streaming is one request per fragment.
		w.Header().Set("Access-Control-Max-Age", "7200")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/img/") {
		s.serveImg(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/rmt/") { // remote-library media proxy (remoteui_media.go)
		ruiProxyServe(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/mi/") { // fragmented-MP4 stream index (MSE runtime)
		s.serveIndex(w, r)
		return
	}
	tok, sok := s.routeToken(r.URL.Path, "/m/")
	if !sok {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	path, ok := s.tokens[tok]
	owner := s.owner[tok]
	s.mu.Unlock()
	if !ok {
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
	cw := &countingWriter{ResponseWriter: w}
	start := time.Now()
	http.ServeContent(cw, r, fi.Name(), fi.ModTime(), f) // Range-capable
	if owner != nil && owner.log != nil {
		owner.log.Debug("mediahttp", "serve", map[string]any{
			"file": fi.Name(), "range": r.Header.Get("Range"), "sent": cw.n,
			"ms": time.Since(start).Milliseconds(),
		})
	}
}

// countingWriter counts body bytes for the media-serve debug log.
type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n += int64(n)
	return n, err
}

// serveIndex serves a media token's cached fragmented-MP4 index JSON (404 when the file is
// not a supported fMP4 - the negative sentinel has no frags - or the cache is cold/stale;
// the JS runtime then falls back to plain src). Store read only - never parses here.
func (s *mpMediaSrv) serveIndex(w http.ResponseWriter, r *http.Request) {
	tok, sok := s.routeToken(r.URL.Path, "/mi/")
	if !sok {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	path, ok := s.tokens[tok]
	owner := s.owner[tok]
	s.mu.Unlock()
	if !ok || owner == nil || owner.svc.Store == nil {
		http.NotFound(w, r)
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, ok := owner.svc.Store.GetAnalysis(store.KindMp4Frag, path, fi.ModTime().Unix())
	if !ok || !bytes.Contains(data, []byte(`"frags"`)) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // mtime-keyed server-side; tiny payload
	_, _ = w.Write(data)
}

// serveImg serves a token's resized JPEG (decode+resize once, then cache the bytes).
func (s *mpMediaSrv) serveImg(w http.ResponseWriter, r *http.Request) {
	tok, sok := s.routeToken(r.URL.Path, "/img/")
	if !sok {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	req, ok := s.imgTokens[tok]
	cached := s.imgCache[tok]
	s.mu.Unlock()
	if !ok {
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
