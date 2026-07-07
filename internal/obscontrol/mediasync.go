package obscontrol

// Media-sync tier: keep chosen OBS media sources locked to a house clock across the sources this
// node owns (local OBS + direct LAN remotes). The chase control law lives in internal/mediasync;
// this file wires it to obscontrol's already-managed OBS connections + a wall-clock house clock.
//
// Sync is daemon-side + in-proc, mirroring the direct-LAN remote pattern (obs-websocket is plain
// JSON over a websocket - small blast radius). The local OBS is reached through the crash-isolated
// featurehost proxy; remotes through the in-proc direct client. Both satisfy MediaController.

import (
	"context"
	"time"

	"rave.page/mate/internal/mediasync"
)

const (
	syncEvery   = 500 * time.Millisecond // chase tick cadence (corrections are step-jumps; no need to run per-frame)
	syncTimeout = 3 * time.Second        // per-source obs-websocket round-trip budget
)

// SyncConfig is the media-sync config, re-read each tick (a settings edit takes effect live).
type SyncConfig struct {
	Enabled            bool
	DeadBandFrames     float64
	Fps                float64
	RestartThresholdMs int
	Sources            []SyncSource
}

// SyncSource is one media input to keep in sync on a chosen endpoint.
type SyncSource struct {
	Endpoint       string // "" / "local" = local OBS; else a source id (obs@host:port)
	InputName      string
	InputKind      string
	StaticOffsetMs int
	Enabled        bool
}

// SetSyncConfig installs the live config accessor (nil = sync disabled). Call once at wiring.
func (m *Manager) SetSyncConfig(fn func() SyncConfig) { m.syncCfg = fn }

// StartSync anchors the house clock to "now" (timeline 0) and begins chasing.
func (m *Manager) StartSync() { m.syncClock.StartNow() }

// StopSync freezes the house clock; chasers go idle (media is left where it is).
func (m *Manager) StopSync() { m.syncClock.Stop() }

// SyncRunning reports whether the house clock is advancing.
func (m *Manager) SyncRunning() bool { return m.syncClock.Running() }

// SyncStatuses returns each chaser's live status (for the UI + ctl obs-sync-status).
func (m *Manager) SyncStatuses() []mediasync.Status {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	out := make([]mediasync.Status, 0, len(m.chasers))
	for _, c := range m.chasers {
		out = append(out, c.Status())
	}
	return out
}

// resolveEndpoint maps a config endpoint to an owned source id ("" / "local" → the local OBS).
func (m *Manager) resolveEndpoint(ep string) string {
	if ep == "" || ep == "local" {
		return m.localID
	}
	return ep
}

// controllerFor returns the MediaController for an owned source id, if that source supports media
// requests (local proxy + direct remotes both do).
func (m *Manager) controllerFor(sourceID string) (mediasync.MediaController, bool) {
	m.mu.Lock()
	s := m.srcs[sourceID]
	m.mu.Unlock()
	if s == nil || s.obs == nil {
		return nil, false
	}
	mc, ok := s.obs.(mediasync.MediaController)
	return mc, ok
}

// chaserKey identifies a chaser by endpoint + input.
func chaserKey(endpoint, input string) string { return endpoint + "\x00" + input }

// tickSync reconciles chasers from config, then ticks each once (skipping when disabled). Ticks
// run concurrently per source so one slow OBS doesn't stall the others within the cadence.
func (m *Manager) tickSync(ctx context.Context) {
	cfg := SyncConfig{}
	if m.syncCfg != nil {
		cfg = m.syncCfg()
	}
	m.reconcileChasers(cfg)
	if !cfg.Enabled {
		return
	}

	m.syncMu.Lock()
	chasers := make([]*mediasync.Chaser, 0, len(m.chasers))
	for _, c := range m.chasers {
		chasers = append(chasers, c)
	}
	m.syncMu.Unlock()

	for _, c := range chasers {
		go func(ch *mediasync.Chaser) {
			cctx, cancel := context.WithTimeout(ctx, syncTimeout)
			defer cancel()
			if err := ch.Tick(cctx); err != nil {
				m.log.Debug(logTag, "sync tick", map[string]any{"source": ch.Config().InputName, "error": err.Error()})
			}
		}(c)
	}
}

// reconcileChasers rebuilds the chaser set from config: adds new / changed ones, drops removed.
// A chaser is rebuilt when its resolved source config changes (so tunables/offset edits apply).
func (m *Manager) reconcileChasers(cfg SyncConfig) {
	want := map[string]mediasync.SourceConfig{}
	if cfg.Enabled {
		for _, s := range cfg.Sources {
			if !s.Enabled || s.InputName == "" {
				continue
			}
			key := chaserKey(s.Endpoint, s.InputName)
			want[key] = mediasync.SourceConfig{
				Endpoint:           s.Endpoint,
				InputName:          s.InputName,
				InputKind:          s.InputKind,
				StaticOffsetMs:     s.StaticOffsetMs,
				Fps:                cfg.Fps,
				DeadBandFrames:     cfg.DeadBandFrames,
				RestartThresholdMs: cfg.RestartThresholdMs,
			}
		}
	}

	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	// Drop chasers no longer wanted.
	for key := range m.chasers {
		if _, ok := want[key]; !ok {
			delete(m.chasers, key)
		}
	}
	// Add / rebuild chasers.
	for key, sc := range want {
		if ex, ok := m.chasers[key]; ok && ex.Config() == sc {
			continue // unchanged
		}
		mc, ok := m.controllerFor(m.resolveEndpoint(sc.Endpoint))
		if !ok {
			delete(m.chasers, key) // endpoint not connected yet - retry next reconcile
			continue
		}
		m.chasers[key] = mediasync.NewChaser(mediasync.MediaControllerCfg{SourceConfig: sc, Ctrl: mc}, m.syncClock)
	}
}
