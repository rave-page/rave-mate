package app

import (
	"sync"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/logbus"
)

// mediadevice.go - WP-3 wiring: the medialink send path asks "which GPU do I encode on?" at every
// route open and gets the user's preference back (config policy → encoderscan resolution). Kept out
// of app.go so the resolver + its logging live in one place.

// mediaEncodeDevice builds the medialink Options.EncodeDevice callback. Returns ("", -1) for the
// automatic policy, i.e. NO device flags anywhere - identical to the pre-WP-3 behaviour.
//
// PolicyAvoid's protection set comes from encoderscan.Detect with a CONFIG-ONLY OBS input
// (OBSConfigEncoder: no obs-websocket round trip, so there is no startup-order dependency and no
// shared state to race). That still protects a LIVE OBS: Scan marks the OBS process critical when
// its own video-encode utilization crosses the threshold, which is exactly the case that matters.
func mediaEncodeDevice(log *logbus.Bus, cfg func() config.MediaLinkFeature) func() (string, int) {
	sel := encoderscan.NewDeviceSelector(
		func() (string, string) { return cfg().DevicePref() },
		func() (stream, record string, active bool, err error) {
			s, r, ok := encoderscan.OBSConfigEncoder()
			if !ok {
				return "", "", false, nil
			}
			return s, r, false, nil
		})
	var mu sync.Mutex
	var lastLUID string
	var logged bool
	return func() (string, int) {
		d := sel()
		mu.Lock()
		changed := !logged || d.LUID != lastLUID
		lastLUID, logged = d.LUID, true
		mu.Unlock()
		if changed && log != nil {
			log.Info("medialink", "encode device selected", map[string]any{
				"adapter": d.LUID, "index": d.Index, "name": d.Name, "why": d.Reason})
		}
		return d.LUID, d.Index
	}
}

// mediaEncodePolicy builds the medialink Options.EncodePolicy callback: the SENDER-side codec
// preference (MediaLink.PreferCodec mirrored onto the send side, so pinning h264 on the sharing PC
// steers negotiation too) plus the hard encoder pin (MediaLink.Encoder).
func mediaEncodePolicy(cfg func() config.MediaLinkFeature) func() (string, string) {
	return func() (string, string) {
		f := cfg()
		return f.PreferCodec, f.PinnedEncoder()
	}
}
