// Package discovery is a pure-stdlib mDNS/DNS-SD responder + browser for finding other
// rave-mate instances on the LAN. It advertises _ravemate._tcp.local. with a TXT record
// (node id, nickname, tcp port, protocol version, identity fingerprint) and browses for
// peers. No external dependency beyond golang.org/x/net/ipv4 (already vendored) for
// per-interface multicast joins. Two instances on the SAME host discover each other
// (multicast loopback + SO_REUSEADDR), which is also how the dev/test flow works.
package discovery

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

const (
	serviceType   = "_ravemate._tcp.local."
	mdnsGroup4    = "224.0.0.251"
	mdnsPort      = 5353
	queryInterval = 10 * time.Second
	peerTTL       = 35 * time.Second // drop peers unseen this long (≈3 missed announces)
	logTag        = "discovery"
)

// Service is what this instance advertises.
type Service struct {
	NodeID       string
	Name         string // human nickname
	Port         int    // peer-link TCP port
	ProtoVersion int
	IdentityFP   string // identity-pubkey fingerprint (== NodeID today; explicit for fwd compat)
}

func (s Service) serviceName() string  { return serviceType }
func (s Service) instanceName() string { return s.NodeID + "." + serviceType }
func (s Service) hostName() string     { return s.NodeID + ".local." }
func (s Service) txtStrings() []string {
	return []string{
		"nid=" + s.NodeID,
		"name=" + s.Name,
		"port=" + strconv.Itoa(s.Port),
		"pv=" + strconv.Itoa(s.ProtoVersion),
		"fp=" + s.IdentityFP,
	}
}

// Peer is a discovered rave-mate instance.
type Peer struct {
	NodeID       string
	Name         string
	Address      net.IP // source address we heard it from (best for dialing)
	Port         int
	FP           string
	ProtoVersion int
	LastSeen     time.Time
}

// Discovery runs the responder + browser on a shared multicast socket.
type Discovery struct {
	svc   Service
	log   *logbus.Bus
	clock func() time.Time

	mu      sync.Mutex
	peers   map[string]Peer
	subs    map[int]chan []Peer
	nextSub int

	conn   *net.UDPConn
	p      *ipv4.PacketConn
	ifaces []net.Interface
}

// SetPort sets the advertised peer-link TCP port. Call before Start (the port is known
// only after the peer listener binds).
func (d *Discovery) SetPort(p int) { d.svc.Port = p }

// New builds a Discovery for the given service advertisement.
func New(svc Service, log *logbus.Bus) *Discovery {
	return &Discovery{
		svc:   svc,
		log:   log,
		clock: time.Now,
		peers: map[string]Peer{},
		subs:  map[int]chan []Peer{},
	}
}

// Start binds the multicast socket, joins the group on every capable interface, and runs
// the read/announce/query/expiry loops until ctx is cancelled or Stop is called.
func (d *Discovery) Start(ctx context.Context) error {
	lc := net.ListenConfig{Control: reuseControl}
	pc, err := lc.ListenPacket(ctx, "udp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(mdnsPort)))
	if err != nil {
		return err
	}
	conn := pc.(*net.UDPConn)
	p := ipv4.NewPacketConn(conn)
	_ = p.SetMulticastLoopback(true) // so a second instance on this host hears us
	_ = p.SetMulticastTTL(255)       // mDNS standard

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

	d.mu.Lock()
	d.conn, d.p, d.ifaces = conn, p, joined
	d.mu.Unlock()

	d.log.Info(logTag, "started", map[string]any{"interfaces": len(joined), "node": d.svc.NodeID})

	debuglog.Go(d.log, logTag, d.readLoop)
	debuglog.Go(d.log, logTag, func() { d.tickLoop(ctx) })

	// Announce + query immediately so discovery is snappy.
	d.announce(announceTTL)
	d.query()
	return nil
}

// Stop sends a goodbye (TTL 0) and closes the socket, unblocking the loops.
func (d *Discovery) Stop() {
	d.mu.Lock()
	conn, p := d.conn, d.p
	d.conn, d.p = nil, nil
	d.mu.Unlock()
	if conn == nil {
		return
	}
	d.sendOn(p, conn, encodeGoodbye(d.svc))
	_ = conn.Close()
	d.log.Info(logTag, "stopped", nil)
}

// Peers returns the current live peer set.
func (d *Discovery) Peers() []Peer {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked()
}

// Subscribe returns a channel that receives the full peer set whenever it changes, plus an
// unsubscribe func. The current set is delivered immediately.
func (d *Discovery) Subscribe() (<-chan []Peer, func()) {
	ch := make(chan []Peer, 4)
	d.mu.Lock()
	id := d.nextSub
	d.nextSub++
	d.subs[id] = ch
	cur := d.snapshotLocked()
	d.mu.Unlock()
	ch <- cur
	return ch, func() {
		d.mu.Lock()
		if c, ok := d.subs[id]; ok {
			delete(d.subs, id)
			close(c)
		}
		d.mu.Unlock()
	}
}

// ── loops ────────────────────────────────────────────────────────────────────

func (d *Discovery) readLoop() {
	buf := make([]byte, 65536)
	for {
		d.mu.Lock()
		conn := d.conn
		d.mu.Unlock()
		if conn == nil {
			return
		}
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed
		}
		msg, perr := parseMessage(buf[:n])
		if perr != nil {
			continue
		}
		d.handle(msg, src)
	}
}

func (d *Discovery) tickLoop(ctx context.Context) {
	t := time.NewTicker(queryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			d.Stop()
			return
		case <-t.C:
			d.mu.Lock()
			open := d.conn != nil
			d.mu.Unlock()
			if !open {
				return
			}
			d.announce(announceTTL)
			d.query()
			d.expire()
		}
	}
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (d *Discovery) handle(msg message, src *net.UDPAddr) {
	// Answer browse queries for our service so a freshly-started peer learns us at once.
	if !msg.response {
		for _, q := range msg.questions {
			if q.qtype == typePTR && q.name == serviceType {
				d.announce(announceTTL)
				return
			}
		}
		return
	}

	// A response: pull the TXT (which carries node id / port / fp) for our service.
	txt := map[string]string{}
	gotOurService := false
	for _, r := range msg.records {
		switch r.rtype {
		case typePTR:
			if r.name == serviceType {
				gotOurService = true
			}
		case typeTXT:
			for _, kv := range r.txt {
				if i := indexByte(kv, '='); i >= 0 {
					txt[kv[:i]] = kv[i+1:]
				}
			}
		}
	}
	nid := txt["nid"]
	if nid == "" || (!gotOurService && txt["pv"] == "") {
		return // not one of ours
	}
	if nid == d.svc.NodeID {
		return // our own announce echoed back
	}

	port, _ := strconv.Atoi(txt["port"])
	pv, _ := strconv.Atoi(txt["pv"])
	peer := Peer{
		NodeID: nid, Name: txt["name"], Address: append(net.IP(nil), src.IP...),
		Port: port, FP: txt["fp"], ProtoVersion: pv, LastSeen: d.clock(),
	}
	d.upsert(peer)
}

func (d *Discovery) upsert(p Peer) {
	d.mu.Lock()
	prev, existed := d.peers[p.NodeID]
	changed := !existed || prev.Address.String() != p.Address.String() ||
		prev.Port != p.Port || prev.Name != p.Name || prev.FP != p.FP
	d.peers[p.NodeID] = p
	var snap []Peer
	if changed {
		snap = d.snapshotLocked()
	}
	d.mu.Unlock()
	if changed {
		d.notify(snap)
	}
}

func (d *Discovery) expire() {
	cutoff := d.clock().Add(-peerTTL)
	d.mu.Lock()
	var dropped bool
	for id, p := range d.peers {
		if p.LastSeen.Before(cutoff) {
			delete(d.peers, id)
			dropped = true
		}
	}
	var snap []Peer
	if dropped {
		snap = d.snapshotLocked()
	}
	d.mu.Unlock()
	if dropped {
		d.notify(snap)
	}
}

// ── send ─────────────────────────────────────────────────────────────────────

func (d *Discovery) announce(ttl uint32) {
	d.broadcast(encodeAnnounce(d.svc, localIPv4s(d.ifaces), ttl))
}
func (d *Discovery) query() { d.broadcast(encodeQuery(serviceType)) }

func (d *Discovery) broadcast(b []byte) {
	d.mu.Lock()
	conn, p := d.conn, d.p
	d.mu.Unlock()
	if conn != nil {
		d.sendOn(p, conn, b)
	}
}

// sendOn writes b to the multicast group out each joined interface (multi-NIC coverage).
func (d *Discovery) sendOn(p *ipv4.PacketConn, conn *net.UDPConn, b []byte) {
	group := &net.UDPAddr{IP: net.ParseIP(mdnsGroup4), Port: mdnsPort}
	if p == nil {
		_, _ = conn.WriteToUDP(b, group)
		return
	}
	for i := range d.ifaces {
		_ = p.SetMulticastInterface(&d.ifaces[i])
		if _, err := p.WriteTo(b, nil, group); err != nil {
			_, _ = conn.WriteToUDP(b, group) // fall back to default route
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (d *Discovery) snapshotLocked() []Peer {
	out := make([]Peer, 0, len(d.peers))
	for _, p := range d.peers {
		out = append(out, p)
	}
	return out
}

func (d *Discovery) notify(snap []Peer) {
	d.mu.Lock()
	subs := make([]chan []Peer, 0, len(d.subs))
	for _, c := range d.subs {
		subs = append(subs, c)
	}
	d.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- snap:
		default: // slow consumer - drop; next change re-sends the full set
		}
	}
}

func localIPv4s(ifaces []net.Interface) []net.IP {
	var out []net.IP
	for _, ifi := range ifaces {
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if v4 := ipn.IP.To4(); v4 != nil && !v4.IsLoopback() {
					out = append(out, v4)
				}
			}
		}
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
