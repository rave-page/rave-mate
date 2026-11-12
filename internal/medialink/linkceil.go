package medialink

import "net"

// linkceil.go - the STATIC link-speed ceiling (adaptive budget item 4): never assume more than a
// configurable fraction of the interface link speed when it is knowable; otherwise fall back to the
// congestion controller's proven rate (0 here = "no static ceiling", so min() never binds on it).
// The congestion controller still probes/backs off dynamically below this.

// DefaultLinkFracPct is the default fraction of a KNOWN interface link speed used as the static
// ceiling. 50% for a wired >=1 Gb LAN leaves ample room for other traffic + the controller's probing;
// the quality model (<=80 Mbps) is far below a 1 Gb link, so this binds only on genuinely slow paths.
const DefaultLinkFracPct = 50

// LinkCeilKbps = linkMbps * fracPct%, in kbps. 0 when the speed is unknown (linkMbps<=0) => no static
// ceiling (the congestion controller's proven rate bounds the route instead).
func LinkCeilKbps(linkMbps, fracPct int) int {
	if linkMbps <= 0 {
		return 0
	}
	if fracPct <= 0 {
		fracPct = DefaultLinkFracPct
	}
	if fracPct > 100 {
		fracPct = 100
	}
	return linkMbps * 1000 * fracPct / 100
}

// localLinkCeilKbps resolves the local interface link speed toward the peer (best effort) and applies
// the fraction. 0 when the local IP / speed is unknown (the common Windows case today => congestion
// controller bounds it). On a TCP route (no per-datagram stats) this static ceiling + the quality
// model are the only bounds; UDP additionally has the congestion controller. peerAddr = "host:port".
func (rm *RouteManager) localLinkCeilKbps(peerAddr string, fracPct int) int {
	if peerAddr == "" {
		return 0
	}
	ip := net.ParseIP(hostToward(peerAddr)) // the NIC the OS routes to the peer through
	if ip == nil {
		return 0
	}
	return LinkCeilKbps(interfaceLinkMbps(ip), fracPct)
}
