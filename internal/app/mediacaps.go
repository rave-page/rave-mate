package app

import (
	"context"
	"slices"
	"sync"

	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediapipe"
)

// mediacaps.go owns WHAT this node advertises as its video-encode capability and WHEN it probes for
// it - both governed by the activity governor:
//
//   - The §3.2 capability probe test-encodes every hardware encoder at launch. Each test encode takes
//     a REAL encode session, so running it while OBS streams can make OBS's own encoder fail. While
//     background work is disallowed we advertise the listing-only probe (no session, no GPU work) and
//     upgrade to the validated set once the stream ends.
//   - The advertised list is re-planned through encoderscan.PlanAdvertise on every streaming edge, so
//     a live stream steers peer routes off the silicon it holds and gets it back afterwards.
//
// The plan can only WITHHOLD or REORDER encoders (see PlanAdvertise): it never empties the list,
// because a peer with no negotiable codec falls back to raw video - the melt this whole epic fixes.
type mediaCaps struct {
	log  *logbus.Bus
	push func(enc, dec []string) // SetCodecCaps sink (in-proc router or the media child proxy)

	// Injected so the planner is unit-testable without a live machine.
	scan      func() encoderscan.Report // live encode-contention scan (nil → encoderscan.Detect)
	streaming func() bool               // is a stream live now? (nil → governor)
	cpu       func() float64            // system CPU % (late-bound: perfmon is built after this)

	mu         sync.Mutex
	probed     []string // encoder set from the probe, pre-plan (the planner never mutates it)
	decoders   []string
	validated  bool // probed came from test-encodes (not the listing-only probe)
	advertised []string
	last       encoderscan.AdvertisePlan
	wasLive    bool
}

func newMediaCaps(log *logbus.Bus, push func(enc, dec []string)) *mediaCaps {
	return &mediaCaps{log: log, push: push}
}

// SetCPUSource wires the system-CPU sampler once perfmon exists (nil-safe until then: an unknown CPU
// load just makes the CPU device look idle, and HW devices still outrank it on headroom).
func (m *mediaCaps) SetCPUSource(fn func() float64) {
	m.mu.Lock()
	m.cpu = fn
	m.mu.Unlock()
}

func (m *mediaCaps) report() encoderscan.Report {
	m.mu.Lock()
	scan := m.scan
	m.mu.Unlock()
	if scan != nil {
		return scan()
	}
	// OBS's CONFIGURED encoder comes from its profile ini (works with obs-websocket disconnected);
	// "is it live" comes from the governor, the single source of truth for that signal.
	return encoderscan.Detect(func() (stream, record string, active bool, err error) {
		s, r, ok := encoderscan.OBSConfigEncoder()
		if !ok {
			return "", "", false, nil
		}
		return s, r, m.live(), nil
	})
}

func (m *mediaCaps) live() bool {
	m.mu.Lock()
	fn := m.streaming
	m.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return governor.Snapshot().Streaming
}

func (m *mediaCaps) cpuPct() float64 {
	m.mu.Lock()
	fn := m.cpu
	m.mu.Unlock()
	if fn == nil {
		return 0
	}
	return fn()
}

// setProbed records a probe result and re-plans the advertisement.
func (m *mediaCaps) setProbed(enc, dec []string, validated bool) {
	m.mu.Lock()
	m.probed = slices.Clone(enc)
	m.decoders = slices.Clone(dec)
	m.validated = validated
	m.mu.Unlock()
	m.replan("probe")
}

// replan recomputes the advertised list from the probed set + the live contention scan and pushes it
// when it changed (SetCodecCaps re-advertises to every peer, so no-op pushes are wasted broadcasts).
func (m *mediaCaps) replan(reason string) {
	m.mu.Lock()
	probed, dec := slices.Clone(m.probed), slices.Clone(m.decoders)
	m.mu.Unlock()
	if len(probed) == 0 && len(dec) == 0 {
		return // nothing probed yet - the probe path pushes once it has something
	}
	ap := encoderscan.PlanAdvertise(probed, m.report(), m.cpuPct(), encoderscan.ExhaustReduceQuality)

	m.mu.Lock()
	changed := !slices.Equal(ap.Encoders, m.advertised)
	m.advertised = slices.Clone(ap.Encoders)
	m.last = ap
	m.mu.Unlock()
	if !changed {
		return
	}
	if m.log != nil {
		f := map[string]any{"reason": reason, "advertise": ap.Encoders, "plan": string(ap.Plan.Action)}
		if len(ap.Withheld) > 0 {
			f["withheld"] = ap.Withheld
		}
		m.log.Info("mediapipe", "encoder advertisement planned", f)
	}
	if m.push != nil {
		m.push(ap.Encoders, dec)
	}
}

// onGovernorChange re-plans on a STREAMING edge only. Focus/minimize/size-move flap with normal
// window use and a re-plan costs a process+PDH scan, so they are ignored.
func (m *mediaCaps) onGovernorChange() {
	live := m.live()
	m.mu.Lock()
	was := m.wasLive
	m.wasLive = live
	m.mu.Unlock()
	if was == live {
		return
	}
	reason := "stream ended"
	if live {
		reason = "stream live"
	}
	m.replan(reason)
}

// snapshot returns the current advertisement decision (diagnostics: ctl encoder-scan).
func (m *mediaCaps) snapshot() (advertised []string, validated bool, plan encoderscan.AdvertisePlan) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.advertised), m.validated, m.last
}

// probeAndAdvertise runs the capability probe under governor control and advertises the result.
// filter maps a raw probe result to the encoder set this node would advertise (SWOnly diagnostic
// filter + the native MF engine, which needs no ffmpeg); mfOnly is the fallback set when ffmpeg is
// missing entirely (nil = advertise nothing → routes stay raw/echo).
//
// While background work is disallowed (a stream is live) the LISTING-ONLY probe runs: full
// advertisement, zero encode sessions taken. The validated probe then upgrades it once the governor
// allows heavy work again - which may be never during a long stream, and that is fine: an in-build
// encoder that fails at route time is recoverable, a stolen NVENC session mid-stream is not.
func (m *mediaCaps) probeAndAdvertise(ctx context.Context, filter func(mediapipe.Caps) []string, mfOnly []string) {
	if !governor.BackgroundAllowed() {
		if c, ok := mediapipe.ProbeListing(ctx, m.log); ok {
			if m.log != nil {
				m.log.Info("mediapipe", "stream live at startup - advertising the listing-only probe (test encodes deferred: they take a real encode session)", nil)
			}
			m.setProbed(filter(c), c.Decoders, false)
		} else if len(mfOnly) > 0 {
			m.setProbed(mfOnly, nil, false)
		}
		governor.WaitWhileBusy(ctx) // parks in short slices until the stream ends (or ctx dies)
		if ctx.Err() != nil {
			return
		}
	}
	caps, ok := mediapipe.Probe(ctx, m.log)
	if !ok {
		if len(mfOnly) > 0 {
			// No ffmpeg: the native MF hardware encoder can still SOURCE h264 routes (receiving
			// still needs ffmpeg's decode child on the far end).
			if m.log != nil {
				m.log.Warn("mediapipe", "ffmpeg unavailable - sending via native MF encoder only, receiving disabled", nil)
			}
			m.setProbed(mfOnly, nil, false)
			return
		}
		if m.log != nil {
			m.log.Warn("mediapipe", "ffmpeg unavailable - video routes stay raw/echo", nil)
		}
		return
	}
	m.setProbed(filter(caps), caps.Decoders, caps.Validated)
}
