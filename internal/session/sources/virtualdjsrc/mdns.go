package virtualdjsrc

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

// Minimal, self-contained mDNS/DNS-SD responder for _os2l._tcp so VirtualDJ (the OS2L
// client) auto-discovers our server. We can't reuse internal/discovery: its codec is
// unexported and hard-wired to its own Service / _ravemate._tcp type with no generic
// "advertise type T on port P" entry point, and that package is read-only here. So this is a
// small purpose-built responder - pure stdlib (net + encoding/binary), no x/net/ipv4.
//
// Loopback note: net.ListenMulticastUDP disables IP_MULTICAST_LOOP, which would stop a
// same-host VirtualDJ from hearing our announces. So we RECEIVE queries on that multicast
// socket but SEND from a separate plain net.ListenUDP socket, whose IP_MULTICAST_LOOP is
// enabled by OS default → same-host discovery works.

const (
	typeA   = 1
	typePTR = 12
	typeTXT = 16
	typeSRV = 33

	classIN      = 1
	flagResponse = 0x8400 // QR=1, AA=1
	announceTTL  = 120

	os2lService   = "_os2l._tcp.local."
	os2lInstance  = "rave-mate"
	mdnsGroup     = "224.0.0.251"
	mdnsPort      = 5353
	announceEvery = 10 * time.Second
)

var errMalformed = errors.New("mdns: malformed message")

// advertiseOS2L answers PTR browse queries and periodically announces the OS2L service until
// ctx is cancelled. Degrades gracefully: if the multicast recv socket can't bind it still
// sends unsolicited announces (which VirtualDJ picks up on its next browse cycle).
func advertiseOS2L(ctx context.Context, log *logbus.Bus, port int) {
	group := &net.UDPAddr{IP: net.ParseIP(mdnsGroup), Port: mdnsPort}

	recv, rerr := net.ListenMulticastUDP("udp4", nil, group)
	if rerr != nil {
		log.Info(os2lTag, "mDNS recv socket unavailable; unsolicited announces only", map[string]any{"error": rerr.Error()})
	}
	send, serr := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if serr != nil {
		log.Warn(os2lTag, "mDNS send socket failed; OS2L needs manual config", map[string]any{"error": serr.Error()})
		if recv != nil {
			_ = recv.Close()
		}
		return
	}
	defer func() { _ = send.Close() }()

	instance := os2lInstance + "." + os2lService
	host := os2lInstance + "-os2l.local."
	announce := encodeAnnounce(instance, host, port, localIPv4s(), announceTTL)

	if recv != nil {
		debuglog.Go(log, os2lTag, func() { answerLoop(recv, send, group, announce) })
	}
	log.Info(os2lTag, "advertising OS2L via mDNS", map[string]any{"service": os2lService, "port": port})

	sendTo(send, group, announce)
	t := time.NewTicker(announceEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			sendTo(send, group, encodeGoodbye(instance))
			if recv != nil {
				_ = recv.Close()
			}
			return
		case <-t.C:
			sendTo(send, group, announce)
		}
	}
}

// answerLoop replies to _os2l._tcp PTR browse queries with a full announce. Returns when the
// recv socket is closed (ctx cancel).
func answerLoop(recv, send *net.UDPConn, group *net.UDPAddr, announce []byte) {
	buf := make([]byte, 4096)
	for {
		n, _, err := recv.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if isOS2LQuery(buf[:n]) {
			sendTo(send, group, announce)
		}
	}
}

func sendTo(send *net.UDPConn, group *net.UDPAddr, b []byte) { _, _ = send.WriteToUDP(b, group) }

// ── encode ───────────────────────────────────────────────────────────────────

// encodeAnnounce builds a response: PTR + SRV + TXT + one A per address. ttl 0 = goodbye.
func encodeAnnounce(instance, host string, port int, ips []net.IP, ttl uint32) []byte {
	var ans []byte
	count := 0
	add := func(b []byte) { ans = append(ans, b...); count++ }
	add(rrPTR(os2lService, instance, ttl))
	add(rrSRV(instance, host, port, ttl))
	add(rrTXT(instance, []string{"txtvers=1"}, ttl))
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			add(rrA(host, v4, ttl))
		}
	}
	return append(header(flagResponse, 0, uint16(count)), ans...)
}

func encodeGoodbye(instance string) []byte {
	return append(header(flagResponse, 0, 1), rrPTR(os2lService, instance, 0)...)
}

func header(flags, qd, an uint16) []byte {
	buf := make([]byte, 12)
	binary.BigEndian.PutUint16(buf[2:], flags)
	binary.BigEndian.PutUint16(buf[4:], qd)
	binary.BigEndian.PutUint16(buf[6:], an)
	return buf
}

func encodeName(name string) []byte {
	var out []byte
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if label == "" {
			continue
		}
		if len(label) > 63 {
			label = label[:63]
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0)
}

func rrHeader(name string, rtype uint16, ttl uint32, rdlen int) []byte {
	b := encodeName(name)
	b = appendU16(b, rtype)
	b = appendU16(b, classIN)
	b = appendU32(b, ttl)
	return appendU16(b, uint16(rdlen))
}

func rrPTR(name, target string, ttl uint32) []byte {
	rd := encodeName(target)
	return append(rrHeader(name, typePTR, ttl, len(rd)), rd...)
}

func rrSRV(name, target string, port int, ttl uint32) []byte {
	rd := make([]byte, 6) // priority(0) weight(0) port
	binary.BigEndian.PutUint16(rd[4:], uint16(port))
	rd = append(rd, encodeName(target)...)
	return append(rrHeader(name, typeSRV, ttl, len(rd)), rd...)
}

func rrTXT(name string, strs []string, ttl uint32) []byte {
	var rd []byte
	for _, sv := range strs {
		if len(sv) > 255 {
			sv = sv[:255]
		}
		rd = append(rd, byte(len(sv)))
		rd = append(rd, sv...)
	}
	if len(rd) == 0 {
		rd = []byte{0}
	}
	return append(rrHeader(name, typeTXT, ttl, len(rd)), rd...)
}

func rrA(name string, v4 net.IP, ttl uint32) []byte {
	rd := append([]byte(nil), v4.To4()...)
	return append(rrHeader(name, typeA, ttl, len(rd)), rd...)
}

func appendU16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }
func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// ── parse (query detection only) ───────────────────────────────────────────────

// isOS2LQuery reports whether b is a DNS query carrying a PTR question for _os2l._tcp.local.
func isOS2LQuery(b []byte) bool {
	if len(b) < 12 {
		return false
	}
	if binary.BigEndian.Uint16(b[2:])&0x8000 != 0 {
		return false // a response, not a query
	}
	qd := int(binary.BigEndian.Uint16(b[4:]))
	off := 12
	for i := 0; i < qd; i++ {
		name, next, err := readName(b, off)
		if err != nil || next+4 > len(b) {
			return false
		}
		if binary.BigEndian.Uint16(b[next:]) == typePTR && strings.EqualFold(name, os2lService) {
			return true
		}
		off = next + 4
	}
	return false
}

// readName decodes a (possibly compressed) DNS name, returning the dotted name and the
// offset just past it in the record stream. Guards pointer loops.
func readName(b []byte, off int) (string, int, error) {
	var labels []string
	next := -1
	jumps := 0
	for {
		if off >= len(b) {
			return "", 0, errMalformed
		}
		n := int(b[off])
		switch {
		case n == 0:
			off++
			if next < 0 {
				next = off
			}
			return strings.Join(labels, ".") + ".", next, nil
		case n&0xC0 == 0xC0: // compression pointer
			if off+1 >= len(b) {
				return "", 0, errMalformed
			}
			ptr := int(binary.BigEndian.Uint16(b[off:]) & 0x3FFF)
			if next < 0 {
				next = off + 2
			}
			if jumps++; jumps > 64 {
				return "", 0, errMalformed
			}
			off = ptr
		default:
			if off+1+n > len(b) {
				return "", 0, errMalformed
			}
			labels = append(labels, string(b[off+1:off+1+n]))
			off += 1 + n
		}
	}
}

// localIPv4s returns non-loopback IPv4 addresses (127.0.0.1 fallback if none) for the A records.
func localIPv4s() []net.IP {
	var out []net.IP
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if v4 := ipn.IP.To4(); v4 != nil && !v4.IsLoopback() {
					out = append(out, v4)
				}
			}
		}
	}
	if len(out) == 0 {
		out = append(out, net.IPv4(127, 0, 0, 1))
	}
	return out
}
