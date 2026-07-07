package discovery

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Minimal DNS-over-multicast (mDNS / RFC 1035 + 6762/6763) wire codec - only the subset
// rave-mate needs: a PTR browse query, and announce/goodbye responses carrying PTR + SRV +
// TXT + A. We encode names uncompressed (legal, simpler) and follow compression pointers
// on parse (other responders compress). Not a general mDNS stack.

const (
	typeA   = 1
	typePTR = 12
	typeTXT = 16
	typeSRV = 33

	classIN       = 1
	flagResponse  = 0x8400 // QR=1, AA=1
	announceTTL   = 120
	goodbyeTTL    = 0
	maxMessageLen = 9000 // mDNS permits large messages; cap parsing work
)

var errMalformed = errors.New("discovery: malformed DNS message")

// rr is a decoded resource record (only the fields we consume per type).
type rr struct {
	name  string
	rtype uint16
	ttl   uint32
	// type-specific:
	ptr     string   // PTR target
	txt     []string // TXT strings
	srvPort uint16   // SRV port
	srvTgt  string   // SRV target host
	a       net.IP   // A address
}

// message is a parsed DNS message (questions + all records flattened).
type message struct {
	response  bool
	questions []question
	records   []rr // answers + authority + additional
}

type question struct {
	name  string
	qtype uint16
}

// ── encode ───────────────────────────────────────────────────────────────────

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

func putHeader(buf []byte, flags uint16, qd, an uint16) {
	binary.BigEndian.PutUint16(buf[0:], 0) // ID 0 for mDNS
	binary.BigEndian.PutUint16(buf[2:], flags)
	binary.BigEndian.PutUint16(buf[4:], qd)
	binary.BigEndian.PutUint16(buf[6:], an)
	binary.BigEndian.PutUint16(buf[8:], 0)
	binary.BigEndian.PutUint16(buf[10:], 0)
}

// encodeQuery builds a PTR browse query for the service name.
func encodeQuery(service string) []byte {
	buf := make([]byte, 12)
	putHeader(buf, 0, 1, 0) // QR=0
	buf = append(buf, encodeName(service)...)
	buf = appendU16(buf, typePTR)
	buf = appendU16(buf, classIN)
	return buf
}

// encodeAnnounce builds a response advertising the service: PTR + SRV + TXT + A (one A per
// address). ttl=0 (encodeGoodbye) signals withdrawal.
func encodeAnnounce(svc Service, addrs []net.IP, ttl uint32) []byte {
	service := svc.serviceName()
	instance := svc.instanceName()
	host := svc.hostName()

	var ans []byte
	count := 0
	add := func(b []byte) { ans = append(ans, b...); count++ }

	add(rrPTR(service, instance, ttl))
	add(rrSRV(instance, host, svc.Port, ttl))
	add(rrTXT(instance, svc.txtStrings(), ttl))
	for _, ip := range addrs {
		if v4 := ip.To4(); v4 != nil {
			add(rrA(host, v4, ttl))
		}
	}

	buf := make([]byte, 12)
	putHeader(buf, flagResponse, 0, uint16(count))
	return append(buf, ans...)
}

func encodeGoodbye(svc Service) []byte {
	// A PTR with TTL 0 tells browsers to drop the instance immediately.
	buf := make([]byte, 12)
	putHeader(buf, flagResponse, 0, 1)
	return append(buf, rrPTR(svc.serviceName(), svc.instanceName(), goodbyeTTL)...)
}

func rrHeader(name string, rtype uint16, ttl uint32, rdlen int) []byte {
	b := encodeName(name)
	b = appendU16(b, rtype)
	b = appendU16(b, classIN)
	b = appendU32(b, ttl)
	b = appendU16(b, uint16(rdlen))
	return b
}

func rrPTR(name, target string, ttl uint32) []byte {
	rd := encodeName(target)
	return append(rrHeader(name, typePTR, ttl, len(rd)), rd...)
}

func rrSRV(name, target string, port int, ttl uint32) []byte {
	rd := make([]byte, 6)
	binary.BigEndian.PutUint16(rd[0:], 0) // priority
	binary.BigEndian.PutUint16(rd[2:], 0) // weight
	binary.BigEndian.PutUint16(rd[4:], uint16(port))
	rd = append(rd, encodeName(target)...)
	return append(rrHeader(name, typeSRV, ttl, len(rd)), rd...)
}

func rrTXT(name string, strs []string, ttl uint32) []byte {
	var rd []byte
	for _, s := range strs {
		if len(s) > 255 {
			s = s[:255]
		}
		rd = append(rd, byte(len(s)))
		rd = append(rd, s...)
	}
	if len(rd) == 0 {
		rd = []byte{0} // empty TXT = a single zero-length string
	}
	return append(rrHeader(name, typeTXT, ttl, len(rd)), rd...)
}

func rrA(name string, ip net.IP, ttl uint32) []byte {
	rd := append([]byte(nil), ip.To4()...)
	return append(rrHeader(name, typeA, ttl, len(rd)), rd...)
}

func appendU16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }
func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// ── parse ────────────────────────────────────────────────────────────────────

func parseMessage(b []byte) (message, error) {
	if len(b) < 12 || len(b) > maxMessageLen {
		return message{}, errMalformed
	}
	flags := binary.BigEndian.Uint16(b[2:])
	qd := binary.BigEndian.Uint16(b[4:])
	an := binary.BigEndian.Uint16(b[6:])
	ns := binary.BigEndian.Uint16(b[8:])
	ar := binary.BigEndian.Uint16(b[10:])

	msg := message{response: flags&0x8000 != 0}
	off := 12
	var err error
	for i := 0; i < int(qd); i++ {
		var q question
		if q, off, err = parseQuestion(b, off); err != nil {
			return message{}, err
		}
		msg.questions = append(msg.questions, q)
	}
	total := int(an) + int(ns) + int(ar)
	for i := 0; i < total; i++ {
		var r rr
		if r, off, err = parseRR(b, off); err != nil {
			return message{}, err
		}
		msg.records = append(msg.records, r)
	}
	return msg, nil
}

func parseQuestion(b []byte, off int) (question, int, error) {
	name, next, err := readName(b, off)
	if err != nil {
		return question{}, 0, err
	}
	if next+4 > len(b) {
		return question{}, 0, errMalformed
	}
	q := question{name: name, qtype: binary.BigEndian.Uint16(b[next:])}
	return q, next + 4, nil // skip qtype(2) + qclass(2)
}

func parseRR(b []byte, off int) (rr, int, error) {
	name, next, err := readName(b, off)
	if err != nil {
		return rr{}, 0, err
	}
	if next+10 > len(b) {
		return rr{}, 0, errMalformed
	}
	r := rr{name: name, rtype: binary.BigEndian.Uint16(b[next:])}
	r.ttl = binary.BigEndian.Uint32(b[next+4:])
	rdlen := int(binary.BigEndian.Uint16(b[next+8:]))
	rdStart := next + 10
	if rdStart+rdlen > len(b) {
		return rr{}, 0, errMalformed
	}
	rd := b[rdStart : rdStart+rdlen]
	switch r.rtype {
	case typePTR:
		if r.ptr, _, err = readName(b, rdStart); err != nil {
			return rr{}, 0, err
		}
	case typeSRV:
		if rdlen < 6 {
			return rr{}, 0, errMalformed
		}
		r.srvPort = binary.BigEndian.Uint16(rd[4:])
		if r.srvTgt, _, err = readName(b, rdStart+6); err != nil {
			return rr{}, 0, err
		}
	case typeTXT:
		r.txt = parseTXT(rd)
	case typeA:
		if rdlen == 4 {
			r.a = net.IPv4(rd[0], rd[1], rd[2], rd[3])
		}
	}
	return r, rdStart + rdlen, nil
}

func parseTXT(rd []byte) []string {
	var out []string
	for i := 0; i < len(rd); {
		n := int(rd[i])
		i++
		if i+n > len(rd) {
			break
		}
		if n > 0 {
			out = append(out, string(rd[i:i+n]))
		}
		i += n
	}
	return out
}

// readName decodes a (possibly compressed) DNS name starting at off, returning the dotted
// name and the offset just past the name in the *record stream* (compression pointers don't
// advance that). Guards against pointer loops.
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
			// Canonical trailing-dot form so names compare equal to our constants.
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
				return "", 0, fmt.Errorf("%w: pointer loop", errMalformed)
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
