package medialink

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"sync"
)

// Conn is an AEAD-sealed, length-framed media connection over a reliable byte stream (TCP / any
// io.ReadWriteCloser). Each frame is sealed with AES-256-GCM; the 96-bit nonce is a per-direction
// monotonic counter (never transmitted - both ends step in lockstep on the ordered stream), so the
// per-frame overhead is just 4 length bytes + the 16-byte GCM tag. Keys are HKDF-derived from the
// peerlink session secret, giving the media plane its own encryption (which peerlink's control
// plane still lacks). Safe for one concurrent writer + one concurrent reader.
type Conn struct {
	rw io.ReadWriteCloser
	br *bufio.Reader

	send, recv cipher.AEAD

	wmu      sync.Mutex
	sctr     uint64
	wscratch []byte // reused frame-plaintext buffer
	wout     []byte // reused length+ciphertext buffer

	rmu   sync.Mutex
	rctr  uint64
	rbuf  []byte // reused ciphertext buffer
	ptbuf []byte // reused plaintext buffer
}

// maxCipher bounds an inbound sealed frame so a corrupt length can't force a huge read/alloc.
const maxCipher = headerLen + maxPayload + 64

// NewConn wraps rw as an encrypted media connection. master is the shared peerlink session secret;
// initiator must be true on the dialing end and false on the accepting end so the two directions
// use distinct keys.
func NewConn(rw io.ReadWriteCloser, master []byte, initiator bool) (*Conn, error) {
	sk, rk, err := deriveKeys(master, initiator)
	if err != nil {
		return nil, err
	}
	sa, err := newGCM(sk)
	if err != nil {
		return nil, err
	}
	ra, err := newGCM(rk)
	if err != nil {
		return nil, err
	}
	return &Conn{rw: rw, br: bufio.NewReaderSize(rw, 1<<16), send: sa, recv: ra}, nil
}

// deriveKeys splits the master into per-direction 256-bit keys. The initiator sends on c2s +
// receives on s2c; the responder mirrors.
func deriveKeys(master []byte, initiator bool) (send, recv []byte, err error) {
	c2s, err := hkdf.Key(sha256.New, master, nil, "medialink c2s v1", 32)
	if err != nil {
		return nil, nil, err
	}
	s2c, err := hkdf.Key(sha256.New, master, nil, "medialink s2c v1", 32)
	if err != nil {
		return nil, nil, err
	}
	if initiator {
		return c2s, s2c, nil
	}
	return s2c, c2s, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// WriteFrame seals + sends one frame. Length prefix + ciphertext go out in a single Write to avoid
// tiny-packet latency (pair with TCP_NODELAY on the socket).
func (c *Conn) WriteFrame(f *Frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.wscratch = f.marshal(c.wscratch[:0])
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], c.sctr)
	c.sctr++
	// Layout: [4-byte len][ciphertext]. Seal appends into wout after the reserved length prefix.
	c.wout = append(c.wout[:0], 0, 0, 0, 0)
	c.wout = c.send.Seal(c.wout, nonce[:], c.wscratch, nil)
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
	if n < 16 || n > maxCipher {
		return nil, errBadPayload
	}
	if cap(c.rbuf) < int(n) {
		c.rbuf = make([]byte, n)
	}
	c.rbuf = c.rbuf[:n]
	if _, err := io.ReadFull(c.br, c.rbuf); err != nil {
		return nil, err
	}
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], c.rctr)
	c.rctr++
	pt, err := c.recv.Open(c.ptbuf[:0], nonce[:], c.rbuf, nil)
	if err != nil {
		return nil, err
	}
	c.ptbuf = pt
	return parseFrame(pt)
}

// Close closes the underlying stream.
func (c *Conn) Close() error { return c.rw.Close() }
