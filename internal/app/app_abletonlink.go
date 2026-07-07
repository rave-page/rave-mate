package app

import (
	"context"
	"strconv"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/resolume"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/session/aggregator"
)

// linkBridgeTick is the DJ→Link publish cadence (tempo pushed every tick; phase align + Resolume
// phrase-boundary detection at this resolution - 200ms is <3% of a phrase at 128 BPM/16 beats).
const linkBridgeTick = 200 * time.Millisecond

// linkPhaseAlignEvery throttles beat-phase realign so continuous RequestBeat doesn't fight Link's
// own convergence - tempo lock is the primary goal; phase is a best-effort nudge.
const linkPhaseAlignEvery = time.Second

// runLinkBridge reads the fused DJ master (session Merger) and drives the Link session's tempo +
// phrase phase when this node owns the tempo, and (if Resolume is configured) re-triggers a phrase
// clip on each Link phrase boundary. Runs until ctx is done (the module lifetime).
//
// Tempo-owner role (config): "always"/"auto" → drive Link from the DJ master; "follow" → never
// drive (join the session read-only, follow a peer's tempo). Multi-rave-mate election (mirroring
// the timecode Plane lowest-NodeID rule) is a follow-up - a single DJ bridge is the common case and
// "auto" drives it. Link's own session already reconciles tempo across peers.
func runLinkBridge(ctx context.Context, agg *aggregator.Aggregator, link *featurehost.AbletonLinkProxy, cfgFn func() config.AbletonLinkFeature) {
	tick := time.NewTicker(linkBridgeTick)
	defer tick.Stop()

	var (
		lastPhaseAlign time.Time
		rc             *resolume.Client
		rcKey          string  // host:oscPort:restPort the client was built for (rebuild on change)
		prevFrac       float64 // previous Link phrase fraction (0..1) for boundary detection
		havePrev       bool
	)
	defer func() {
		if rc != nil {
			_ = rc.Close()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			cfg := cfgFn()

			// Drive Link tempo/phase from the DJ master (unless following a peer).
			if cfg.ResolvedTempoOwner() != "follow" {
				u := agg.Snapshot()
				if bpm, ok := masterFloat(u, session.FieldBPM); ok && bpm > 0 {
					b := featurehost.LinkBridge{Drive: true, Tempo: bpm}
					if now.Sub(lastPhaseAlign) >= linkPhaseAlignEvery {
						if ph, hasPh := masterFloat(u, session.FieldPhase); hasPh {
							b.Phase, b.HasPhase = ph, true
							lastPhaseAlign = now
						}
					}
					if cfg.StartStopSync {
						_, playing := u.DeriveNowPlaying()
						b.Playing, b.HasPlaying = playing, true
					}
					_ = link.PushBridge(b) // fire-and-forget; a down child no-ops, next tick retries
				}
			}

			// Resolume phrase-boundary clip trigger (works in follow mode too - Link may come from
			// a peer). Rebuild the client on config change; nil when disabled.
			rc, rcKey = ensureResolume(rc, rcKey, cfg.Resolume)
			if rc == nil || !cfg.Resolume.HasPhraseClip() {
				havePrev = false
				continue
			}
			st := link.State()
			if !st.Available || !st.Enabled {
				havePrev = false
				continue
			}
			frac := st.PhraseFraction()
			if havePrev && frac < prevFrac { // wrapped past the phrase boundary
				_ = rc.ConnectClip(cfg.Resolume.PhraseClipLayer, cfg.Resolume.PhraseClipClip) // OSC, fire-and-forget
			}
			prevFrac, havePrev = frac, true
		}
	}
}

// ensureResolume returns a Resolume client for the current config, rebuilding it when the
// host/ports change and closing it (returning nil) when Resolume is disabled. key tracks what the
// current client was built for.
func ensureResolume(cur *resolume.Client, key string, cfg config.ResolumeConfig) (*resolume.Client, string) {
	if !cfg.Enabled {
		if cur != nil {
			_ = cur.Close()
		}
		return nil, ""
	}
	want := cfg.ResolvedHost() + ":" + strconv.Itoa(cfg.ResolvedOSCPort()) + ":" + strconv.Itoa(cfg.ResolvedRESTPort())
	if cur != nil && key == want {
		return cur, key
	}
	if cur != nil {
		_ = cur.Close()
	}
	return resolume.New(cfg.ResolvedHost(), cfg.ResolvedOSCPort(), cfg.ResolvedRESTPort()), want
}

// masterFloat reads a float64 master field from the merged state (the Merger stores float64).
func masterFloat(u session.UnifiedState, field string) (float64, bool) {
	fv, ok := u.Master[field]
	if !ok {
		return 0, false
	}
	f, ok := fv.Value.(float64)
	return f, ok
}
