package webui

// Remote-library media bridging. The mirrored document references the HOST's loopback media
// server (mediahttp /m/ + /img/ tokens on 127.0.0.1:<host port>) - unreachable from the
// controller. The host rewrites its loopback prefix to a placeholder on the way out; the
// controller rewrites the placeholder to its OWN loopback server's /rmt/<sid>/ route, which
// fetches the bytes over ChanRemoteUI (byte-range RPC riding the authenticated peer link).
// Only paths under the host's token-guarded /m/ and /img/ routes are reachable - the same
// unguessable-token surface the host's own webview uses, nothing else on its filesystem.

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ruiMediaPlaceholder replaces the host's loopback origin on the wire (plain string
	// rewrite; .invalid TLD can never resolve if it leaks).
	ruiMediaPlaceholder = "https://rmt-media.invalid/"

	ruiFetchChunkMax = 2 << 20          // one fetch reply's raw byte cap (base64 rides one link frame)
	ruiFetchTimeout  = 15 * time.Second // controller-side wait per chunk
	ruiFetchPendMax  = 32               // concurrent in-flight fetches; excess request → 503
	ruiImgFullMax    = 4 << 20          // whole-image proxy cap (larger → 502)

	ruiProxyMax        = 4        // registered proxy sessions (FIFO evict; ≤1 live in practice)
	ruiProxyCacheBytes = 32 << 20 // per-proxy image byte cache (FIFO evict)
	ruiProxyStreams    = 4        // concurrent /m/ streaming loops (memory bound: streams × chunk)
)

// ── host side ───────────────────────────────────────────────────────────────────

// rewriteMediaOut maps this host's loopback media origin to the wire placeholder.
func (h *ruiHub) rewriteMediaOut(payload string) string {
	mpMediaFS.mu.Lock()
	port := mpMediaFS.port
	mpMediaFS.mu.Unlock()
	if port == 0 {
		return payload
	}
	return strings.ReplaceAll(payload, fmt.Sprintf("http://127.0.0.1:%d/", port), ruiMediaPlaceholder)
}

// handleFetch serves one byte-range of a token-guarded media resource to the controller.
func (h *ruiHub) handleFetch(peer string, m ruiMsg) {
	res := h.fetchLocal(m)
	res.T, res.SID, res.FID, res.Path = ruiKindFetchRes, m.SID, m.FID, ""
	if err := h.send(peer, res); err != nil && h.u.log != nil {
		h.u.log.Warn(ruiLogTag, "fetch reply failed", map[string]any{"peer": peer, "error": err.Error()})
	}
}

// fetchLocal reads [Off, Off+Len) of a /m/ or /img/ token from the local loopback server.
// Anything outside those token routes is refused - the peer can only read what this host's
// own page could.
func (h *ruiHub) fetchLocal(m ruiMsg) ruiMsg {
	if !strings.HasPrefix(m.Path, "/m/") && !strings.HasPrefix(m.Path, "/img/") {
		return ruiMsg{Status: http.StatusNotFound}
	}
	mpMediaFS.mu.Lock()
	port := mpMediaFS.port
	mpMediaFS.mu.Unlock()
	if port == 0 {
		return ruiMsg{Status: http.StatusNotFound}
	}
	ln := m.Len
	if ln <= 0 || ln > ruiFetchChunkMax {
		ln = ruiFetchChunkMax
	}
	off := m.Off
	if off < 0 {
		off = 0
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, m.Path), nil)
	if err != nil {
		return ruiMsg{Status: http.StatusBadGateway}
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(ln)-1))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ruiMsg{Status: http.StatusBadGateway}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return ruiMsg{Status: resp.StatusCode}
	}
	total := int64(-1)
	body := resp.Body
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if i := strings.LastIndexByte(cr, '/'); i >= 0 {
				total, _ = strconv.ParseInt(cr[i+1:], 10, 64)
			}
		}
	case http.StatusOK:
		// endpoint ignored the Range (serveImg): skip to off manually
		total = resp.ContentLength
		if off > 0 {
			if _, err := io.CopyN(io.Discard, body, off); err != nil {
				return ruiMsg{Status: http.StatusRequestedRangeNotSatisfiable, Total: total}
			}
		}
	}
	buf, err := io.ReadAll(io.LimitReader(body, int64(ln)))
	if err != nil {
		return ruiMsg{Status: http.StatusBadGateway}
	}
	return ruiMsg{Status: http.StatusOK, CT: resp.Header.Get("Content-Type"), Total: total,
		Off: off, Data: base64.StdEncoding.EncodeToString(buf)}
}

// ── controller-side fetch RPC ───────────────────────────────────────────────────

// fetchRemote requests one byte-range from the mirrored peer; blocks ≤ ruiFetchTimeout.
// In-flight requests are bounded (ruiFetchPendMax; excess errors immediately).
func (h *ruiHub) fetchRemote(peer, sid, path string, off int64, ln int) (ruiMsg, error) {
	fid := randToken()
	ch := make(chan ruiMsg, 1)
	h.mu.Lock()
	if h.fetchPend == nil {
		h.fetchPend = map[string]chan ruiMsg{}
	}
	if len(h.fetchPend) >= ruiFetchPendMax {
		h.mu.Unlock()
		return ruiMsg{}, fmt.Errorf("remoteui: fetch queue full")
	}
	h.fetchPend[fid] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.fetchPend, fid)
		h.mu.Unlock()
	}()
	if err := h.send(peer, ruiMsg{T: ruiKindFetch, SID: sid, FID: fid, Path: path, Off: off, Len: ln}); err != nil {
		return ruiMsg{}, err
	}
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(ruiFetchTimeout):
		return ruiMsg{}, fmt.Errorf("remoteui: fetch timeout")
	}
}

// deliverFetch routes a fetch reply to its waiter (mirror sink → here).
func (h *ruiHub) deliverFetch(m ruiMsg) {
	h.mu.Lock()
	ch := h.fetchPend[m.FID]
	delete(h.fetchPend, m.FID)
	h.mu.Unlock()
	if ch != nil {
		ch <- m // buffered
	}
}

// ── controller-side loopback proxy (/rmt/<sid>/…) ───────────────────────────────

// ruiProxy adapts one mirror session to the local loopback media server.
type ruiProxy struct {
	fetch func(path string, off int64, ln int) (ruiMsg, error)

	mu         sync.Mutex
	cache      map[string][]byte // whole-image bytes + content type, keyed by path
	cacheCT    map[string]string
	cacheOrder []string
	cacheBytes int
}

var (
	ruiProxyMu    sync.Mutex
	ruiProxies    = map[string]*ruiProxy{} // sid → proxy (cap ruiProxyMax, FIFO evict)
	ruiProxyOrder []string
	ruiStreamSem  = make(chan struct{}, ruiProxyStreams)
)

func registerRuiProxy(sid string, p *ruiProxy) {
	ruiProxyMu.Lock()
	defer ruiProxyMu.Unlock()
	if _, ok := ruiProxies[sid]; !ok {
		ruiProxyOrder = append(ruiProxyOrder, sid)
	}
	ruiProxies[sid] = p
	for len(ruiProxyOrder) > ruiProxyMax { // bounded: evict oldest session
		old := ruiProxyOrder[0]
		ruiProxyOrder = ruiProxyOrder[1:]
		delete(ruiProxies, old)
	}
}

func unregisterRuiProxy(sid string) {
	ruiProxyMu.Lock()
	defer ruiProxyMu.Unlock()
	if _, ok := ruiProxies[sid]; ok {
		delete(ruiProxies, sid)
		for i, s := range ruiProxyOrder {
			if s == sid {
				ruiProxyOrder = append(ruiProxyOrder[:i], ruiProxyOrder[i+1:]...)
				break
			}
		}
	}
}

// ruiProxyServe handles /rmt/<sid>/(m|img)/<tok> on the local loopback media server.
func ruiProxyServe(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/rmt/")
	sid, sub, ok := strings.Cut(rest, "/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	ruiProxyMu.Lock()
	p := ruiProxies[sid]
	ruiProxyMu.Unlock()
	if p == nil {
		http.NotFound(w, r)
		return
	}
	path := "/" + sub
	switch {
	case strings.HasPrefix(path, "/img/"):
		p.serveImg(w, r, path)
	case strings.HasPrefix(path, "/m/"):
		p.serveMedia(w, r, path)
	default:
		http.NotFound(w, r)
	}
}

// serveImg proxies a whole (small) image, cached per path (byte-bounded FIFO).
func (p *ruiProxy) serveImg(w http.ResponseWriter, r *http.Request, path string) {
	p.mu.Lock()
	cached, ct := p.cache[path], p.cacheCT[path]
	p.mu.Unlock()
	if cached == nil {
		var buf []byte
		var off int64
		for {
			res, err := p.fetch(path, off, ruiFetchChunkMax)
			if err != nil || res.Status != http.StatusOK {
				http.Error(w, "remote fetch failed", http.StatusBadGateway)
				return
			}
			chunk, derr := base64.StdEncoding.DecodeString(res.Data)
			if derr != nil {
				http.Error(w, "bad chunk", http.StatusBadGateway)
				return
			}
			buf = append(buf, chunk...)
			off += int64(len(chunk))
			ct = res.CT
			if len(chunk) == 0 || (res.Total > 0 && off >= res.Total) {
				break
			}
			if len(buf) > ruiImgFullMax {
				http.Error(w, "image too large for remote proxy", http.StatusBadGateway)
				return
			}
		}
		p.mu.Lock()
		if _, dup := p.cache[path]; !dup {
			p.cache[path] = buf
			p.cacheCT[path] = ct
			p.cacheOrder = append(p.cacheOrder, path)
			p.cacheBytes += len(buf)
			for p.cacheBytes > ruiProxyCacheBytes && len(p.cacheOrder) > 0 { // bounded byte cache
				old := p.cacheOrder[0]
				p.cacheOrder = p.cacheOrder[1:]
				p.cacheBytes -= len(p.cache[old])
				delete(p.cache, old)
				delete(p.cacheCT, old)
			}
		}
		p.mu.Unlock()
		cached = buf
	}
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(cached)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(cached)
}

// serveMedia proxies a media stream. Ranged request → one remote chunk as 206 (players
// follow up with further ranges); rangeless → sequential 200 stream, one chunk in flight.
// Concurrency bounded by ruiStreamSem (memory ≤ streams × chunk).
func (p *ruiProxy) serveMedia(w http.ResponseWriter, r *http.Request, path string) {
	select {
	case ruiStreamSem <- struct{}{}:
		defer func() { <-ruiStreamSem }()
	default:
		http.Error(w, "too many remote streams", http.StatusServiceUnavailable)
		return
	}
	start, hasRange := parseRangeStart(r.Header.Get("Range"))
	res, err := p.fetch(path, start, ruiFetchChunkMax)
	if err != nil || res.Status != http.StatusOK {
		http.Error(w, "remote fetch failed", http.StatusBadGateway)
		return
	}
	chunk, derr := base64.StdEncoding.DecodeString(res.Data)
	if derr != nil {
		http.Error(w, "bad chunk", http.StatusBadGateway)
		return
	}
	ct := res.CT
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Accept-Ranges", "bytes")
	if hasRange && res.Total > 0 {
		end := start + int64(len(chunk)) - 1
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, res.Total))
		w.Header().Set("Content-Length", strconv.Itoa(len(chunk)))
		w.WriteHeader(http.StatusPartialContent)
		if r.Method != http.MethodHead {
			_, _ = w.Write(chunk)
		}
		return
	}
	if res.Total > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(res.Total, 10))
	}
	if r.Method == http.MethodHead {
		return
	}
	off := start
	for {
		if len(chunk) == 0 {
			return
		}
		if _, werr := w.Write(chunk); werr != nil {
			return // client went away
		}
		off += int64(len(chunk))
		if res.Total > 0 && off >= res.Total {
			return
		}
		res, err = p.fetch(path, off, ruiFetchChunkMax)
		if err != nil || res.Status != http.StatusOK {
			return
		}
		chunk, derr = base64.StdEncoding.DecodeString(res.Data)
		if derr != nil {
			return
		}
	}
}

// parseRangeStart extracts the first-byte offset of a "bytes=start-…" header.
func parseRangeStart(h string) (int64, bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(h, "bytes=") {
		return 0, false
	}
	spec := strings.TrimPrefix(h, "bytes=")
	if i := strings.IndexByte(spec, ','); i >= 0 {
		spec = spec[:i] // first range only
	}
	startS, _, ok := strings.Cut(spec, "-")
	if !ok || startS == "" {
		return 0, false // suffix ranges ("-500") degrade to a full stream
	}
	start, err := strconv.ParseInt(strings.TrimSpace(startS), 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

// mpProxyPort lazily starts the local loopback media server and returns its port (0 on failure).
func (u *UI) mpProxyPort() int {
	s := mpMediaFS
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ensureListenLocked(u) {
		return 0
	}
	return s.port
}
