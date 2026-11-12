//go:build !linux

package medialink

import "net"

// interfaceLinkMbps on non-Linux returns 0 (unknown) today: the congestion controller's proven rate
// bounds the route. Windows adapter link speed via GetAdaptersAddresses (iphlpapi) is a documented
// follow-up; see .devnotes/ADAPTIVE_BUDGET.md.
func interfaceLinkMbps(net.IP) int { return 0 }
