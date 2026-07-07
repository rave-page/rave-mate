package filexfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net"
	"os"
	"path/filepath"
)

// recv.go - receiver side: dial the sender, pull each file (skipping/resuming what's already
// on disk), verify per-file sha256, rename <dest>.part → <dest> on match.

// dialPull runs one receive session for an accepted transfer.
func (m *Manager) dialPull(id string) {
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil || x.Send || x.State.Terminal() || x.cancel != nil {
		m.mu.Unlock()
		return
	}
	addr, peer := x.addr, x.Peer
	ctx := m.ctx
	m.mu.Unlock()
	if ctx == nil {
		return
	}
	secret, live := m.secrets.FileSecret(peer)
	if !live {
		m.endSession(id, errors.New("no live link to the paired instance"))
		return
	}
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		m.endSession(id, errors.New("could not reach the paired instance"))
		return
	}
	setNoDelay(c)
	if err := writePreamble(c, id); err != nil {
		_ = c.Close()
		m.endSession(id, err)
		return
	}
	conn, err := newFConn(c, secret, id, true) // dialing end = initiator
	if err != nil {
		_ = c.Close()
		m.endSession(id, err)
		return
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !m.beginSession(id, cancel) {
		_ = conn.Close()
		return
	}
	go func() { <-sctx.Done(); _ = conn.Close() }() // Cancel/Stop interrupts blocking IO
	m.infof("receive session up", map[string]any{"id": id, "peer": peer})
	err = m.runRecv(conn, id)
	_ = conn.Close()
	m.endSession(id, err)
	m.infof("receive session ended", map[string]any{"id": id})
}

// runRecv pulls every manifest file, then confirms with done.
func (m *Manager) runRecv(conn *fconn, id string) error {
	msg, err := conn.readCtl()
	if err != nil {
		return err
	}
	if msg.T != ctlManifest {
		return errBadFrame
	}
	if err := checkManifest(msg.Files); err != nil {
		_ = conn.writeCtl(ctlMsg{T: ctlErr, Reason: err.Error()})
		return err
	}
	files := msg.Files
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil {
		m.mu.Unlock()
		return errors.New("transfer vanished")
	}
	// Manifest is authoritative over the offer summary.
	x.files = files
	x.Files = len(files)
	x.Bytes = 0
	for _, fe := range files {
		x.Bytes += fe.Size
	}
	dir := x.Path
	m.mu.Unlock()
	if dir == "" {
		return protoErrf("no download directory configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return protoErrf("cannot create download directory: %v", err)
	}
	for i, fe := range files {
		if m.canceledLocally(id) {
			_ = conn.writeCtl(ctlMsg{T: ctlCancel})
			return context.Canceled
		}
		dest := filepath.Join(dir, filepath.FromSlash(fe.Path))
		if fi, serr := os.Stat(dest); serr == nil && fi.Mode().IsRegular() && fi.Size() == fe.Size {
			if err := conn.writeCtl(ctlMsg{T: ctlHave, Index: i}); err != nil {
				return err
			}
			m.addProgress(id, fe.Size)
			continue
		}
		if err := m.recvOne(conn, id, i, dest, fe); err != nil {
			var pe *protoErr
			if errors.As(err, &pe) {
				_ = conn.writeCtl(ctlMsg{T: ctlErr, Reason: pe.msg})
			}
			return err
		}
	}
	return conn.writeCtl(ctlMsg{T: ctlDone})
}

// recvOne pulls one file: resume the .part prefix (hashed locally, negotiated via get's
// offset), append chunks, verify the sender's sha256, rename into place.
func (m *Manager) recvOne(conn *fconn, id string, index int, dest string, fe FileEntry) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return protoErrf("cannot create %q: %v", filepath.Dir(dest), err)
	}
	part := dest + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return protoErrf("cannot write %q: %v", part, err)
	}
	// On failure the .part stays for resume (removed explicitly only when poisoned).
	defer func() { _ = f.Close() }() // second Close (success path) is a harmless no-op error
	h := sha256.New()
	offset, err := hashPrefix(f, h, fe.Size)
	if err != nil {
		return protoErrf("cannot resume %q: %v", part, err)
	}
	if err := conn.writeCtl(ctlMsg{T: ctlGet, Index: index, Offset: offset}); err != nil {
		return err
	}
	if offset > 0 {
		m.addProgress(id, offset)
	}
	received := offset
	for received < fe.Size {
		typ, body, rerr := conn.read()
		if rerr != nil {
			return rerr
		}
		if typ == frameCtl {
			cm, perr := parseCtlBody(body)
			if perr != nil {
				return perr
			}
			switch cm.T {
			case ctlCancel:
				return errRemoteCanceled
			case ctlErr:
				return protoErrf("%s", cm.Reason)
			}
			return errBadFrame // no other ctl is legal mid-file
		}
		idx, off, data, perr := parseChunk(body)
		if perr != nil {
			return perr
		}
		if int(idx) != index || off != received || len(data) == 0 || received+int64(len(data)) > fe.Size {
			return protoErrf("chunk out of sequence for %s", fe.Path)
		}
		if _, werr := f.Write(data); werr != nil {
			return protoErrf("write failed for %q: %v", part, werr)
		}
		h.Write(data)
		received += int64(len(data))
		m.addProgress(id, int64(len(data)))
	}
	fin, err := conn.readCtl()
	if err != nil {
		return err
	}
	switch fin.T {
	case ctlFileDone:
	case ctlCancel:
		return errRemoteCanceled
	case ctlErr:
		return protoErrf("%s", fin.Reason)
	default:
		return errBadFrame
	}
	if fin.Index != index || fin.SHA != hex.EncodeToString(h.Sum(nil)) {
		_ = f.Close()
		_ = os.Remove(part) // poisoned partial - never resume from it
		return protoErrf("checksum mismatch for %s", fe.Path)
	}
	if err := f.Close(); err != nil {
		return protoErrf("close failed for %q: %v", part, err)
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return protoErrf("cannot replace %q: %v", dest, err)
	}
	if err := os.Rename(part, dest); err != nil {
		return protoErrf("cannot finalize %q: %v", dest, err)
	}
	return nil
}

// hashPrefix hashes an existing .part into h and returns its size as the resume offset. A
// .part larger than the expected file is discarded (stale from a different manifest).
func hashPrefix(f *os.File, h hash.Hash, want int64) (int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if fi.Size() > want {
		if err := f.Truncate(0); err != nil {
			return 0, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		return 0, nil
	}
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// parseCtlBody decodes a control frame body (mid-file interleave path).
func parseCtlBody(body []byte) (ctlMsg, error) {
	var cm ctlMsg
	if err := json.Unmarshal(body, &cm); err != nil {
		return ctlMsg{}, errBadFrame
	}
	return cm, nil
}
