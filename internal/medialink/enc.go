package medialink

import "net"

// enc.go holds the media-plane encryption-mode resolution helpers (brief req 5): the per-peer
// preference source (EncAuto/EncOn/EncOff) and the "is this peer on a directly-connected private
// subnet?" test that gates the `auto` default's LAN-cleartext decision. resolveEnc (negotiate.go)
// combines a preference pair with the ANDed Local flags into the final AEAD decision for a route.

// mediaPref reports this node's media-plane encryption preference for a peer. Preference source, in
// order: an explicit 3-state MediaEncMode(peer) on the secret provider; else the legacy boolean
// MediaEncOff(peer) (true → EncOff, migrated opt-out); else the new default EncAuto. A test provider
// with neither method defaults to EncAuto (LAN-cleartext-capable, AEAD off-subnet).
func (rm *RouteManager) mediaPref(peer string) string {
	if mp, ok := rm.secrets.(interface{ MediaEncMode(string) string }); ok {
		switch m := mp.MediaEncMode(peer); m {
		case EncOn, EncOff, EncAuto:
			return m
		}
	}
	if ep, ok := rm.secrets.(interface{ MediaEncOff(string) bool }); ok && ep.MediaEncOff(peer) {
		return EncOff // legacy opt-out migrates to the explicit off state
	}
	return EncAuto
}

// sameSubnet reports whether peer's link address (from the SecretProvider's optional PeerAddr) is an
// IP on one of THIS host's directly-connected private subnets (same interface prefix, RFC1918 /
// link-local / ULA). This is the `auto` enc input: cleartext is only ever used when both ends
// confirm it. A peer whose address is unknown, public, or on a different subnet returns false (→
// AEAD), failing safe toward encryption.
func (rm *RouteManager) sameSubnet(peer string) bool {
	ap, ok := rm.secrets.(interface {
		PeerAddr(string) (string, bool)
	})
	if !ok {
		return false
	}
	paddr, ok := ap.PeerAddr(peer)
	if !ok {
		return false
	}
	host, _, err := net.SplitHostPort(paddr)
	if err != nil {
		host = paddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !isPrivateIP(ip) {
		return false
	}
	return ipOnLocalSubnet(ip)
}

// isPrivateIP reports whether ip is RFC1918 (10/8, 172.16/12, 192.168/16), IPv4 link-local
// (169.254/16), IPv6 link-local (fe80::/10) or IPv6 unique-local (fc00::/7) - the address classes
// the `auto` rule treats as a trusted directly-connected LAN. Public + CGNAT (100.64/10, the
// Tailscale range) are NOT trusted here → AEAD.
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 10:
			return true
		case v4[0] == 172 && v4[1]&0xf0 == 16:
			return true
		case v4[0] == 192 && v4[1] == 168:
			return true
		}
		return false
	}
	return len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc // fc00::/7 unique-local
}

// ipOnLocalSubnet reports whether ip falls inside one of this host's interface prefixes (i.e. it is
// reachable without a router hop - a genuinely directly-connected subnet, not merely private).
func ipOnLocalSubnet(ip net.IP) bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.Contains(ip) {
				return true
			}
		}
	}
	return false
}
