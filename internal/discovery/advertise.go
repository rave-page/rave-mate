package discovery

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

// Advertiser is an announce-only mDNS/DNS-SD responder for a single arbitrary DNS-SD
// service (e.g. Serato's `_SeratoIOSRemote._tcp`). Unlike Discovery it does not browse or
// track peers - it just publishes PTR+SRV+TXT+A for one service and answers PTR browse
// queries for that type, so a third-party browser (Serato DJ Pro) can find and connect to
// us. Reuses the wire codec + rr* helpers in wire.go and the reuse-addr socket control so
// it can share the 5353 multicast port with the peer Discovery responder on the same host.
type Advertiser struct {
	service  string   // e.g. "_SeratoIOSRemote._tcp.local." (dotted = label separators)
	instance string   // full instance name "<label>._SeratoIOSRemote._tcp.local."
	host     string   // SRV target host "<label>.local."
	port     int      // advertised TCP port
	txt      []string // TXT strings; empty => a single zero-length string
	log      *logbus.Bus
	tag      string

	mu     sync.Mutex
	conn   *net.UDPConn
	p      *ipv4.PacketConn
	ifaces []net.Interface
}

// NewAdvertiser builds an announce-only responder. serviceType is a fully-qualified DNS-SD
// type ending in ".local." (dots are label separators). instanceLabel and hostLabel are
// single DNS labels; any dots in them are stripped so they never mis-split on the wire.
func NewAdvertiser(serviceType, instanceLabel, hostLabel string, port int, txt []string, log *logbus.Bus) *Advertiser {
	instanceLabel = strings.ReplaceAll(instanceLabel, ".", "")
	hostLabel = strings.ReplaceAll(hostLabel, ".", "")
	if !strings.HasSuffix(serviceType, ".") {
		serviceType += "."
	}
	return &Advertiser{
		service:  serviceType,
		instance: instanceLabel + "." + serviceType,
		host:     hostLabel + ".local.",
		port:     port,
		txt:      txt,
		log:      log,
		tag:      "advertise",
	}
}

// InstanceName returns the published instance label (before the service suffix).
func (a *Advertiser) InstanceName() string { return strings.TrimSuffix(a.instance, "."+a.service) }

// Start binds the multicast socket (shared 5353), joins the group on every capable
// interface, sends the first announce, and runs the query-response + periodic-announce
// loops until ctx is cancelled or Stop is called.
func (a *Advertiser) Start(ctx context.Context) error {
	lc := net.ListenConfig{Control: reuseControl}
	pc, err := lc.ListenPacket(ctx, "udp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(mdnsPort)))
	if err != nil {
		return err
	}
	conn := pc.(*net.UDPConn)
	p := ipv4.NewPacketConn(conn)
	_ = p.SetMulticastLoopback(true)
	_ = p.SetMulticastTTL(255)

	group := &net.UDPAddr{IP: net.ParseIP(mdnsGroup4), Port: mdnsPort}
	var joined []net.Interface
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		if err := p.JoinGroup(&ifi, group); err == nil {
			joined = append(joined, ifi)
		}
	}
	if len(joined) == 0 {
		_ = conn.Close()
		return &net.OpError{Op: "joingroup", Err: errNoMulticastIface}
	}

	a.mu.Lock()
	a.conn, a.p, a.ifaces = conn, p, joined
	a.mu.Unlock()

	a.log.Info(a.tag, "advertising", map[string]any{"service": a.service, "instance": a.instance, "port": a.port, "interfaces": len(joined)})

	debuglog.Go(a.log, a.tag, a.readLoop)
	debuglog.Go(a.log, a.tag, func() { a.tickLoop(ctx) })
	a.announce(announceTTL)
	return nil
}

// Stop sends a goodbye (TTL 0) and closes the socket, unblocking the loops.
func (a *Advertiser) Stop() {
	a.mu.Lock()
	conn, p := a.conn, a.p
	a.conn, a.p = nil, nil
	a.mu.Unlock()
	if conn == nil {
		return
	}
	a.sendOn(p, conn, a.encodeGoodbye())
	_ = conn.Close()
	a.log.Info(a.tag, "stopped", nil)
}

func (a *Advertiser) readLoop() {
	buf := make([]byte, 65536)
	for {
		a.mu.Lock()
		conn := a.conn
		a.mu.Unlock()
		if conn == nil {
			return
		}
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		msg, perr := parseMessage(buf[:n])
		if perr != nil || msg.response {
			continue
		}
		// Answer a browse query for our service so Serato learns us immediately.
		for _, q := range msg.questions {
			if q.qtype == typePTR && q.name == a.service {
				a.announce(announceTTL)
				break
			}
		}
	}
}

func (a *Advertiser) tickLoop(ctx context.Context) {
	// Re-announce well before the record TTL expires so browsers keep us cached.
	t := time.NewTicker(time.Duration(announceTTL/2) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.Stop()
			return
		case <-t.C:
			a.mu.Lock()
			open := a.conn != nil
			a.mu.Unlock()
			if !open {
				return
			}
			a.announce(announceTTL)
		}
	}
}

func (a *Advertiser) announce(ttl uint32) {
	a.mu.Lock()
	conn, p, ifaces := a.conn, a.p, a.ifaces
	a.mu.Unlock()
	if conn == nil {
		return
	}
	a.sendOn(p, conn, a.encodeAnnounce(localIPv4s(ifaces), ttl))
}

// encodeAnnounce builds the PTR+SRV+TXT+A response for this service (mirrors wire.go
// encodeAnnounce but for an arbitrary service/instance/host rather than the peer Service).
func (a *Advertiser) encodeAnnounce(addrs []net.IP, ttl uint32) []byte {
	var ans []byte
	count := 0
	add := func(b []byte) { ans = append(ans, b...); count++ }
	add(rrPTR(a.service, a.instance, ttl))
	add(rrSRV(a.instance, a.host, a.port, ttl))
	add(rrTXT(a.instance, a.txt, ttl))
	for _, ip := range addrs {
		if v4 := ip.To4(); v4 != nil {
			add(rrA(a.host, v4, ttl))
		}
	}
	buf := make([]byte, 12)
	putHeader(buf, flagResponse, 0, uint16(count))
	return append(buf, ans...)
}

func (a *Advertiser) encodeGoodbye() []byte {
	buf := make([]byte, 12)
	putHeader(buf, flagResponse, 0, 1)
	return append(buf, rrPTR(a.service, a.instance, goodbyeTTL)...)
}

func (a *Advertiser) sendOn(p *ipv4.PacketConn, conn *net.UDPConn, b []byte) {
	group := &net.UDPAddr{IP: net.ParseIP(mdnsGroup4), Port: mdnsPort}
	if p == nil {
		_, _ = conn.WriteToUDP(b, group)
		return
	}
	for i := range a.ifaces {
		_ = p.SetMulticastInterface(&a.ifaces[i])
		if _, err := p.WriteTo(b, nil, group); err != nil {
			_, _ = conn.WriteToUDP(b, group)
		}
	}
}
