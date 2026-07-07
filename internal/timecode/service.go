package timecode

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

const source = "timecode"

// ltcSampleRate is the fixed LTC render rate (48kHz, the SMPTE-into-media-software norm).
const ltcSampleRate = 48000

// Service is the in-proc timecode module: one master Generator plus the three sinks (LTC audio /
// MTC / Art-Net), each independently enabled from config. The module (app lifecycle) arms it; the
// clock is started/stopped explicitly (UI START/STOP or `ctl tc-start`/`tc-stop`). Light + no cgo.
type Service struct {
	log *logbus.Bus
	cfg func() config.TimecodeFeature
	gen *Generator

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	ltcs    []*ltcGen  // live LTC emitters (one per audio device - for jam reseed); empty when off
	mtcs    []*midiOut // live MTC ports (for jam full-frame + close); empty when off
	plane   Plane      // medialink timecode plane (master announce / slave chase); nil = standalone
}

// Plane is the medialink timecode-plane surface (medialink.TCPlane) the service drives as its
// local house-clock source when elected master and follows when a paired instance is master.
type Plane interface {
	SetLocalSource(fn medialink.TCSource)
	SetChase(fn func(frame int64, rate medialink.Rate))
	AnnounceNow()
	Status() medialink.TCStatus
}

// New builds the module. cfg reads the current TimecodeFeature (live).
func New(log *logbus.Bus, cfg func() config.TimecodeFeature) *Service {
	return &Service{log: log, cfg: cfg, gen: NewGenerator()}
}

// Now returns the current house timecode + whether the clock is running.
func (s *Service) Now() (Timecode, bool) { return s.gen.Now(), s.gen.Running() }

// FollowClock re-bases the master frame counter onto the medialink media clock (ns) - TC egress
// (LTC/MTC/Art-Net) then advances on the disciplined cross-instance domain instead of local wall
// time (§4). Call once at wiring time, before the clock starts.
func (s *Service) FollowClock(nowNs func() int64) {
	base := time.Now()
	s.gen.SetNow(func() time.Time { return base.Add(time.Duration(nowNs())) })
}

// AttachPlane wires the house clock into the medialink timecode plane: the generator is the
// plane's announce source when this node is elected master; while a paired instance is master
// the running local clock chases it (jam on divergence past chaseTolFrames).
func (s *Service) AttachPlane(p Plane) {
	s.mu.Lock()
	s.plane = p
	s.mu.Unlock()
	p.SetLocalSource(func() (int64, medialink.Rate, bool) {
		return s.gen.FrameNow(), s.gen.Rate(), s.gen.Running()
	})
	p.SetChase(s.chaseRemote)
}

// chaseTolFrames is the slave-chase dead band (±1 frame = the §8 P3 accept tolerance).
const chaseTolFrames = 1

// chaseRemote jams the running local clock onto the elected remote master's position when it
// diverges past the dead band (rate follows the master's).
func (s *Service) chaseRemote(frame int64, rate medialink.Rate) {
	if !s.gen.Running() {
		return
	}
	if d := frame - s.gen.FrameNow(); d > chaseTolFrames || d < -chaseTolFrames {
		s.Jam(medialink.TimecodeFromFrames(frame, rate))
	}
}

// announce pushes the current state onto the plane (no-op when detached or not master).
func (s *Service) announce() {
	s.mu.Lock()
	p := s.plane
	s.mu.Unlock()
	if p != nil {
		p.AnnounceNow()
	}
}

// Running reports whether the clock is emitting.
func (s *Service) Running() bool { return s.gen.Running() }

// startTC resolves the configured StartAt into a starting Timecode: "clock" = time-of-day, a
// hh:mm:ss:ff literal, else 00:00:00:00.
func startTC(f config.TimecodeFeature, rate Rate) Timecode {
	if f.StartAt == "clock" {
		now := time.Now()
		tc := Timecode{H: now.Hour(), M: now.Minute(), S: now.Second(), Rate: rate}
		num, den := rate.Exact()
		if num > 0 {
			tc.F = int(int64(now.Nanosecond()) * num / (int64(time.Second) * den))
		}
		return tc
	}
	return ParseStartTC(f.StartAt, rate)
}

// StartClock (re)starts the master clock + opens every enabled sink. Idempotent-safe: a running
// clock is stopped first. Returns nil even if a sink's device is unavailable (that sink logs +
// stays off; the others run).
func (s *Service) StartClock() error {
	s.StopClock()

	f := s.cfg()
	rate := ParseRate(f.ResolvedRate())
	start := startTC(f, rate)
	s.gen.Start(rate, start)

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()

	ltcs := f.LTCSinks()
	mtcs := f.MTCSinks()
	artnets := f.ArtNetSinks()
	for _, sink := range ltcs {
		s.startLTC(ctx, sink, rate)
	}
	for _, sink := range mtcs {
		s.startMTC(ctx, sink, rate)
	}
	for _, sink := range artnets {
		s.startArtNet(ctx, sink, rate)
	}
	s.log.Info(source, "clock started", map[string]any{
		"rate": f.ResolvedRate(), "start": start.String(),
		"ltc": len(ltcs), "mtc": len(mtcs), "artnet": len(artnets),
	})
	s.announce() // §4 on-jump announce (no-op unless elected TC master)
	return nil
}

// StopClock halts the clock + closes every sink.
func (s *Service) StopClock() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.running = false
	mtcs := s.mtcs
	s.mtcs, s.ltcs = nil, nil
	s.mu.Unlock()

	stopFrame := mtcFullFrame(s.gen.Now())
	for _, m := range mtcs {
		_ = m.long(stopFrame) // full-frame locate at the stop position
	}
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	for _, m := range mtcs {
		m.close()
	}
	s.gen.Stop()
	s.announce()
}

// Jam re-locates the clock to tc live (all running sinks follow): re-seeds the LTC counter + emits
// an MTC full-frame locate.
func (s *Service) Jam(tc Timecode) {
	s.gen.Jam(tc)
	s.mu.Lock()
	ltcs, mtcs := s.ltcs, s.mtcs
	s.mu.Unlock()
	frame := tc.Frames()
	for _, g := range ltcs {
		g.reseed(frame)
	}
	full := mtcFullFrame(tc)
	for _, m := range mtcs {
		_ = m.long(full)
	}
	s.announce()
}

// startLTC spawns one audio-out streamer for sink on its own device. Free-runs from the generator's
// current frame; multiple LTC sinks seeded at the same frame stay frame-locked. Called per sink.
func (s *Service) startLTC(ctx context.Context, sink config.TCLTCSink, rate Rate) {
	g := newLTCGen(rate, ltcSampleRate, gainToAmp(sink.ResolvedGainDb()), s.gen.FrameNow())
	s.mu.Lock()
	s.ltcs = append(s.ltcs, g)
	s.mu.Unlock()
	dev := sink.Device
	s.wg.Add(1)
	debuglog.Go(s.log, source, func() {
		defer s.wg.Done()
		if err := playLTC(ctx, dev, ltcSampleRate, g.fill); err != nil && ctx.Err() == nil {
			s.log.Warn(source, "LTC audio output stopped", map[string]any{"device": dev, "error": err.Error()})
		}
	})
}

// startMTC opens the MIDI port for sink, emits a full-frame locate, then streams quarter-frames
// (4/frame), each derived from the generator (drift-free). Called per sink.
func (s *Service) startMTC(ctx context.Context, sink config.TCMTCSink, rate Rate) {
	out, err := openMidiOut(sink.Device)
	if err != nil {
		s.log.Warn(source, "MTC output unavailable", map[string]any{"device": sink.Device, "error": err.Error()})
		return
	}
	s.mu.Lock()
	s.mtcs = append(s.mtcs, out)
	s.mu.Unlock()
	_ = out.long(mtcFullFrame(s.gen.Now()))

	num, den := rate.Exact()
	qfInterval := time.Duration(int64(time.Second) * den / (num * 4)) // quarter-frame period
	s.wg.Add(1)
	debuglog.Go(s.log, source, func() {
		defer s.wg.Done()
		t := time.NewTicker(qfInterval)
		defer t.Stop()
		last := int64(-1)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// Monotonic quarter-frame index from the master (resynced each tick - no cumulative
				// drift; a duplicate tick within one QF period is skipped). Each 8-piece group spans
				// 2 frames; the group's timecode = the frame at group start (receiver lags ≤2 frames,
				// spec-expected).
				qf := s.gen.QuarterFrameNow()
				if qf == last {
					continue
				}
				last = qf
				piece := int(qf & 7)
				groupFrame := (qf / 8) * 2
				tc := medialink.TimecodeFromFrames(groupFrame, rate)
				out.short(0xF1, mtcQuarterFrame(tc, piece), 0)
			}
		}
	})
}

// startArtNet opens the UDP socket for sink + streams one ArtTimeCode packet per frame (resynced to
// the master each tick). Called per sink.
func (s *Service) startArtNet(ctx context.Context, sink config.TCArtNetSink, rate Rate) {
	addr := sink.ResolvedAddr()
	conn, err := net.Dial("udp", addr)
	if err != nil {
		s.log.Warn(source, "Art-Net TimeCode output unavailable", map[string]any{"addr": addr, "error": err.Error()})
		return
	}
	num, den := rate.Exact()
	frameInterval := time.Duration(int64(time.Second) * den / num)
	s.wg.Add(1)
	debuglog.Go(s.log, source, func() {
		defer s.wg.Done()
		defer func() { _ = conn.Close() }()
		t := time.NewTicker(frameInterval)
		defer t.Stop()
		var last Timecode
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tc := s.gen.Now()
				if tc == last {
					continue // same frame - one packet per frame
				}
				last = tc
				p := artTimeCodePacket(tc)
				if _, err := conn.Write(p[:]); err != nil {
					s.log.Warn(source, "Art-Net TimeCode send failed", map[string]any{"error": err.Error()})
					return
				}
			}
		}
	})
}

// StatusLine is the one-line ctl TC-STATUS report (+ plane role when attached).
func (s *Service) StatusLine() string {
	f := s.cfg()
	tc, running := s.Now()
	line := fmt.Sprintf("running=%v rate=%s tc=%s ltc=%d mtc=%d artnet=%d",
		running, f.ResolvedRate(), tc.String(), len(f.LTCSinks()), len(f.MTCSinks()), len(f.ArtNetSinks()))
	s.mu.Lock()
	p := s.plane
	s.mu.Unlock()
	if p != nil {
		st := p.Status()
		if st.Role == medialink.TCRoleMaster {
			line += " tcmaster=self"
		} else {
			line += " tcmaster=" + st.Master
			if st.Holdover {
				line += " (holdover)"
			}
		}
	}
	return line
}
