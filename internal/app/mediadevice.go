package app

import (
	"rave.page/mate/internal/config"
)

// mediadevice.go - WP-3 wiring: the medialink send path's SENDER-side codec preference. The encode
// DEVICE resolver moved to featurehost.NewEncodeDeviceResolver (shared by daemon + media child, now
// throughput-aware plan-driven for AUTO).

// mediaEncodePolicy builds the medialink Options.EncodePolicy callback: the SENDER-side codec
// preference (MediaLink.PreferCodec mirrored onto the send side, so pinning h264 on the sharing PC
// steers negotiation too) plus the hard encoder pin (MediaLink.Encoder).
func mediaEncodePolicy(cfg func() config.MediaLinkFeature) func() (string, string) {
	return func() (string, string) {
		f := cfg()
		return f.PreferCodec, f.PinnedEncoder()
	}
}
