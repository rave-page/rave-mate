package filexfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
)

// send.go - sender side: the transfer listener + the serve loop that streams chunks on the
// receiver's get/have requests. The receiver drives (pull model), which is what makes
// offset-resume trivial.

// errRemoteCanceled marks a peer-initiated cancel (endSession settles to canceled).
var errRemoteCanceled = errors.New("canceled by the paired instance")

func (m *Manager) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go m.serveInbound(ctx, c)
	}
}

// serveInbound correlates a dialed-in socket to an offered transfer and runs the send session.
func (m *Manager) serveInbound(ctx context.Context, c net.Conn) {
	setNoDelay(c)
	id, err := readPreamble(c)
	if err != nil {
		_ = c.Close()
		return
	}
	m.mu.Lock()
	x := m.xfers[id]
	var peer string
	ok := x != nil && x.Send && !x.State.Terminal() && x.cancel == nil
	if ok {
		peer = x.Peer
	}
	m.mu.Unlock()
	if !ok {
		_ = c.Close()
		return
	}
	secret, live := m.secrets.FileSecret(peer)
	if !live {
		m.warnf("no file secret for peer", map[string]any{"peer": peer})
		_ = c.Close()
		return
	}
	conn, err := newFConn(c, secret, id, false) // accepting end = responder
	if err != nil {
		_ = c.Close()
		return
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !m.beginSession(id, cancel) {
		_ = conn.Close()
		return
	}
	go func() { <-sctx.Done(); _ = conn.Close() }() // Cancel/Stop interrupts blocking IO
	m.infof("send session up", map[string]any{"id": id, "peer": peer})
	err = m.runSend(conn, id)
	_ = conn.Close()
	m.endSession(id, err)
	m.infof("send session ended", map[string]any{"id": id})
}

// runSend serves one send session: manifest, then get/have requests until done.
func (m *Manager) runSend(conn *fconn, id string) error {
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil {
		m.mu.Unlock()
		return errors.New("transfer vanished")
	}
	files := x.files
	root := x.Path
	m.mu.Unlock()
	if err := conn.writeCtl(ctlMsg{T: ctlManifest, Files: files}); err != nil {
		return err
	}
	for {
		msg, err := conn.readCtl()
		if err != nil {
			return err
		}
		switch msg.T {
		case ctlHave:
			if msg.Index < 0 || msg.Index >= len(files) {
				return errBadFrame
			}
			m.addProgress(id, files[msg.Index].Size)
		case ctlGet:
			if msg.Index < 0 || msg.Index >= len(files) {
				return errBadFrame
			}
			fe := files[msg.Index]
			if msg.Offset < 0 || msg.Offset > fe.Size {
				return errBadFrame
			}
			if err := m.sendFile(conn, id, msg.Index, root, fe, msg.Offset); err != nil {
				var pe *protoErr
				if errors.As(err, &pe) {
					_ = conn.writeCtl(ctlMsg{T: ctlErr, Reason: pe.msg})
				}
				return err
			}
		case ctlDone:
			return nil
		case ctlCancel:
			return errRemoteCanceled
		case ctlErr:
			return protoErrf("%s", msg.Reason)
		default:
			return errBadFrame
		}
	}
}

// sendFile streams one file from offset: reads from 0 to hash the WHOLE file (the receiver
// verifies its resumed prefix against the same digest), transmits only bytes ≥ offset.
func (m *Manager) sendFile(conn *fconn, id string, index int, root string, fe FileEntry, offset int64) error {
	src := localPath(root, fe.Path)
	f, err := os.Open(src)
	if err != nil {
		return protoErrf("source unreadable: %s", fe.Path)
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || fi.Size() != fe.Size {
		return protoErrf("source changed while sending: %s", fe.Path)
	}
	if offset > 0 {
		m.addProgress(id, offset) // resumed bytes already at the peer
	}
	h := sha256.New()
	buf := make([]byte, chunkSize)
	var pos int64
	for pos < fe.Size {
		if m.canceledLocally(id) {
			_ = conn.writeCtl(ctlMsg{T: ctlCancel})
			return context.Canceled
		}
		n, rerr := io.ReadFull(f, buf[:min(int64(chunkSize), fe.Size-pos)])
		if n == 0 {
			return protoErrf("source truncated while sending: %s (%v)", fe.Path, rerr)
		}
		h.Write(buf[:n])
		end := pos + int64(n)
		if end > offset { // transmit the part of this block past the resume point
			skip := int64(0)
			if pos < offset {
				skip = offset - pos
			}
			if err := conn.writeChunk(uint32(index), pos+skip, buf[skip:n]); err != nil {
				return err
			}
			m.addProgress(id, int64(n)-skip)
		}
		pos = end
	}
	return conn.writeCtl(ctlMsg{T: ctlFileDone, Index: index, SHA: hex.EncodeToString(h.Sum(nil))})
}

// localPath maps a manifest path back onto the sender's disk: entries are prefixed with
// base(root), so they resolve relative to root's parent.
func localPath(root, rel string) string {
	return filepath.Join(filepath.Dir(root), filepath.FromSlash(rel))
}
