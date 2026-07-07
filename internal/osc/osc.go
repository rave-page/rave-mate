// Package osc implements a minimal OSC 1.0 UDP client for sending tracker
// data to VRChat. Pure stdlib; no third-party deps.
package osc

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
)

// Client sends OSC messages over a connected UDP socket.
type Client struct {
	conn *net.UDPConn
}

// New dials a UDP socket to addr. Empty addr defaults to VRChat's OSC in-port.
func New(addr string) (*Client, error) {
	if addr == "" {
		addr = "127.0.0.1:9000"
	}
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, ua)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

// Send encodes one OSC message and writes it to the socket.
func (c *Client) Send(address string, args ...any) error {
	b, err := encode(address, args...)
	if err != nil {
		return err
	}
	_, err = c.conn.Write(b)
	return err
}

// Close closes the underlying socket.
func (c *Client) Close() error {
	return c.conn.Close()
}

// encode builds the OSC 1.0 wire bytes for a message (big-endian).
func encode(address string, args ...any) ([]byte, error) {
	tags := []byte{','}
	var payload []byte
	for _, a := range args {
		switch v := a.(type) {
		case float32:
			tags = append(tags, 'f')
			var buf [4]byte
			binary.BigEndian.PutUint32(buf[:], math.Float32bits(v))
			payload = append(payload, buf[:]...)
		case int32:
			tags = append(tags, 'i')
			var buf [4]byte
			binary.BigEndian.PutUint32(buf[:], uint32(v))
			payload = append(payload, buf[:]...)
		case int:
			tags = append(tags, 'i')
			var buf [4]byte
			binary.BigEndian.PutUint32(buf[:], uint32(int32(v)))
			payload = append(payload, buf[:]...)
		case string:
			tags = append(tags, 's')
			payload = append(payload, padString(v)...)
		case bool:
			if v {
				tags = append(tags, 'T')
			} else {
				tags = append(tags, 'F')
			}
		default:
			return nil, fmt.Errorf("osc: unsupported arg type %T", a)
		}
	}
	out := padString(address)
	out = append(out, padString(string(tags))...)
	out = append(out, payload...)
	return out, nil
}

// padString returns s null-terminated and zero-padded to a 4-byte boundary.
func padString(s string) []byte {
	b := make([]byte, len(s)+1) // +1 ensures at least one null terminator
	copy(b, s)
	if pad := len(b) % 4; pad != 0 {
		b = append(b, make([]byte, 4-pad)...)
	}
	return b
}
