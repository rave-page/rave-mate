// Package rtspserve is the local performer video chain from DMX_TIMECODE_RESEARCH.md:
// VRChat has no Spout/NDI ingest, but its AVPro player takes rtspt:// (RTSP over TCP)
// with ~0.5s latency. An ffmpeg subprocess (existing worker precedent: mediatools.Resolve
// + sysexec) encodes the configured source to Annex-B H.264 on stdout; this package
// splits it into access units and serves them to LAN consumers over a minimal stdlib-net
// RTSP/RTP server (TCP-interleaved only - exactly what rtspt:// speaks). Replaces the
// OBS→RTMP→MediaMTX relay with one app.
package rtspserve

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

const source = "rtspserve"

// Server is the RTSP module. cfg is re-read on each (re)start (the standard module pattern).
type Server struct {
	log   *logbus.Bus
	cfgFn func() config.RTSPServeFeature
	// runSource feeds the hub; defaults to the ffmpeg subprocess, injectable for tests.
	runSource func(ctx context.Context, cfg config.RTSPServeFeature, fd *feed)

	mu       sync.Mutex
	running  bool
	addr     string
	clients  int
	restarts int
	srcUp    bool
	lastErr  string
	feed     *feed
}

// New builds the server.
func New(log *logbus.Bus, cfgFn func() config.RTSPServeFeature) *Server {
	s := &Server{log: log, cfgFn: cfgFn}
	s.runSource = s.runFFmpeg
	return s
}

// Start binds the RTSP listener and launches the source (module Start contract:
// non-blocking, everything bound to ctx). Fails on missing source config or a busy port.
func (s *Server) Start(ctx context.Context) error {
	cfg := s.cfgFn()
	if strings.TrimSpace(cfg.Source) == "" {
		return errors.New("rtspserve: no video source configured (Settings → RTSP)")
	}
	ln, err := net.Listen("tcp", cfg.ResolvedListenAddr())
	if err != nil {
		return fmt.Errorf("rtspserve: listen %s: %w", cfg.ResolvedListenAddr(), err)
	}
	fd := newFeed()
	s.mu.Lock()
	s.running, s.addr, s.feed = true, ln.Addr().String(), fd
	s.clients, s.restarts, s.lastErr, s.srcUp = 0, 0, "", false
	s.mu.Unlock()
	s.log.Info(source, "rtsp server up", map[string]any{"addr": ln.Addr().String(), "path": cfg.ResolvedPath()})

	debuglog.Go(s.log, source, func() { s.runSource(ctx, cfg, fd) })
	debuglog.Go(s.log, source, func() {
		<-ctx.Done()
		_ = ln.Close()
		s.mu.Lock()
		s.running, s.srcUp = false, false
		s.mu.Unlock()
	})
	debuglog.Go(s.log, source, func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return // listener closed (ctx done)
			}
			debuglog.Go(s.log, source, func() { s.handleConn(ctx, c, cfg, fd) })
		}
	})
	return nil
}

// Status is the live snapshot for the settings card.
type Status struct {
	Running  bool
	Addr     string // bound listen address
	Clients  int
	SourceUp bool   // ffmpeg currently running
	Restarts int    // source restarts this run
	AUs      uint64 // access units fanned out
	LastErr  string
}

// Status returns the live snapshot.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Running: s.running, Addr: s.addr, Clients: s.clients,
		SourceUp: s.srcUp, Restarts: s.restarts, LastErr: s.lastErr}
	if s.feed != nil {
		st.AUs = s.feed.count()
	}
	return st
}

func (s *Server) setErr(msg string) {
	s.mu.Lock()
	if msg != "" && s.lastErr != msg {
		s.log.Warn(source, "source error", map[string]any{"error": msg})
	}
	s.lastErr = msg
	s.mu.Unlock()
}

// ── source: supervised ffmpeg subprocess ─────────────────────────────────────

// runFFmpeg supervises the encode subprocess: spawn → pipe stdout through the NAL/AU
// chain into the feed → restart with capped backoff on exit (source hiccups must not
// kill the chain mid-set).
func (s *Server) runFFmpeg(ctx context.Context, cfg config.RTSPServeFeature, fd *feed) {
	backoff := time.Second
	for ctx.Err() == nil {
		ffmpeg, ok := mediatools.Resolve("ffmpeg")
		if !ok {
			s.setErr("ffmpeg not found (install it in Settings → Library & media, or add to PATH)")
			if !sleepCtx(ctx, 10*time.Second) {
				return
			}
			continue
		}
		started := time.Now()
		err := s.runFFmpegOnce(ctx, ffmpeg, cfg, fd)
		s.mu.Lock()
		s.srcUp = false
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.setErr(err.Error())
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second // stable run - reset
		}
		s.mu.Lock()
		s.restarts++
		s.mu.Unlock()
		s.log.Warn(source, "ffmpeg exited - restarting", map[string]any{"backoff": backoff.String()})
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

// runFFmpegOnce runs one encode process to completion.
func (s *Server) runFFmpegOnce(ctx context.Context, ffmpeg string, cfg config.RTSPServeFeature, fd *feed) error {
	cmd := exec.CommandContext(ctx, ffmpeg, ffmpegArgs(cfg)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	tail := &tailWriter{}
	cmd.Stderr = tail
	sysexec.Hide(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	s.mu.Lock()
	s.srcUp = true
	s.mu.Unlock()
	s.setErr("")

	asm := &auAssembler{onAU: fd.publish, onParams: fd.setParams}
	split := &nalSplitter{onNAL: asm.addNAL}
	buf := make([]byte, 64<<10)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			_, _ = split.Write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	split.Flush()
	asm.Flush()
	werr := cmd.Wait()
	if werr != nil && ctx.Err() == nil {
		return fmt.Errorf("ffmpeg: %v - %s", werr, tail.String())
	}
	return nil
}

// ffmpegArgs builds the encode command: source → zero-latency H.264 elementary stream on
// stdout, AUDs inserted so the assembler has exact frame boundaries.
func ffmpegArgs(cfg config.RTSPServeFeature) []string {
	fps := strconv.Itoa(cfg.ResolvedFPS())
	kbps := cfg.ResolvedBitrate()
	args := []string{"-hide_banner", "-loglevel", "warning", "-fflags", "nobuffer"}
	if f := strings.TrimSpace(cfg.InputFormat); f != "" {
		args = append(args, "-f", f)
		if f == "gdigrab" || f == "x11grab" || f == "avfoundation" {
			args = append(args, "-framerate", fps) // grab demuxers only - others reject the option
		}
	}
	args = append(args, "-i", cfg.Source, "-an")
	if cfg.Passthrough {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
			"-pix_fmt", "yuv420p", "-r", fps, "-g", fps, // 1s keyframes (research: fast join/recover)
			"-b:v", fmt.Sprintf("%dk", kbps), "-maxrate", fmt.Sprintf("%dk", kbps),
			"-bufsize", fmt.Sprintf("%dk", 2*kbps))
	}
	return append(args, "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "-")
}

// sleepCtx sleeps d or until ctx cancel; false when cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// tailWriter keeps the last chunk of stderr for error reporting.
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 1024 {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-1024:]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// ── feed: AU fan-out hub ─────────────────────────────────────────────────────

// feed is the hub between the one source and N streaming clients. Slow clients drop whole
// access units and resync at the next keyframe (never block the source).
type feed struct {
	mu    sync.Mutex
	sps   []byte
	pps   []byte
	ready chan struct{} // closed once SPS+PPS are known (DESCRIBE can answer)
	subs  map[*subscriber]struct{}
	aus   uint64
}

type subscriber struct {
	ch      chan accessUnit
	waitKey bool // skip until next IDR (join / after drop)
}

func newFeed() *feed {
	return &feed{ready: make(chan struct{}), subs: map[*subscriber]struct{}{}}
}

// setParams stores the latest SPS/PPS and marks the feed describable.
func (f *feed) setParams(sps, pps []byte) {
	f.mu.Lock()
	first := f.sps == nil || f.pps == nil
	f.sps, f.pps = sps, pps
	f.mu.Unlock()
	if first {
		select {
		case <-f.ready:
		default:
			close(f.ready)
		}
	}
}

// params returns the current SPS/PPS (nil until seen).
func (f *feed) params() (sps, pps []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sps, f.pps
}

// publish fans an access unit out to all subscribers (non-blocking; laggards resync on key).
func (f *feed) publish(au accessUnit) {
	f.mu.Lock()
	f.aus++
	for sub := range f.subs {
		if sub.waitKey && !au.key {
			continue
		}
		select {
		case sub.ch <- au:
			sub.waitKey = false
		default:
			sub.waitKey = true // dropped - wait for the next IDR to resume cleanly
		}
	}
	f.mu.Unlock()
}

func (f *feed) count() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.aus
}

func (f *feed) subscribe() *subscriber {
	sub := &subscriber{ch: make(chan accessUnit, 32), waitKey: true}
	f.mu.Lock()
	f.subs[sub] = struct{}{}
	f.mu.Unlock()
	return sub
}

func (f *feed) unsubscribe(sub *subscriber) {
	f.mu.Lock()
	delete(f.subs, sub)
	f.mu.Unlock()
}

// ── RTSP protocol (TCP-interleaved only) ─────────────────────────────────────

// sdp builds the session description advertising the H.264 stream.
func sdp(sps, pps []byte) string {
	prof := ""
	if len(sps) >= 4 {
		prof = fmt.Sprintf("profile-level-id=%02X%02X%02X;", sps[1], sps[2], sps[3])
	}
	return "v=0\r\n" +
		"o=- 0 0 IN IP4 0.0.0.0\r\n" +
		"s=rave-mate\r\n" +
		"t=0 0\r\n" +
		"m=video 0 RTP/AVP 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=rtpmap:96 H264/90000\r\n" +
		"a=fmtp:96 packetization-mode=1;" + prof + "sprop-parameter-sets=" +
		base64.StdEncoding.EncodeToString(sps) + "," + base64.StdEncoding.EncodeToString(pps) + "\r\n" +
		"a=control:streamid=0\r\n"
}
