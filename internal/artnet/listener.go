package artnet

import (
	"context"
	"net"
	"time"

	"rave.page/mate/internal/logbus"
)

const source = "artnet"

// Listener binds a UDP socket, ingests ArtDmx into the store, and answers ArtPoll with an
// ArtPollReply so consoles discover this node. Pure stdlib net.
type Listener struct {
	log       *logbus.Bus
	store     *Store
	shortName string
	longName  string
}

// NewListener builds an Art-Net listener writing into store. short/long name this node in
// ArtPollReply.
func NewListener(log *logbus.Bus, store *Store, short, long string) *Listener {
	return &Listener{log: log, store: store, shortName: short, longName: long}
}

// Run binds addr (":6454" if empty) and serves until ctx is cancelled. Blocks.
func (l *Listener) Run(ctx context.Context, addr string) error {
	if addr == "" {
		addr = ":6454"
	}
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", ua)
	if err != nil {
		return err
	}
	l.log.Info(source, "art-net listening", map[string]any{"addr": conn.LocalAddr().String()})
	go func() { <-ctx.Done(); _ = conn.Close() }()
	defer func() { _ = conn.Close() }()

	localIP := preferredIPv4()
	buf := make([]byte, 1024)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		l.handle(conn, buf[:n], from, localIP)
	}
}

// handle dispatches one datagram: ArtDmx → store, ArtPoll → reply.
func (l *Listener) handle(conn *net.UDPConn, p []byte, from *net.UDPAddr, localIP net.IP) {
	switch opcodeOf(p) {
	case opDmx:
		d, err := ParseArtDmx(p)
		if err != nil {
			return // malformed / not-for-us; ignore
		}
		l.store.Set(d.Universe, d.Sequence, d.Data, from.IP.String(), time.Now())
	case opPoll:
		var unis []uint16
		for _, st := range l.store.Stats(time.Now(), time.Hour) {
			unis = append(unis, st.Universe)
		}
		reply := BuildArtPollReply(localIP, l.shortName, l.longName, unis)
		// Unicast the reply to the polling node (spec permits directed replies; simplest + reliable
		// on a switched LAN). from carries the console's address.
		if _, err := conn.WriteToUDP(reply, from); err != nil {
			l.log.Debug(source, "art-poll reply failed", map[string]any{"to": from.String(), "error": err.Error()})
		}
	}
}

// preferredIPv4 returns a non-loopback IPv4 for ArtPollReply's advertised address (loopback fallback).
func preferredIPv4() net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return net.IPv4(127, 0, 0, 1)
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
			if v4 := ipn.IP.To4(); v4 != nil {
				return v4
			}
		}
	}
	return net.IPv4(127, 0, 0, 1)
}
