package app

import (
	"context"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/senderkeeper"
)

// keeperKeepAlive is the daemon's keeper check cadence: well inside the keeper's 10 min idle exit, and
// a crashed keeper is back within it (routes that lost a destination fall back in-process meanwhile).
const keeperKeepAlive = time.Minute

// keeperAlive is the keep-alive seam (senderkeeper.Supervisor in production).
type keeperAlive interface{ KeepAlive() error }

// runKeeperKeepAlive starts rave-mate-keeper at boot and keeps it alive while the receive destinations
// are keeper-hosted (mediaLink.senderKeeper, default on; GPU destinations only). Logs transitions only:
// "up" once, "not available" (missing sidecar, spawn failure, non-Windows) once per outage.
func runKeeperKeepAlive(ctx context.Context, log *logbus.Bus, k keeperAlive, cfg func() config.MediaLinkFeature) {
	t := time.NewTicker(keeperKeepAlive)
	defer t.Stop()
	up, logged := false, false
	for {
		if c := cfg(); c.SenderKeeperEnabled() && c.ZeroCopyDecode() {
			err := k.KeepAlive()
			switch {
			case err == nil && !up:
				log.Info("mediaroute", "sender keeper up - receive destinations survive restarts/updates", map[string]any{"pipe": senderkeeper.PipeName()})
			case err != nil && (up || !logged):
				log.Warn("mediaroute", "sender keeper not available - receive destinations use the in-process sender", map[string]any{"err": err.Error()})
			}
			up, logged = err == nil, true
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
