//go:build linux

package medialink

import (
	"net"
	"os"
	"strconv"
	"strings"
)

// interfaceLinkMbps returns the link speed (Mbps) of the interface holding ip, from
// /sys/class/net/<if>/speed. 0 when unknown (virtual/loopback/down interfaces report -1 or error).
func interfaceLinkMbps(ip net.IP) int {
	if ip == nil {
		return 0
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var aip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				aip = v.IP
			case *net.IPAddr:
				aip = v.IP
			}
			if aip == nil || !aip.Equal(ip) {
				continue
			}
			b, err := os.ReadFile("/sys/class/net/" + iface.Name + "/speed")
			if err != nil {
				return 0
			}
			n, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || n <= 0 { // -1 = unknown (e.g. no carrier / virtual)
				return 0
			}
			return n
		}
	}
	return 0
}
