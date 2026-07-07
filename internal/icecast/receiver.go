// Package icecast is rave-mate's local Icecast-source receiver: the endpoint Traktor's
// built-in Broadcasting feature streams a live set to. It speaks the Icecast2 source
// protocol (an HTTP SOURCE/PUT upload authenticated by a source password), streams the
// encoded body straight to a timestamped file in the sets dir, and parses the broadcast
// metadata (in-band Ogg Vorbis comments + the /admin/metadata side channel Traktor uses
// for MP3) into a now-playing feed. A captured file is time-linked to the recorder's
// tracklist (a track's offset into the audio = track.StartedAt − capture.StartedAt), which
// unlocks history playback and per-track fingerprinting.
//
// Audio is broadcast-quality lossy (Ogg Vorbis / MP3 at Traktor's broadcast bitrate) by
// design - Icecast carries an encoded stream. Lossless would be Traktor's internal Audio
// Recorder, but that has no live metadata/timing feed, so Icecast is the right tool for the
// linked-set goal. All wire handling is stdlib (net + bufio + textproto); no new dep.
package icecast

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

const source = "icecast"

// Config is the receiver's runtime configuration (decoupled from the config package so the
// receiver is testable in isolation).
type Config struct {
	Addr     string // listen address, e.g. "127.0.0.1:8000"
	Mount    string // expected mount path (e.g. "/stream"); "" accepts any mount
	Username string // source username; "" = "source"
	Password string // source password (required - an empty password rejects every connection)
	SetsDir  string // capture output dir (created on Start)

	// SingleFile keeps one capture file open across brief source drops: a reconnect within
	// ReconnectGrace resumes the same file rather than starting a new one (so transient
	// disconnects don't chop a set into fragments). Off = one file per source connection.
	SingleFile     bool
	ReconnectGrace time.Duration // grace window for a reconnect (only used when SingleFile)

	// MetadataOnly parses the broadcast for now-playing but writes NO audio to disk (no capture
	// file, no capture events) - for when native audio recording is the canonical recording and
	// Icecast is only the metadata source.
	MetadataOnly bool
}

// Meta is a now-playing update parsed from the broadcast metadata.
type Meta struct {
	Title  string
	Artist string
}

func (m Meta) empty() bool { return m.Title == "" && m.Artist == "" }

// Capture describes one captured set file. It is emitted on the capture channel when a
// source connects (EndedAt zero) and again when it disconnects (EndedAt set, Bytes final).
type Capture struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Format    string    `json:"format"` // ogg|mp3|aac|bin
	Mount     string    `json:"mount"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	Bytes     int64     `json:"bytes"`
}

// Status is a snapshot of the receiver for the UI ("Traktor connected ✓").
type Status struct {
	Listening    bool
	Addr         string
	Connected    bool // a source is currently streaming
	Reconnecting bool // single-file capture held open during a reconnect grace gap
	Mount        string
	Format       string
	FilePath     string
	StartedAt    time.Time
	Bytes        int64
	Title        string
	Artist       string

	// Diagnostics - so a user can see whether Traktor ever reached the receiver and why a
	// connection was refused (the #1 "can't connect" cause is a forgotten Start Broadcasting
	// in Traktor, i.e. Attempts stays 0).
	Attempts   int    // total inbound TCP connections since start
	LastRemote string // remote addr of the most recent connection
	LastEvent  string // human-readable last event (e.g. "auth rejected - no password set")
	LastError  string // last bind/listen error ("" = none)
}

// Receiver is the Icecast-source listener. One source connection is captured at a time
// (a second concurrent source is rejected, mirroring Icecast's "mount in use").
type Receiver struct {
	log *logbus.Bus

	mu  sync.Mutex
	cfg Config
	ln  net.Listener

	// live capture state (nil when idle)
	cur     *Capture
	curFile *os.File    // open file backing cur (kept open across a grace gap in single-file mode)
	stream  bool        // a source is actively connected + writing right now
	grace   *time.Timer // pending finalize timer during a single-file reconnect gap
	lastMs  Meta
	bytes   int64
	seq     int
	listing bool

	// diagnostics (see Status)
	attempts   int
	lastRemote string
	lastEvent  string
	lastErr    string

	subMu    sync.Mutex
	metaSubs map[int]chan Meta
	capSubs  map[int]chan Capture
	nextSub  int
}

// New constructs a receiver. cfg may be replaced later via SetConfig (takes effect on the
// next source connection).
func New(log *logbus.Bus, cfg Config) *Receiver {
	return &Receiver{
		log:      log,
		cfg:      cfg,
		metaSubs: map[int]chan Meta{},
		capSubs:  map[int]chan Capture{},
	}
}

// SetConfig swaps the receiver config. The new sets dir/auth apply to the next connection;
// an in-flight capture keeps its original file.
func (r *Receiver) SetConfig(cfg Config) {
	r.mu.Lock()
	r.cfg = cfg
	r.mu.Unlock()
}

// Start binds the listener and serves until ctx is cancelled. Returns the bind error
// synchronously (so the module manager surfaces a port clash); the accept loop runs in a
// goroutine. A nil/blank password is allowed to bind but rejects every connection.
func (r *Receiver) Start(ctx context.Context) error {
	r.mu.Lock()
	cfg := r.cfg
	r.mu.Unlock()
	if cfg.SetsDir != "" {
		if err := os.MkdirAll(cfg.SetsDir, 0o755); err != nil {
			return fmt.Errorf("sets dir: %w", err)
		}
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		r.mu.Lock()
		r.lastErr = err.Error()
		r.mu.Unlock()
		r.log.Warn(source, "receiver bind failed", map[string]any{"addr": cfg.Addr, "error": err.Error()})
		return fmt.Errorf("icecast listen %s: %w", cfg.Addr, err)
	}
	r.mu.Lock()
	r.ln = ln
	r.listing = true
	r.lastErr = ""
	r.mu.Unlock()
	scope := "loopback only (127.0.0.1)"
	if !strings.HasPrefix(cfg.Addr, "127.") && !strings.HasPrefix(cfg.Addr, "localhost") {
		scope = "all interfaces (LAN reachable)"
	}
	r.log.Info(source, "receiver listening", map[string]any{"addr": cfg.Addr, "scope": scope, "setsDir": cfg.SetsDir})

	debuglog.Go(r.log, source, func() {
		<-ctx.Done()
		_ = ln.Close()
	})
	debuglog.Go(r.log, source, func() { r.acceptLoop(ctx, ln) })
	return nil
}

// Stop closes the listener (idempotent). The accept loop also exits on ctx cancel. A capture
// held open in a single-file reconnect grace gap is finalized so its file + record are sealed.
func (r *Receiver) Stop() {
	r.mu.Lock()
	ln := r.ln
	r.ln = nil
	r.listing = false
	// Finalize a capture sitting in a grace gap (no active source) so shutdown doesn't leave
	// it open. An actively-streaming capture is finalized by its own connection goroutine.
	var pending *os.File
	if r.cur != nil && !r.stream {
		pending = r.curFile
	}
	r.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if pending != nil {
		r.finalize(pending)
	}
}

func (r *Receiver) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			r.log.Warn(source, "accept failed", map[string]any{"error": err.Error()})
			continue
		}
		debuglog.Go(r.log, source, func() { r.handleConn(ctx, conn) })
	}
}

// handleConn parses one Icecast request line + headers and dispatches by method.
func (r *Receiver) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	remote := conn.RemoteAddr().String()
	r.noteConn(remote)
	br := bufio.NewReader(conn)
	tp := textproto.NewReader(br)

	// Request line: "<METHOD> <target> <proto>". Icecast2 uses SOURCE/PUT for the upload and
	// GET for the metadata side channel; the proto token may be ICE/1.0 or HTTP/1.x.
	line, err := tp.ReadLine()
	if err != nil {
		// A bare TCP connect with no request line is usually a probe/port-scan or a half-open
		// client - log it so "something connected but didn't speak Icecast" is visible.
		r.note("connection opened then closed before sending a request")
		r.log.Info(source, "connection: no request", map[string]any{"remote": remote})
		return
	}
	method, target, ok := parseRequestLine(line)
	if !ok {
		r.note("bad request line from " + remote)
		r.log.Warn(source, "bad request line", map[string]any{"remote": remote, "line": line})
		writeStatus(conn, 400, "Bad Request")
		return
	}
	r.log.Info(source, "connection", map[string]any{"remote": remote, "method": method, "target": target})
	hdr, err := tp.ReadMIMEHeader()
	if err != nil {
		r.note("malformed headers from " + remote)
		writeStatus(conn, 400, "Bad Request")
		return
	}

	switch strings.ToUpper(method) {
	case "SOURCE", "PUT":
		r.handleSource(ctx, conn, br, target, hdr)
	case "GET":
		r.handleGet(conn, target, hdr)
	default:
		writeStatus(conn, 405, "Method Not Allowed")
	}
}

// handleSource authenticates and captures a source upload to a file in the sets dir. In
// single-file mode a reconnect arriving within the grace window resumes the open file (so a
// transient drop doesn't chop the set); otherwise a second concurrent source is rejected.
func (r *Receiver) handleSource(ctx context.Context, conn net.Conn, br *bufio.Reader, target string, hdr textproto.MIMEHeader) {
	r.mu.Lock()
	cfg := r.cfg
	active := r.cur != nil // a capture exists (streaming or in a grace gap)
	streaming := r.stream  // a source is writing right now
	r.mu.Unlock()

	mount := mountPath(target)
	remote := conn.RemoteAddr().String()
	if !r.authOK(cfg, hdr.Get("Authorization")) {
		reason := "credentials rejected - check the source user/password match Traktor"
		if cfg.Password == "" {
			reason = "no source password is set in rave-mate - set one in Set Capture settings"
		}
		r.note("source auth rejected - " + reason)
		r.log.Warn(source, "source auth rejected", map[string]any{
			"mount": mount, "remote": remote, "reason": reason,
			"contentType": hdr.Get("Content-Type"), "userAgent": hdr.Get("User-Agent"),
		})
		writeStatus(conn, 401, "Unauthorized")
		return
	}
	if cfg.Mount != "" && mount != "" && !strings.EqualFold(mount, cfg.Mount) {
		r.note(fmt.Sprintf("mount %q rejected (expected %q)", mount, cfg.Mount))
		r.log.Warn(source, "source mount mismatch", map[string]any{"got": mount, "want": cfg.Mount, "remote": remote})
		writeStatus(conn, 404, "Mount Not Found")
		return
	}
	// A source is still actively writing: reject the second one (mirrors Icecast's "mount in
	// use"). A capture that exists but isn't streaming is in a single-file grace gap → resume it.
	if active && streaming {
		r.note("source rejected - a capture is already active")
		r.log.Warn(source, "source rejected: capture already active", map[string]any{"mount": mount, "remote": remote})
		writeStatus(conn, 403, "Mountpoint in use")
		return
	}

	// libshout (PUT) sends Expect: 100-continue and waits for it before streaming the body.
	if strings.Contains(strings.ToLower(hdr.Get("Expect")), "100-continue") {
		_, _ = io.WriteString(conn, "HTTP/1.1 100 Continue\r\n\r\n")
	}

	// Metadata-only: ack + parse the body for now-playing, write nothing to disk, emit no capture.
	if cfg.MetadataOnly {
		_, _ = io.WriteString(conn, "HTTP/1.0 200 OK\r\n\r\n")
		format := formatFromContentType(hdr.Get("Content-Type"))
		r.note(fmt.Sprintf("metadata-only: parsing %s from %s (audio not written)", format, remote))
		r.log.Info(source, "metadata-only source", map[string]any{"mount": mount, "format": format, "remote": remote})
		r.streamBody(ctx, br, nil, format)
		return
	}

	cap, file, resumed := r.resumeCapture()
	if !resumed {
		format := formatFromContentType(hdr.Get("Content-Type"))
		var err error
		cap, file, err = r.openCapture(cfg, mount, format)
		if err != nil {
			r.log.Warn(source, "open capture failed", map[string]any{"error": err.Error()})
			writeStatus(conn, 500, "Internal Error")
			return
		}
	}
	// Icecast acknowledges the source with HTTP/1.0 200 OK, then the client streams the body.
	_, _ = io.WriteString(conn, "HTTP/1.0 200 OK\r\n\r\n")
	if resumed {
		r.note(fmt.Sprintf("capture resumed (%s) from %s", cap.Format, remote))
		r.log.Info(source, "capture resumed", map[string]any{"mount": cap.Mount, "format": cap.Format, "path": cap.Path, "remote": remote})
	} else {
		r.note(fmt.Sprintf("capturing %s from %s", cap.Format, remote))
		r.log.Info(source, "capture started", map[string]any{
			"mount": mount, "format": cap.Format, "path": cap.Path, "remote": remote,
			"userAgent": hdr.Get("User-Agent"),
		})
		r.emitCapture(*cap)
	}

	r.streamBody(ctx, br, file, cap.Format)

	// In single-file mode a disconnect (while still running) opens a grace window: keep the
	// file open and wait for a reconnect; only finalize if grace expires. On shutdown
	// (ctx done) or non-single-file mode, finalize immediately.
	if cfg.SingleFile && ctx.Err() == nil {
		r.enterGrace(cfg.ReconnectGrace, file)
		return
	}
	r.finalize(file)
}

// finalize closes the capture, records it, and emits the end event.
func (r *Receiver) finalize(file *os.File) {
	end, ok := r.closeCapture(file)
	if !ok {
		return // already finalized (e.g. a grace timer beat us to it)
	}
	r.note(fmt.Sprintf("capture ended (%s)", humanByteCount(end.Bytes)))
	r.log.Info(source, "capture ended", map[string]any{"path": end.Path, "bytes": end.Bytes})
	r.emitCapture(end)
}

// enterGrace marks the source as no longer streaming and arms a timer to finalize the open
// capture after the grace window - unless a reconnect resumes it first (which stops the timer).
func (r *Receiver) enterGrace(grace time.Duration, file *os.File) {
	r.mu.Lock()
	r.stream = false
	if r.cur == nil { // nothing to hold open
		r.mu.Unlock()
		return
	}
	if r.grace != nil {
		r.grace.Stop()
	}
	r.grace = time.AfterFunc(grace, func() { r.finalize(file) })
	r.mu.Unlock()
	r.note(fmt.Sprintf("source dropped - holding the file open for %s in case it reconnects", grace.Round(time.Second)))
	r.log.Info(source, "capture paused (reconnect grace)", map[string]any{"grace": grace.String()})
}

// resumeCapture re-attaches to a capture sitting in a grace gap: it stops the finalize timer
// and marks the source streaming again, returning the existing capture + open file. ok=false
// when there's nothing to resume (the common first-connect case).
func (r *Receiver) resumeCapture() (*Capture, *os.File, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil || r.curFile == nil || r.stream {
		return nil, nil, false
	}
	if r.grace != nil {
		r.grace.Stop()
		r.grace = nil
	}
	r.stream = true
	return r.cur, r.curFile, true
}

// streamBody copies the source body to file until EOF/disconnect, tapping the bytes for
// in-band metadata (Ogg Vorbis comments). MP3 carries no in-band metadata - Traktor pushes
// it via /admin/metadata, handled separately.
func (r *Receiver) streamBody(ctx context.Context, br *bufio.Reader, file *os.File, format string) {
	var oggSc *oggScanner
	if format == "ogg" {
		oggSc = newOggScanner(func(m Meta) { r.setMeta(m) })
	}
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := br.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if file != nil { // nil in metadata-only mode: parse but don't persist audio
				if _, werr := file.Write(chunk); werr != nil {
					r.log.Warn(source, "capture write failed", map[string]any{"error": werr.Error()})
					return
				}
				r.addBytes(int64(n))
			}
			if oggSc != nil {
				oggSc.feed(chunk)
			}
		}
		if err != nil {
			return // EOF or disconnect ends the capture
		}
	}
}

// handleGet serves the metadata side channel Traktor uses for MP3 broadcasts
// (GET /admin/metadata?mode=updinfo&mount=/m&song=Artist - Title). Other GETs get a stub.
func (r *Receiver) handleGet(conn net.Conn, target string, hdr textproto.MIMEHeader) {
	u, err := url.Parse(target)
	if err != nil {
		writeStatus(conn, 400, "Bad Request")
		return
	}
	if !strings.HasPrefix(u.Path, "/admin/metadata") {
		writeStatus(conn, 200, "OK")
		return
	}
	r.mu.Lock()
	cfg := r.cfg
	r.mu.Unlock()
	if !r.authOK(cfg, hdr.Get("Authorization")) {
		writeStatus(conn, 401, "Unauthorized")
		return
	}
	q := u.Query()
	if song := strings.TrimSpace(q.Get("song")); song != "" {
		r.setMeta(parseSong(song))
	}
	writeStatus(conn, 200, "OK")
}

// ── capture lifecycle ────────────────────────────────────────────────────────

func (r *Receiver) openCapture(cfg Config, mount, format string) (*Capture, *os.File, error) {
	now := time.Now()
	r.mu.Lock()
	r.seq++
	seq := r.seq
	r.mu.Unlock()
	name := now.Format("2006-01-02_150405") + mountSlug(mount) + "." + format
	path := filepath.Join(cfg.SetsDir, name)
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	cap := &Capture{
		ID:        fmt.Sprintf("set_%d_%d", now.UnixNano(), seq),
		Path:      path,
		Format:    format,
		Mount:     mount,
		StartedAt: now,
	}
	r.mu.Lock()
	r.cur = cap
	r.curFile = f
	r.stream = true
	r.bytes = 0
	r.lastMs = Meta{}
	r.mu.Unlock()
	return cap, f, nil
}

func (r *Receiver) addBytes(n int64) {
	r.mu.Lock()
	r.bytes += n
	r.mu.Unlock()
}

// closeCapture syncs + closes the file and clears the live capture state, returning the
// final record. ok=false if the capture was already finalized (a grace timer and the
// connection goroutine can both race to close once; the first wins).
func (r *Receiver) closeCapture(file *os.File) (Capture, bool) {
	r.mu.Lock()
	if r.cur == nil || r.curFile != file {
		r.mu.Unlock()
		return Capture{}, false
	}
	if r.grace != nil {
		r.grace.Stop()
		r.grace = nil
	}
	end := *r.cur
	end.EndedAt = time.Now()
	end.Bytes = r.bytes
	r.cur = nil
	r.curFile = nil
	r.stream = false
	r.bytes = 0
	r.mu.Unlock()
	_ = file.Sync()
	_ = file.Close()
	return end, true
}

// ── metadata ─────────────────────────────────────────────────────────────────

func (r *Receiver) setMeta(m Meta) {
	if m.empty() {
		return
	}
	r.mu.Lock()
	if m == r.lastMs {
		r.mu.Unlock()
		return
	}
	r.lastMs = m
	r.mu.Unlock()
	r.log.Info(source, "now playing", map[string]any{"title": m.Title, "artist": m.Artist})
	r.broadcastMeta(m)
}

// ── auth ─────────────────────────────────────────────────────────────────────

// authOK constant-time-compares the Basic credentials against the configured source
// user/password. A blank configured password always fails (no anonymous source).
func (r *Receiver) authOK(cfg Config, authz string) bool {
	if cfg.Password == "" {
		return false
	}
	const p = "Basic "
	if len(authz) <= len(p) || !strings.EqualFold(authz[:len(p)], p) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(authz[len(p):]))
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return false
	}
	wantUser := cfg.Username
	if wantUser == "" {
		wantUser = "source"
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(cfg.Password)) == 1
	return userOK && passOK
}

// ── diagnostics ──────────────────────────────────────────────────────────────

// noteConn records a new inbound connection (count + remote) for the status panel.
func (r *Receiver) noteConn(remote string) {
	r.mu.Lock()
	r.attempts++
	r.lastRemote = remote
	r.mu.Unlock()
}

// note records the last human-readable receiver event for the status panel.
func (r *Receiver) note(event string) {
	r.mu.Lock()
	r.lastEvent = event
	r.mu.Unlock()
}

// ── snapshot + subscriptions ─────────────────────────────────────────────────

// Snapshot returns the current receiver status.
func (r *Receiver) Snapshot() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{
		Listening: r.listing, Addr: r.cfg.Addr, Title: r.lastMs.Title, Artist: r.lastMs.Artist,
		Attempts: r.attempts, LastRemote: r.lastRemote, LastEvent: r.lastEvent, LastError: r.lastErr,
	}
	if r.cur != nil {
		st.Connected = r.stream     // a source is actively streaming right now
		st.Reconnecting = !r.stream // capture held open during a single-file reconnect grace gap
		st.Mount = r.cur.Mount
		st.Format = r.cur.Format
		st.FilePath = r.cur.Path
		st.StartedAt = r.cur.StartedAt
		st.Bytes = r.bytes
	}
	return st
}

// SubscribeMeta streams now-playing updates until the returned cancel is called.
func (r *Receiver) SubscribeMeta() (<-chan Meta, func()) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	id := r.nextSub
	r.nextSub++
	ch := make(chan Meta, 8)
	r.metaSubs[id] = ch
	return ch, func() {
		r.subMu.Lock()
		defer r.subMu.Unlock()
		if c, ok := r.metaSubs[id]; ok {
			delete(r.metaSubs, id)
			close(c)
		}
	}
}

// SubscribeCapture streams capture start/end records until the returned cancel is called.
func (r *Receiver) SubscribeCapture() (<-chan Capture, func()) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	id := r.nextSub
	r.nextSub++
	ch := make(chan Capture, 8)
	r.capSubs[id] = ch
	return ch, func() {
		r.subMu.Lock()
		defer r.subMu.Unlock()
		if c, ok := r.capSubs[id]; ok {
			delete(r.capSubs, id)
			close(c)
		}
	}
}

func (r *Receiver) broadcastMeta(m Meta) {
	r.subMu.Lock()
	chans := make([]chan Meta, 0, len(r.metaSubs))
	for _, ch := range r.metaSubs {
		chans = append(chans, ch)
	}
	r.subMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- m:
		default:
		}
	}
}

func (r *Receiver) emitCapture(c Capture) {
	r.subMu.Lock()
	chans := make([]chan Capture, 0, len(r.capSubs))
	for _, ch := range r.capSubs {
		chans = append(chans, ch)
	}
	r.subMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- c:
		default:
		}
	}
}
