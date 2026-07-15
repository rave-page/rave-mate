// Package sacn is an E1.31 (streaming ACN / sACN) DMX receiver - an alternate DMX source
// alongside internal/artnet. It joins the per-universe multicast group (239.255.hi.lo) on
// UDP 5568, parses the ACN root + E1.31 framing + DMP layers, and writes the 512 DMX slots
// into the shared universe store via the Sink interface (satisfied by *artnet.Store).
// Pure stdlib net, like artnet.
package sacn

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
)

const source = "sacn"

// Port is the fixed E1.31 UDP port.
const Port = 5568

// ACN packet identifier: "ASC-E1.17\0\0\0".
var acnID = [12]byte{'A', 'S', 'C', '-', 'E', '1', '.', '1', '7', 0, 0, 0}

// E1.31 layer vectors.
const (
	vectorRootData   = 0x00000004 // VECTOR_ROOT_E131_DATA
	vectorFrameData  = 0x00000002 // VECTOR_E131_DATA_PACKET
	vectorDMPSetProp = 0x02       // VECTOR_DMP_SET_PROPERTY
	dmpAddrDataType  = 0xa1       // 1-byte fixed-increment addressing
	headerLen        = 126        // bytes before the DMX start code's channel data
	dmxStartCode     = 0x00       // null start code = standard dimmer data
)

// Sink is the write surface sacn feeds (satisfied by *artnet.Store). Kept as an interface so
// sacn stays decoupled from artnet and testable with a fake store.
type Sink interface {
	// Set applies one universe frame. seq = E1.31 sequence number; srcIP = sender.
	Set(u uint16, seq byte, data []byte, srcIP string, now time.Time) bool
}

// Listener joins one multicast group per configured universe and ingests E1.31 into the store.
type Listener struct {
	log   *logbus.Bus
	store Sink
	unis  []uint16
}

// NewListener builds an sACN listener writing into store for the given universes.
func NewListener(log *logbus.Bus, store Sink, universes []uint16) *Listener {
	return &Listener{log: log, store: store, unis: universes}
}

// Run joins the multicast groups (universes override the constructor list if non-empty) and
// serves until ctx is cancelled. A bind/join failure returns immediately (so the module-start
// probe surfaces it, like artnet). Blocks.
func (l *Listener) Run(ctx context.Context, universes []uint16) error {
	if len(universes) == 0 {
		universes = l.unis
	}
	if len(universes) == 0 {
		universes = []uint16{0}
	}
	var conns []*net.UDPConn
	closeAll := func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}
	for _, u := range universes {
		group := &net.UDPAddr{IP: multicastIP(u), Port: Port}
		conn, err := net.ListenMulticastUDP("udp4", nil, group)
		if err != nil {
			closeAll()
			return fmt.Errorf("sacn join %s: %w", group.IP, err)
		}
		conns = append(conns, conn)
	}
	l.log.Info(source, "sacn listening", map[string]any{"port": Port, "universes": universes})
	go func() { <-ctx.Done(); closeAll() }()

	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		c := conn
		go func() { defer wg.Done(); l.serve(c) }()
	}
	wg.Wait()
	return nil
}

// serve reads datagrams off one group socket until it is closed.
func (l *Listener) serve(conn *net.UDPConn) {
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed on ctx cancel
		}
		u, seq, data, ok := ParseDataPacket(buf[:n])
		if !ok {
			continue // malformed / not an E1.31 data packet
		}
		src := ""
		if from != nil {
			src = from.IP.String()
		}
		l.store.Set(u, seq, data, src, time.Now())
	}
}

// ParseDataPacket validates an E1.31 data packet and extracts universe, sequence and the DMX
// slots (start code stripped). ok=false on any structural mismatch.
func ParseDataPacket(p []byte) (universe uint16, seq byte, data []byte, ok bool) {
	if len(p) < headerLen {
		return 0, 0, nil, false
	}
	if !bytes.Equal(p[4:16], acnID[:]) {
		return 0, 0, nil, false
	}
	if binary.BigEndian.Uint32(p[18:22]) != vectorRootData {
		return 0, 0, nil, false
	}
	if binary.BigEndian.Uint32(p[40:44]) != vectorFrameData {
		return 0, 0, nil, false
	}
	if p[117] != vectorDMPSetProp {
		return 0, 0, nil, false
	}
	if p[118] != dmpAddrDataType {
		return 0, 0, nil, false
	}
	if p[125] != dmxStartCode {
		return 0, 0, nil, false // non-null start code (RDM/text) - not dimmer data
	}
	universe = binary.BigEndian.Uint16(p[113:115])
	seq = p[111]
	// property value count includes the 1-byte start code.
	cnt := int(binary.BigEndian.Uint16(p[123:125]))
	slots := cnt - 1
	if slots < 0 {
		slots = 0
	}
	if slots > 512 {
		slots = 512
	}
	end := headerLen + slots
	if end > len(p) {
		end = len(p)
	}
	return universe, seq, p[headerLen:end], true
}

// BuildDataPacket builds a spec-correct E1.31 data packet (CID + source name left zero -
// fine for a loopback source / tests). data is truncated to 512 slots.
func BuildDataPacket(universe uint16, seq byte, data []byte) []byte {
	slots := len(data)
	if slots > 512 {
		slots = 512
	}
	total := headerLen + slots
	p := make([]byte, total)
	// Root layer.
	binary.BigEndian.PutUint16(p[0:2], 0x0010) // preamble size
	copy(p[4:16], acnID[:])
	binary.BigEndian.PutUint16(p[16:18], 0x7000|uint16(total-16)) // flags+length
	binary.BigEndian.PutUint32(p[18:22], vectorRootData)
	// Framing layer.
	binary.BigEndian.PutUint16(p[38:40], 0x7000|uint16(total-38))
	binary.BigEndian.PutUint32(p[40:44], vectorFrameData)
	p[108] = 100 // priority (default)
	p[111] = seq
	binary.BigEndian.PutUint16(p[113:115], universe)
	// DMP layer.
	binary.BigEndian.PutUint16(p[115:117], 0x7000|uint16(total-115))
	p[117] = vectorDMPSetProp
	p[118] = dmpAddrDataType
	binary.BigEndian.PutUint16(p[121:123], 0x0001)          // address increment
	binary.BigEndian.PutUint16(p[123:125], uint16(slots+1)) // count incl start code
	p[125] = dmxStartCode
	copy(p[headerLen:], data[:slots])
	return p
}

// multicastIP returns the E1.31 multicast group for a universe: 239.255.<hi>.<lo>.
func multicastIP(u uint16) net.IP {
	return net.IPv4(239, 255, byte(u>>8), byte(u&0xff))
}
