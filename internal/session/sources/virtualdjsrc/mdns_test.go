package virtualdjsrc

import (
	"encoding/binary"
	"net"
	"testing"
)

// TestIsOS2LQuery: only a DNS query (QR=0) carrying a PTR question for _os2l._tcp.local. matches.
func TestIsOS2LQuery(t *testing.T) {
	ptrQuery := func(flags uint16, service string) []byte {
		b := header(flags, 1, 0)
		b = append(b, encodeName(service)...)
		b = appendU16(b, typePTR)
		b = appendU16(b, classIN)
		return b
	}
	if !isOS2LQuery(ptrQuery(0, os2lService)) {
		t.Error("valid _os2l._tcp PTR query not recognized")
	}
	if isOS2LQuery(ptrQuery(flagResponse, os2lService)) {
		t.Error("a response (QR=1) must not be treated as a query")
	}
	if isOS2LQuery(ptrQuery(0, "_other._tcp.local.")) {
		t.Error("a query for another service must not match")
	}
	if isOS2LQuery([]byte{0, 0, 0}) {
		t.Error("a too-short buffer must not match")
	}
}

// TestReadNamePointerCompression: readName resolves a name that ends in a compression pointer.
func TestReadNamePointerCompression(t *testing.T) {
	b := make([]byte, 12) // header placeholder
	svcOff := len(b)      // 12
	b = append(b, encodeName(os2lService)...)
	compOff := len(b)
	b = append(b, byte(len("rave-mate")))
	b = append(b, "rave-mate"...)
	b = append(b, 0xC0|byte(svcOff>>8), byte(svcOff)) // pointer → svcOff

	name, next, err := readName(b, compOff)
	if err != nil {
		t.Fatalf("readName: %v", err)
	}
	if want := "rave-mate." + os2lService; name != want {
		t.Errorf("name: got %q want %q", name, want)
	}
	if want := compOff + 1 + len("rave-mate") + 2; next != want { // labels + 2-byte pointer
		t.Errorf("next offset: got %d want %d", next, want)
	}
}

// TestRRRoundTrip encodes each RR then decodes its header + rdata back.
func TestRRRoundTrip(t *testing.T) {
	// A
	name, rtype, class, ttl, rd := parseRR(t, rrA("host.local.", net.IPv4(1, 2, 3, 4), announceTTL))
	if name != "host.local." || rtype != typeA || class != classIN || ttl != announceTTL {
		t.Errorf("A header: %q %d %d %d", name, rtype, class, ttl)
	}
	if len(rd) != 4 || rd[0] != 1 || rd[1] != 2 || rd[2] != 3 || rd[3] != 4 {
		t.Errorf("A rdata: %v", rd)
	}

	// PTR
	name, rtype, _, _, rd = parseRR(t, rrPTR(os2lService, "rave-mate."+os2lService, announceTTL))
	if name != os2lService || rtype != typePTR {
		t.Errorf("PTR header: %q %d", name, rtype)
	}
	if target, _, err := readName(rd, 0); err != nil || target != "rave-mate."+os2lService {
		t.Errorf("PTR target: %q err=%v", target, err)
	}

	// SRV
	name, rtype, _, _, rd = parseRR(t, rrSRV("inst."+os2lService, "host.local.", 47641, announceTTL))
	if name != "inst."+os2lService || rtype != typeSRV {
		t.Errorf("SRV header: %q %d", name, rtype)
	}
	if len(rd) < 6 {
		t.Fatalf("SRV rdata too short: %v", rd)
	}
	if port := binary.BigEndian.Uint16(rd[4:]); port != 47641 {
		t.Errorf("SRV port: got %d want 47641", port)
	}
	if target, _, err := readName(rd, 6); err != nil || target != "host.local." {
		t.Errorf("SRV target: %q err=%v", target, err)
	}

	// TXT
	name, rtype, _, _, rd = parseRR(t, rrTXT("inst."+os2lService, []string{"txtvers=1"}, announceTTL))
	if name != "inst."+os2lService || rtype != typeTXT {
		t.Errorf("TXT header: %q %d", name, rtype)
	}
	if len(rd) < 1 || int(rd[0]) != len("txtvers=1") {
		t.Fatalf("TXT rdata length prefix: %v", rd)
	}
	if got := string(rd[1 : 1+rd[0]]); got != "txtvers=1" {
		t.Errorf("TXT string: got %q", got)
	}
}

// parseRR decodes an RR's uncompressed name + fixed header, returning its rdata slice.
func parseRR(t *testing.T, b []byte) (name string, rtype, class uint16, ttl uint32, rdata []byte) {
	t.Helper()
	name, off, err := readName(b, 0)
	if err != nil {
		t.Fatalf("parseRR name: %v", err)
	}
	if off+10 > len(b) {
		t.Fatalf("parseRR truncated header")
	}
	rtype = binary.BigEndian.Uint16(b[off:])
	class = binary.BigEndian.Uint16(b[off+2:])
	ttl = binary.BigEndian.Uint32(b[off+4:])
	rdlen := int(binary.BigEndian.Uint16(b[off+8:]))
	off += 10
	if off+rdlen > len(b) {
		t.Fatalf("parseRR truncated rdata")
	}
	return name, rtype, class, ttl, b[off : off+rdlen]
}
