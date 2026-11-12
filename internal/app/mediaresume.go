package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/webcam"
)

// rememberingReceives wraps the video-receive control so the set of remote sources the user
// activated is PERSISTED (config.RememberedReceives) and AUTO-REACTIVATED when the source PC
// advertises them again - across restarts, reconnects and the source going offline and back.
//
// Intent capture: an explicit StartReceive remembers (peer, sourceID); an explicit StopReceive
// forgets it. A source merely going offline does NOT forget - that is exactly the case we want to
// resume. Reconcile runs on a ticker in the daemon, uses the ReceiveControl surface (so it works
// identically for the in-proc manager and the media-child proxy), and calls the INNER StartReceive
// (already remembered - no re-persist). The daemon owns cfg.Save().
type rememberingReceives struct {
	mediaroute.ReceiveControl
	log     *logbus.Bus
	cfg     *config.Config
	save    func()
	mu      sync.Mutex               // guards RememberedReceives writes + Save
	backoff map[string]*reoffBackoff // per (peer,source) re-offer backoff; reconcile-goroutine only (no lock)
}

// re-offer backoff: a died route is retried fast (immediate, then 1s, 2s, 4s … capped), so a stage
// blip recovers in ~1s, but a source that stays gone/unreachable is not hammered. The backoff resets
// once a route has stayed up for reoffResetStable (so flapping keeps backing off, a stable route does
// not inherit old penalty).
const (
	reoffBase        = 1 * time.Second
	reoffCap         = 30 * time.Second
	reoffResetStable = 5 * time.Second
)

type reoffBackoff struct {
	next      time.Time     // earliest next re-offer attempt
	interval  time.Duration // current backoff step
	liveSince time.Time     // when the current live route for this key was first seen (zero = not live)
}

// SetTransportOverride forwards to the wrapped control when it supports per-route overrides.
func (r *rememberingReceives) SetTransportOverride(peer, sourceID, mode string) error {
	if t, ok := r.ReceiveControl.(mediaroute.TransportOverrider); ok {
		return t.SetTransportOverride(peer, sourceID, mode)
	}
	return errors.New("per-route transport override unsupported by this media plane")
}

func (r *rememberingReceives) StartReceive(peer, sourceID string) (string, error) {
	sess, err := r.ReceiveControl.StartReceive(peer, sourceID)
	if err != nil {
		return sess, err
	}
	r.mu.Lock()
	changed := r.cfg.Features.MediaLink.RememberReceive(peer, sourceID, r.sourceName(peer, sourceID))
	r.mu.Unlock()
	if changed {
		r.save()
		r.log.Info("mediaroute", "remembered remote source - auto-reactivates when it reappears",
			map[string]any{"peer": peer, "source": sourceID})
	}
	return sess, nil
}

func (r *rememberingReceives) StopReceive(session string) {
	// Resolve session -> (peer, sourceID) BEFORE the route is gone, so an explicit stop forgets it.
	var peer, sid string
	for _, rc := range r.ReceiveControl.Receives() {
		if rc.Session == session {
			peer, sid = rc.Peer, rc.SourceID
			break
		}
	}
	r.ReceiveControl.StopReceive(session)
	if peer == "" {
		return
	}
	r.mu.Lock()
	changed := r.cfg.Features.MediaLink.ForgetReceive(peer, sid)
	r.mu.Unlock()
	if changed {
		r.save()
		r.log.Info("mediaroute", "forgot remote source (explicit stop) - no longer auto-reactivated",
			map[string]any{"peer": peer, "source": sid})
	}
}

// sourceName resolves a display name for a remembered source (best-effort; "" if not advertised).
func (r *rememberingReceives) sourceName(peer, sourceID string) string {
	for _, s := range r.ReceiveControl.RemoteVideoSources() {
		if s.Peer == peer && s.Desc.ID == sourceID {
			return s.Desc.Name
		}
	}
	return ""
}

// reconcile re-offers every remembered source that is advertised but whose route is NOT live now -
// immediately on the first miss, then with bounded exponential backoff so a gone/unreachable source is
// not hammered. A LIVE route (Active) resets the backoff once it has been up for reoffResetStable.
func (r *rememberingReceives) reconcile() {
	r.mu.Lock()
	wanted := append([]config.RememberedReceive(nil), r.cfg.Features.MediaLink.RememberedReceives...)
	r.mu.Unlock()
	if len(wanted) == 0 {
		return
	}
	now := time.Now()
	active := map[string]bool{}
	for _, rc := range r.ReceiveControl.Receives() {
		// Occupied = LIVE or still establishing (offer in-flight). A died-but-not-GC'd receive is NOT
		// occupied (fast re-offer), but an in-flight offer IS - so a start-up burst of triggers does not
		// stack a SECOND offer for a source whose first is still coming up (that killed the first).
		if rc.Occupied(now) {
			active[rc.Peer+"\x00"+rc.SourceID] = true
		}
	}
	avail := map[string]bool{}
	for _, s := range r.ReceiveControl.RemoteVideoSources() {
		avail[s.Peer+"\x00"+s.Desc.ID] = true
	}
	for _, w := range wanted {
		key := w.Peer + "\x00" + w.SourceID
		b := r.backoffFor(key)
		if active[key] {
			r.noteLive(b, now) // live: arm the stable-reset timer; reset backoff once stable
			continue
		}
		b.liveSince = time.Time{} // not live: restart the stability clock for the next up period
		if !avail[key] {
			continue // source PC has not advertised it (yet): wait, do not burn a backoff step
		}
		if now.Before(b.next) {
			continue // backing off from a recent attempt
		}
		if _, err := r.ReceiveControl.StartReceive(w.Peer, w.SourceID); err != nil {
			r.armBackoff(b, now)
			r.log.Debug("mediaroute", "auto-reactivate deferred", map[string]any{
				"peer": w.Peer, "source": w.SourceID, "err": err.Error()})
			continue
		}
		r.armBackoff(b, now) // provisional: don't re-offer again until it has had a chance to come up
		r.log.Info("mediaroute", "auto-reactivated remembered remote source", map[string]any{
			"peer": w.Peer, "source": w.SourceID, "name": w.Name})
	}
}

// backoffFor returns the (reconcile-goroutine-only) backoff record for a key, creating it at the base.
func (r *rememberingReceives) backoffFor(key string) *reoffBackoff {
	if r.backoff == nil {
		r.backoff = map[string]*reoffBackoff{}
	}
	b := r.backoff[key]
	if b == nil {
		b = &reoffBackoff{interval: reoffBase}
		r.backoff[key] = b
	}
	return b
}

// armBackoff schedules the next attempt and doubles the step (capped).
func (r *rememberingReceives) armBackoff(b *reoffBackoff, now time.Time) {
	if b.interval <= 0 {
		b.interval = reoffBase
	}
	b.next = now.Add(b.interval)
	if b.interval *= 2; b.interval > reoffCap {
		b.interval = reoffCap
	}
}

// noteLive resets the backoff once a live route has been stable for reoffResetStable.
func (r *rememberingReceives) noteLive(b *reoffBackoff, now time.Time) {
	if b.liveSince.IsZero() {
		b.liveSince = now
	}
	if now.Sub(b.liveSince) >= reoffResetStable {
		b.interval = reoffBase
		b.next = time.Time{}
	}
}

// runReceiveResume drives reconcile on a 1s ticker (fast recovery; backoff bounds the retry rate).
func runReceiveResume(ctx context.Context, r *rememberingReceives) {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	r.reconcile() // resume immediately at startup (sources already advertised)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reconcile()
		}
	}
}

// runWebcamSettingsPersist snapshots the LOCAL camera's live UVC props on a ticker and persists any
// change to config, so zoom/exposure/etc. are remembered PER DEVICE and re-applied on the next open
// (webcam.Manager.restoreProps). Catches every set - local UI and remote peer commands alike - by
// reading the published status rather than intercepting the command path. Auto props store value 0
// (the device drives the value in auto), so a drifting auto value never churns the config. Runs in
// the daemon, which owns cfg.Save().
func runWebcamSettingsPersist(ctx context.Context, cam webcam.CamControl, cfg *config.Config, save func(), log *logbus.Bus) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, inst := range cam.Instances() {
			if !inst.Local || inst.Device == "" || len(inst.Props) == 0 {
				continue
			}
			changed := false
			for _, p := range inst.Props {
				val := p.Value
				if p.Auto {
					val = 0
				}
				if cfg.Features.Webcam.RememberProp(inst.Device, p.ID, val, p.Auto) {
					changed = true
				}
			}
			if changed {
				save()
				log.Info("webcam", "remembered UVC settings for device", map[string]any{"device": inst.Device})
			}
		}
	}
}
