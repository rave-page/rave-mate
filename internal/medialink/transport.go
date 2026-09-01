package medialink

import (
	"bufio"
	"encoding/binary"
	"io"
	"sync"

	"rave.page/mate/internal/wirecrypto"
)

// Conn is a length-framed media connection over a reliable byte stream (TCP / any
// io.ReadWriteCloser). By default each frame is sealed with AES-256-GCM; the 96-bit nonce is a
// per-direction monotonic counter (never transmitted - both ends step in lockstep on the ordered
// stream), so the per-frame overhead is just 4 length bytes + the 16-byte GCM tag. Keys are
// HKDF-derived from the peerlink session secret (wirecrypto.Sealer). When both peers opt the media
// plane out (peers.PlaneMedia), send/recv are nil and frames go plaintext ([4B len][frame]) on the
// same reliable stream - still authenticated by the peerlink handshake, just not confidential (the
// A/V-latency escape hatch on a trusted LAN). Safe for one concurrent writer + one concurrent
// reader.
type Conn struct {
	rw io.ReadWriteCloser
	br *bufio.Reader

	send, recv *wirecrypto.Sealer // nil = plaintext mode

	wmu      sync.Mutex
	wscratch []byte // reused frame-plaintext buffer
	wout     []byte // reused length+ciphertext buffer

	rmu   sync.Mutex
	rbuf  []byte // reused ciphertext buffer
	ptbuf []byte // reused plaintext buffer
}

// maxCipher bounds an inbound sealed frame so a corrupt length can't force a huge read/alloc.
const maxCipher = headerLen + maxPayload + 64

// NewConn wraps rw as an encrypted media connection. master is the shared peerlink session secret;
// initiator must be true on the dialing end and false on the accepting end so the two directions
// use distinct keys.
func NewConn(rw io.ReadWriteCloser, master []byte, initiator bool) (*Conn, error) {
	send, recv, err := wirecrypto.NewDuplexSealer(master, nil, initiator, "medialink c2s v1", "medialink s2c v1")
	if err != nil {
		return nil, err
	}
	return &Conn{rw: rw, br: bufio.NewReaderSize(rw, 1<<16), send: send, recv: recv}, nil
}

// NewPlainConn wraps rw as an UNENCRYPTED media connection (send/recv nil): the negotiated
// both-peers-opted-out media plane. Same [4B len][frame] framing, no AEAD.
func NewPlainConn(rw io.ReadWriteCloser) *Conn {
	return &Conn{rw: rw, br: bufio.NewReaderSize(rw, 1<<16)}
}

// WriteFrame seals + sends one frame. Length prefix + ciphertext go out in a single Write to avoid
// tiny-packet latency (pair with TCP_NODELAY on the socket).
func (c *Conn) WriteFrame(f *Frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.wscratch = f.marshal(c.wscratch[:0])
	// Layout: [4-byte len][ciphertext|frame]. Seal appends into wout after the reserved prefix.
	c.wout = append(c.wout[:0], 0, 0, 0, 0)
	if c.send != nil {
		c.wout = c.send.Seal(c.wout, c.wscratch)
	} else {
		c.wout = append(c.wout, c.wscratch...)
	}
	binary.BigEndian.PutUint32(c.wout[:4], uint32(len(c.wout)-4))
	_, err := c.rw.Write(c.wout)
	return err
}

// ReadFrame receives + opens the next frame. Returns the underlying read error (incl. io.EOF) on
// stream end.
func (c *Conn) ReadFrame() (*Frame, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	var lp [4]byte
	if _, err := io.ReadFull(c.br, lp[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lp[:])
	lo, hi := uint32(16), uint32(maxCipher) // sealed: min GCM tag, max cipher (frame + tag)
	if c.recv == nil {
		lo, hi = headerLen, headerLen+maxPayload // plaintext: min frame header, max frame
	}
	if n < lo || n > hi {
		return nil, errBadPayload
	}
	if cap(c.rbuf) < int(n) {
		c.rbuf = make([]byte, n)
	}
	c.rbuf = c.rbuf[:n]
	if _, err := io.ReadFull(c.br, c.rbuf); err != nil {
		return nil, err
	}
	if c.recv == nil {
		return parseFrame(c.rbuf) // parseFrame copies the payload; reusing rbuf is safe
	}
	pt, err := c.recv.Open(c.ptbuf[:0], c.rbuf)
	if err != nil {
		return nil, err
	}
	c.ptbuf = pt
	return parseFrame(pt)
}

// Close closes the underlying stream.
func (c *Conn) Close() error { return c.rw.Close() }
