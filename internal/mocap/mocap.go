// Package mocap is the mocap feature module: it hosts the capture node (internal/mocapnode,
// reading the in-world mocap panel off desktop duplication / Spout / dshow) and the master
// (internal/mocapmaster, pose store + composite region renderer), and exposes the master's
// region painter to the VRSL stream encoder (vrslstream's per-frame overlay seam). The region
// rides the extended composite's calibration triad, so the stream must run in extended mode.
//
// ISOLATION: in-proc, vrslstream-style low-throughput supervisor. The heavy work (raw video
// decode) lives in the ffmpeg subprocess the mocapnode sources spawn - rave-mate only samples
// panel cells out of each raw frame and keeps a tiny pose store. The Spout path pulls a GPU
// shared texture in-process (videoshare precedent). Node restarts use the same capped 1->10s
// backoff as vrslstream.runFFmpeg.
package mocap

import (
	"context"
	"fmt"
	"image"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mocapmaster"
	"rave.page/mate/internal/mocapnode"
)

const source = "mocap"

// Service is the mocap module. cfg is re-read on each (re)start (the standard module pattern).
type Service struct {
	log   *logbus.Bus
	cfgFn func() config.MocapFeature

	mu      sync.Mutex
	running bool
	master  *mocapmaster.Master
	sink    func(mocapnode.Packet) // crew-relay seam: routes captured packets when set
	srcDesc string
	packets uint64
	lastErr string
}

// New builds the service.
func New(log *logbus.Bus, cfgFn func() config.MocapFeature) *Service {
	return &Service{log: log, cfgFn: cfgFn}
}

// Start launches the supervised capture node feeding the master, bound to ctx (module Start
// contract: non-blocking). Fails only on invalid config; capture hiccups degrade with a logged
// restart. On ctx cancel the overlay disappears from the stream (Overlay returns nil).
func (s *Service) Start(ctx context.Context) error {
	cfg := s.cfgFn()
	logf := func(format string, args ...any) {
		s.log.Warn(source, fmt.Sprintf(format, args...), nil)
	}
	master, err := mocapmaster.New(mocapmaster.Config{
		BoneSlots: cfg.ResolvedBoneSlots(),
		StageMin:  cfg.ResolvedStageMin(), StageSize: cfg.ResolvedStageSize(),
		Logf: logf,
	})
	if err != nil {
		return err
	}
	_, desc, err := buildSource(cfg, s.log, logf) // fail fast on invalid capture config
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.running, s.master, s.srcDesc = true, master, desc
	s.packets, s.lastErr = 0, ""
	s.mu.Unlock()
	s.log.Info(source, "mocap capture up", map[string]any{
		"source": desc, "boneSlots": cfg.ResolvedBoneSlots(), "fps": cfg.ResolvedFPS(),
	})

	debuglog.Go(s.log, source, func() { s.runNode(ctx, cfg, master, logf) })
	debuglog.Go(s.log, source, func() {
		<-ctx.Done()
		s.mu.Lock()
		s.running, s.master = false, nil // overlay off the stream immediately
		s.mu.Unlock()
	})
	return nil
}

// runNode supervises the capture node: build source+node -> Run -> restart with capped backoff
// on a fatal source error (transient capture failures the ffmpeg sources already ride out
// internally). A Node is Run-once, so each attempt builds a fresh one; the master (pose store)
// persists across restarts.
func (s *Service) runNode(ctx context.Context, cfg config.MocapFeature, master *mocapmaster.Master, logf func(string, ...any)) {
	backoff := time.Second
	for ctx.Err() == nil {
		src, _, err := buildSource(cfg, s.log, logf)
		if err != nil {
			s.setErr(err.Error()) // invalid config - no point retrying until a restart
			return
		}
		node := mocapnode.New(mocapnode.Config{
			Source:   src,
			OnPacket: func(pkt mocapnode.Packet) { s.countPacket(); s.route(pkt, master) },
			Logf:     logf,
		})
		started := time.Now()
		if err := node.Run(ctx); err != nil && ctx.Err() == nil {
			s.setErr(err.Error())
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second // stable run - reset
		}
		s.log.Warn(source, "capture node exited - restarting", map[string]any{"backoff": backoff.String()})
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

// route dispatches one captured packet: the crew sink when installed, else the local master.
func (s *Service) route(pkt mocapnode.Packet, master *mocapmaster.Master) {
	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	if sink != nil {
		sink(pkt)
		return
	}
	master.OnPacket(pkt)
}

// SetSink installs a pluggable packet sink (the crew-relay seam): captured packets route to
// fn INSTEAD of the local master; nil restores the default. A sink that also wants the local
// overlay calls Inject itself (crew node role does). Survives supervised node restarts.
func (s *Service) SetSink(fn func(mocapnode.Packet)) {
	s.mu.Lock()
	s.sink = fn
	s.mu.Unlock()
}

// Inject feeds one packet into the persistent master (remote crew ingest, or a sink keeping
// the local overlay alive). Reports false while the module is stopped - the packet is
// dropped, the caller counts it. Keeps the one-persistent-Master invariant: remote packets
// join the same store/election as local capture.
func (s *Service) Inject(pkt mocapnode.Packet) bool {
	s.mu.Lock()
	m := s.master
	s.mu.Unlock()
	if m == nil {
		return false
	}
	m.OnPacket(pkt)
	return true
}

// Overlay returns the master's composite-region painter while the service runs, nil otherwise -
// the vrslstream per-frame overlay provider (resolved fresh each rendered frame, so toggling
// the module adds/removes the region without a stream restart).
func (s *Service) Overlay() func(*image.RGBA) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.master == nil {
		return nil
	}
	return s.master.Overlay()
}

// buildSource maps cfg onto a mocapnode Source (+ a human description for status). Mirrors
// cmd/mocapnode-probe's mapping; the raw pipe needs fixed geometry, so desktop captures the
// canonical 1920x1080 composite area (the config carries no size field).
func buildSource(cfg config.MocapFeature, log *logbus.Bus, logf func(string, ...any)) (mocapnode.Source, string, error) {
	fps := cfg.ResolvedFPS()
	switch cfg.ResolvedSource() {
	case "spout":
		if strings.TrimSpace(cfg.Device) == "" {
			return nil, "", fmt.Errorf("mocap: spout source needs a sender name (Settings -> Mocap capture)")
		}
		return &mocapnode.SpoutSource{Log: log, Sender: cfg.Device}, "spout " + cfg.Device, nil
	case "dshow":
		if strings.TrimSpace(cfg.Device) == "" {
			return nil, "", fmt.Errorf("mocap: dshow source needs a device name (Settings -> Mocap capture)")
		}
		// W/H zero = the source's 1920x1080 default (the OBS canvas default).
		return &mocapnode.FFmpegDShowSource{Device: cfg.Device, FPS: fps, Logf: logf}, "dshow " + cfg.Device, nil
	default:
		return &mocapnode.FFmpegDesktopSource{Monitor: cfg.Monitor, FPS: fps, W: 1920, H: 1080, Logf: logf},
			fmt.Sprintf("desktop monitor %d", cfg.Monitor), nil
	}
}

func (s *Service) countPacket() {
	s.mu.Lock()
	s.packets++
	s.lastErr = ""
	s.mu.Unlock()
}

func (s *Service) setErr(msg string) {
	s.mu.Lock()
	if msg != "" && s.lastErr != msg {
		s.log.Warn(source, "capture error", map[string]any{"error": msg})
	}
	s.lastErr = msg
	s.mu.Unlock()
}

// Status is the live snapshot for the settings card + ctl mocap-status.
type Status struct {
	Running bool
	Source  string // human source description
	Packets uint64 // decoded panel packets accepted this run
	Dancers int    // active (fresh) dancers in the pose store
	LastErr string
}

// Status returns the live snapshot.
func (s *Service) Status() Status {
	s.mu.Lock()
	st := Status{Running: s.running, Source: s.srcDesc, Packets: s.packets, LastErr: s.lastErr}
	master := s.master
	s.mu.Unlock()
	if master != nil {
		st.Dancers = len(master.Store().ActiveDancers(time.Now()))
	}
	return st
}

// StatusText renders the snapshot as the multi-line ctl reply.
func (s *Service) StatusText() string {
	st := s.Status()
	if !st.Running {
		return "mocap off (enable it in Settings -> Mocap capture)"
	}
	out := "mocap running - source " + st.Source + "\n"
	switch {
	case st.LastErr != "":
		out += "status: ERROR " + st.LastErr + "\n"
	default:
		out += fmt.Sprintf("status: %d packet(s), %d active dancer(s)\n", st.Packets, st.Dancers)
	}
	return out
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
