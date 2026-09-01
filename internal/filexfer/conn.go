package filexfer

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"rave.page/mate/internal/wirecrypto"
)

// conn.go - AEAD-sealed, length-framed transfer stream (the medialink transport pattern):
// [4B len][AES-256-GCM ciphertext]; 96-bit nonce = per-direction monotonic counter (never
// transmitted; both ends step in lockstep on the ordered stream). Keys are HKDF-derived from
// the peerlink FileSecret, salted with the transfer id so parallel transfers between the
// same pair never share keys (unique nonces guaranteed per SP 800-38D §8.2.1).
// Plaintext frame = [1B type][body]. frameCtl = JSON ctlMsg; frameChunk =
// [4B fileIndex][8B offset][data ≤ chunkSize].

const (
	chunkSize   = 1 << 20 // ~1 MiB payload per chunk frame
	chunkHdrLen = 12      // 4B index + 8B offset
	maxPlain    = 1 + chunkHdrLen + chunkSize
	maxCipher   = maxPlain + 64 // GCM tag + slack; hostile-length guard
	maxPreamble = 128

	frameCtl   byte = 0
	frameChunk byte = 1

	dialTimeout  = 5 * time.Second
	preambleWait = 5 * time.Second
	ioIdle       = 60 * time.Second // per-frame read/write deadline on a net.Conn
)

var (
	errBadFrame = errors.New("filexfer: bad frame")
	errTooShort = errors.New("filexfer: short frame")
)

// fconn is the sealed transfer stream. Single reader + single writer (the session loops are
// strictly request/response, but Cancel closes concurrently - Close is safe anytime).
type fconn struct {
	rw io.ReadWriteCloser
	nc net.Conn // non-nil when rw is a real socket (idle deadlines)
	br *bufio.Reader

	send, recv *wirecrypto.Sealer

	wmu      sync.Mutex
	wscratch []byte // reused frame-plaintext buffer
	wout     []byte // reused length+ciphertext buffer
	rbuf     []byte
	pt       []byte
}

// newFConn wraps rw as a sealed transfer stream. master = peerlink FileSecret; salt = the
// transfer id; initiator MUST be true on the dialing (receiver) end only.
func newFConn(rw io.ReadWriteCloser, master []byte, salt string, initiator bool) (*fconn, error) {
	send, recv, err := wirecrypto.NewDuplexSealer(master, []byte(salt), initiator, "filexfer c2s v1", "filexfer s2c v1")
	if err != nil {
		return nil, err
	}
	fc := &fconn{rw: rw, br: bufio.NewReaderSize(rw, 1<<16), send: send, recv: recv}
	if nc, ok := rw.(net.Conn); ok {
		fc.nc = nc
	}
	return fc, nil
}

// newPlainFConn wraps rw as an UNENCRYPTED transfer stream (send/recv nil): the negotiated
// both-peers-opted-out files plane. Same [4B len][typ||body] framing, no AEAD - still
// authenticated by the peerlink handshake, just not confidential on a trusted LAN.
func newPlainFConn(rw io.ReadWriteCloser) *fconn {
	fc := &fconn{rw: rw, br: bufio.NewReaderSize(rw, 1<<16)}
	if nc, ok := rw.(net.Conn); ok {
		fc.nc = nc
	}
	return fc
}

// write seals + sends one [1B type][body] frame.
func (c *fconn) write(typ byte, body []byte) error {
	if len(body) > maxPlain-1 {
		return errBadFrame
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.wscratch = append(c.wscratch[:0], typ)
	c.wscratch = append(c.wscratch, body...)
	// Layout: [4B len][ciphertext|frame]; single Write (pair with TCP_NODELAY).
	c.wout = append(c.wout[:0], 0, 0, 0, 0)
	if c.send != nil {
		c.wout = c.send.Seal(c.wout, c.wscratch)
	} else {
		c.wout = append(c.wout, c.wscratch...)
	}
	binary.BigEndian.PutUint32(c.wout[:4], uint32(len(c.wout)-4))
	if c.nc != nil {
		_ = c.nc.SetWriteDeadline(time.Now().Add(ioIdle))
	}
	_, err := c.rw.Write(c.wout)
	return err
}

// writeCtl sends a JSON control frame.
func (c *fconn) writeCtl(msg ctlMsg) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.write(frameCtl, raw)
}

// writeChunk sends one file-data frame.
func (c *fconn) writeChunk(index uint32, offset int64, data []byte) error {
	body := make([]byte, chunkHdrLen+len(data))
	binary.BigEndian.PutUint32(body, index)
	binary.BigEndian.PutUint64(body[4:], uint64(offset))
	copy(body[chunkHdrLen:], data)
	return c.write(frameChunk, body)
}

// read receives + opens the next frame. body is valid until the next read.
func (c *fconn) read() (typ byte, body []byte, err error) {
	if c.nc != nil {
		_ = c.nc.SetReadDeadline(time.Now().Add(ioIdle))
	}
	var lp [4]byte
	if _, err := io.ReadFull(c.br, lp[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(lp[:])
	lo, hi := uint32(16), uint32(maxCipher) // sealed: min GCM tag, max cipher
	if c.recv == nil {
		lo, hi = 1, uint32(maxPlain) // plaintext: min type byte, max frame
	}
	if n < lo || n > hi {
		return 0, nil, errBadFrame
	}
	if cap(c.rbuf) < int(n) {
		c.rbuf = make([]byte, n)
	}
	c.rbuf = c.rbuf[:n]
	if _, err := io.ReadFull(c.br, c.rbuf); err != nil {
		return 0, nil, err
	}
	pt := c.rbuf
	if c.recv != nil {
		pt, err = c.recv.Open(c.pt[:0], c.rbuf)
		if err != nil {
			return 0, nil, err // tampered/corrupt frame: AEAD open fails, conn is dead
		}
		c.pt = pt
	}
	if len(pt) < 1 {
		return 0, nil, errTooShort
	}
	return pt[0], pt[1:], nil
}

// readCtl reads the next frame and requires a control message.
func (c *fconn) readCtl() (ctlMsg, error) {
	typ, body, err := c.read()
	if err != nil {
		return ctlMsg{}, err
	}
	if typ != frameCtl {
		return ctlMsg{}, errBadFrame
	}
	var msg ctlMsg
	if err := json.Unmarshal(body, &msg); err != nil {
		return ctlMsg{}, errBadFrame
	}
	return msg, nil
}

// parseChunk splits a frameChunk body.
func parseChunk(body []byte) (index uint32, offset int64, data []byte, err error) {
	if len(body) < chunkHdrLen {
		return 0, 0, nil, errTooShort
	}
	return binary.BigEndian.Uint32(body), int64(binary.BigEndian.Uint64(body[4:])), body[chunkHdrLen:], nil
}

func (c *fconn) Close() error { return c.rw.Close() }

// writePreamble sends the plaintext transfer-id correlation header ([2B len][id]). Not
// secret - authenticity comes from the AEAD keys that follow.
func writePreamble(w io.Writer, id string) error {
	if len(id) == 0 || len(id) > maxPreamble {
		return errBadFrame
	}
	buf := make([]byte, 2+len(id))
	binary.BigEndian.PutUint16(buf, uint16(len(id)))
	copy(buf[2:], id)
	_, err := w.Write(buf)
	return err
}

// readPreamble reads the transfer-id header with a deadline (when the conn supports one).
func readPreamble(c io.Reader) (string, error) {
	if nc, ok := c.(net.Conn); ok {
		_ = nc.SetReadDeadline(time.Now().Add(preambleWait))
		defer func() { _ = nc.SetReadDeadline(time.Time{}) }()
	}
	var lp [2]byte
	if _, err := io.ReadFull(c, lp[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint16(lp[:])
	if n == 0 || n > maxPreamble {
		return "", errBadFrame
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func setNoDelay(c net.Conn) {
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
}
