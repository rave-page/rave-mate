package filexfer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ── unit harness (net.Pipe, no listener/bus) ─────────────────────────────────

type fixedSecret struct{}

func (fixedSecret) FileSecret(string) ([]byte, bool) { return testMaster, true }

type nopBus struct{}

func (nopBus) Publish(string, json.RawMessage)      {}
func (nopBus) Subscribe(string, func(Event)) func() { return func() {} }

func unitManager(self string) *Manager {
	return New(Options{Self: self, Bus: nopBus{}, Secrets: fixedSecret{},
		Policy: func() Policy { return Policy{Enabled: true, AutoAccept: true} }})
}

// inject registers a transfer without negotiation (unit paths).
func inject(m *Manager, x *xfer) {
	m.mu.Lock()
	m.xfers[x.ID] = x
	m.order = append(m.order, x.ID)
	m.mu.Unlock()
}

// serveGets emulates the sender loop for unit tests: answer get requests until ctl done/err.
func serveGets(t *testing.T, m *Manager, conn *fconn, id, root string, files []FileEntry) error {
	t.Helper()
	for {
		msg, err := conn.readCtl()
		if err != nil {
			return err
		}
		switch msg.T {
		case ctlGet:
			if err := m.sendFile(conn, id, msg.Index, root, files[msg.Index], msg.Offset); err != nil {
				return err
			}
		case ctlDone, ctlErr, ctlCancel:
			return nil
		}
	}
}

func randish(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}
	return b
}

// Chunker/hasher round-trip: multi-chunk file, exact content + hash verify + .part cleanup.
func TestSendRecvFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "media.bin")
	content := randish(3*chunkSize + 12345)
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	fe := FileEntry{Path: "media.bin", Size: int64(len(content))}
	sm, rm := unitManager("s"), unitManager("r")
	inject(sm, &xfer{Transfer: Transfer{ID: "t1", Send: true, State: StateActive}})
	inject(rm, &xfer{Transfer: Transfer{ID: "t1", State: StateActive}})
	sc, rc := pipePair(t, "t1")
	done := make(chan error, 1)
	go func() { done <- serveGets(t, sm, sc, "t1", src, []FileEntry{fe}) }()

	dest := filepath.Join(t.TempDir(), "out", "media.bin")
	if err := rm.recvOne(rc, "t1", 0, dest, fe); err != nil {
		t.Fatalf("recvOne: %v", err)
	}
	_ = rc.writeCtl(ctlMsg{T: ctlDone})
	if err := <-done; err != nil {
		t.Fatalf("sender: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("content mismatch (err=%v, %d vs %d bytes)", err, len(got), len(content))
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatal(".part left behind")
	}
}

// Resume: pre-seed a .part prefix; only the remainder crosses the wire; hash covers the whole file.
func TestResumeFromOffset(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.bin")
	content := randish(2*chunkSize + 999)
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	fe := FileEntry{Path: "big.bin", Size: int64(len(content))}
	prefix := int64(chunkSize + 500)

	outDir := t.TempDir()
	dest := filepath.Join(outDir, "big.bin")
	if err := os.WriteFile(dest+".part", content[:prefix], 0o644); err != nil {
		t.Fatal(err)
	}
	sm, rm := unitManager("s"), unitManager("r")
	inject(sm, &xfer{Transfer: Transfer{ID: "t2", Send: true, State: StateActive}})
	inject(rm, &xfer{Transfer: Transfer{ID: "t2", State: StateActive}})
	sc, rc := pipePair(t, "t2")

	// Interpose on the sender loop to capture the negotiated offset + bytes sent.
	var gotOffset int64 = -1
	var sentBytes int64
	done := make(chan error, 1)
	go func() {
		msg, err := sc.readCtl()
		if err != nil {
			done <- err
			return
		}
		gotOffset = msg.Offset
		before := senderDone(sm, "t2")
		err = sm.sendFile(sc, "t2", 0, src, fe, msg.Offset)
		sentBytes = senderDone(sm, "t2") - before
		done <- err
	}()
	if err := rm.recvOne(rc, "t2", 0, dest, fe); err != nil {
		t.Fatalf("recvOne: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("sender: %v", err)
	}
	if gotOffset != prefix {
		t.Fatalf("negotiated offset %d, want %d", gotOffset, prefix)
	}
	if sentBytes != fe.Size { // offset counted as done + remainder streamed
		t.Fatalf("sender progress %d, want %d", sentBytes, fe.Size)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("resumed content mismatch (err=%v)", err)
	}
}

func senderDone(m *Manager, id string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.xfers[id].Done
}

// A .part that doesn't match the source (corrupted prefix) fails the sha verify and the
// poisoned .part is removed - never silently accepted.
func TestCorruptResumePrefixRejected(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.bin")
	content := randish(chunkSize + 100)
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	fe := FileEntry{Path: "f.bin", Size: int64(len(content))}
	outDir := t.TempDir()
	dest := filepath.Join(outDir, "f.bin")
	bad := append([]byte{}, content[:600]...)
	bad[5] ^= 0xFF
	if err := os.WriteFile(dest+".part", bad, 0o644); err != nil {
		t.Fatal(err)
	}
	sm, rm := unitManager("s"), unitManager("r")
	inject(sm, &xfer{Transfer: Transfer{ID: "t3", Send: true, State: StateActive}})
	inject(rm, &xfer{Transfer: Transfer{ID: "t3", State: StateActive}})
	sc, rc := pipePair(t, "t3")
	go func() { _ = serveGets(t, sm, sc, "t3", src, []FileEntry{fe}) }()

	err := rm.recvOne(rc, "t3", 0, dest, fe)
	var pe *protoErr
	if err == nil || !asProtoErr(err, &pe) {
		t.Fatalf("want checksum protoErr, got %v", err)
	}
	if _, serr := os.Stat(dest + ".part"); !os.IsNotExist(serr) {
		t.Fatal("poisoned .part kept")
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Fatal("dest created despite mismatch")
	}
}

// Cancel mid-transfer (sender side): the chunk loop notices and tells the peer.
func TestCancelMidTransferSenderLoop(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "c.bin")
	content := randish(4 * chunkSize)
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	fe := FileEntry{Path: "c.bin", Size: int64(len(content))}
	sm := unitManager("s")
	inject(sm, &xfer{Transfer: Transfer{ID: "t4", Send: true, State: StateActive}})
	sc, rc := pipePair(t, "t4")
	done := make(chan error, 1)
	go func() { done <- sm.sendFile(sc, "t4", 0, src, fe, 0) }()

	// Read the first chunk, then cancel locally on the sender.
	typ, _, err := rc.read()
	if err != nil || typ != frameChunk {
		t.Fatalf("first chunk: typ=%d err=%v", typ, err)
	}
	sm.Cancel("t4")
	// Drain until the cancel ctl arrives.
	deadline := time.After(5 * time.Second)
	for {
		var msg ctlMsg
		typ, body, err := rc.read()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if typ == frameCtl {
			if json.Unmarshal(body, &msg) == nil && msg.T == ctlCancel {
				break
			}
			t.Fatalf("unexpected ctl %+v", msg)
		}
		select {
		case <-deadline:
			t.Fatal("no cancel ctl")
		default:
		}
	}
	if err := <-done; err == nil {
		t.Fatal("sendFile finished despite cancel")
	}
	if st := sm.Transfers()[0].State; st != StateCanceled {
		t.Fatalf("sender state %s, want canceled", st)
	}
}

func asProtoErr(err error, pe **protoErr) bool {
	for e := err; e != nil; {
		if p, ok := e.(*protoErr); ok {
			*pe = p
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// ── e2e harness (two managers, fake bus, ephemeral loopback listener) ─────────

// hub is an in-proc eventbus stand-in: Publish fans out to every node (Local on self).
type hub struct {
	mu    sync.Mutex
	nodes map[string]*hubNode
}

type hubNode struct {
	h    *hub
	id   string
	mu   sync.Mutex
	subs map[string]map[int]func(Event)
	seq  int
}

func newHub() *hub { return &hub{nodes: map[string]*hubNode{}} }

func (h *hub) node(id string) *hubNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.nodes[id]
	if n == nil {
		n = &hubNode{h: h, id: id, subs: map[string]map[int]func(Event){}}
		h.nodes[id] = n
	}
	return n
}

func (n *hubNode) Publish(topic string, data json.RawMessage) {
	n.h.mu.Lock()
	nodes := make([]*hubNode, 0, len(n.h.nodes))
	for _, p := range n.h.nodes {
		nodes = append(nodes, p)
	}
	n.h.mu.Unlock()
	for _, p := range nodes {
		p.mu.Lock()
		fns := make([]func(Event), 0, len(p.subs[topic]))
		for _, fn := range p.subs[topic] {
			fns = append(fns, fn)
		}
		p.mu.Unlock()
		for _, fn := range fns {
			fn(Event{Origin: n.id, Local: p.id == n.id, Data: data})
		}
	}
}

func (n *hubNode) Subscribe(topic string, fn func(Event)) func() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.subs[topic] == nil {
		n.subs[topic] = map[int]func(Event){}
	}
	n.seq++
	id := n.seq
	n.subs[topic][id] = fn
	return func() {
		n.mu.Lock()
		delete(n.subs[topic], id)
		n.mu.Unlock()
	}
}

// polSource is a mutable receiver policy for gating tests.
type polSource struct {
	mu  sync.Mutex
	pol Policy
}

func (p *polSource) get() Policy   { p.mu.Lock(); defer p.mu.Unlock(); return p.pol }
func (p *polSource) set(np Policy) { p.mu.Lock(); p.pol = np; p.mu.Unlock() }

// e2ePair starts a sender ("a") + receiver ("b") manager over ephemeral loopback listeners.
func e2ePair(t *testing.T, pol *polSource) (sender, receiver *Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := newHub()
	sender = New(Options{Self: "a", Bus: h.node("a"), Secrets: fixedSecret{},
		Policy: func() Policy { return Policy{} }, AdvertHost: "127.0.0.1", Ports: []int{0}})
	receiver = New(Options{Self: "b", Bus: h.node("b"), Secrets: fixedSecret{},
		Policy: pol.get, AdvertHost: "127.0.0.1", Ports: []int{0}})
	sender.retryAfter, sender.answerWait = 50*time.Millisecond, 2*time.Second
	if err := sender.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); sender.Stop(); receiver.Stop() })
	return sender, receiver
}

func waitState(t *testing.T, m *Manager, id string, want State) Transfer {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, tr := range m.Transfers() {
			if tr.ID == id && tr.State == want {
				return tr
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	var last Transfer
	for _, tr := range m.Transfers() {
		if tr.ID == id {
			last = tr
		}
	}
	t.Fatalf("transfer %s never reached %s (last: %+v)", id, want, last)
	return Transfer{}
}

// Full directory transfer over loopback: auto-accept, nested tree, both sides done.
func TestFullTransferLoopback(t *testing.T) {
	srcRoot := filepath.Join(t.TempDir(), "pack")
	mustWrite(t, filepath.Join(srcRoot, "a.bin"), chunkSize+77)
	mustWrite(t, filepath.Join(srcRoot, "sub", "b.bin"), 4096)
	mustWrite(t, filepath.Join(srcRoot, "sub", "empty"), 0)
	downDir := t.TempDir()
	pol := &polSource{pol: Policy{Enabled: true, Dir: downDir, AutoAccept: true}}
	sender, receiver := e2ePair(t, pol)

	id, err := sender.SendToPeer("b", srcRoot)
	if err != nil {
		t.Fatal(err)
	}
	st := waitState(t, sender, id, StateDone)
	rt := waitState(t, receiver, id, StateDone)
	if st.Done != st.Bytes || rt.Done != rt.Bytes || st.Bytes != rt.Bytes {
		t.Fatalf("byte accounting: send %d/%d recv %d/%d", st.Done, st.Bytes, rt.Done, rt.Bytes)
	}
	for _, rel := range []string{"pack/a.bin", "pack/sub/b.bin", "pack/sub/empty"} {
		want, err := os.ReadFile(filepath.Join(filepath.Dir(srcRoot), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(downDir, filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("received %s mismatch (err=%v)", rel, err)
		}
	}
}

// Multiple queued transfers to the same peer complete independently.
func TestMultipleQueuedTransfers(t *testing.T) {
	downDir := t.TempDir()
	pol := &polSource{pol: Policy{Enabled: true, Dir: downDir, AutoAccept: true}}
	sender, receiver := e2ePair(t, pol)
	srcDir := t.TempDir()
	var ids []string
	for i, n := range []int{chunkSize + 1, 2048, 3 * chunkSize} {
		p := filepath.Join(srcDir, string(rune('x'+i))+".bin")
		mustWrite(t, p, n)
		id, err := sender.SendToPeer("b", p)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		waitState(t, sender, id, StateDone)
		waitState(t, receiver, id, StateDone)
	}
	if len(sender.Transfers()) != 3 || len(receiver.Transfers()) != 3 {
		t.Fatalf("queue sizes: %d / %d", len(sender.Transfers()), len(receiver.Transfers()))
	}
}

// Cancel mid-transfer e2e: receiver cancels between files; both sides settle canceled.
func TestCancelMidTransferLoopback(t *testing.T) {
	srcRoot := filepath.Join(t.TempDir(), "many")
	for i := 0; i < 20; i++ {
		mustWrite(t, filepath.Join(srcRoot, string(rune('a'+i))+".bin"), 64*1024)
	}
	pol := &polSource{pol: Policy{Enabled: true, Dir: t.TempDir(), AutoAccept: true}}
	sender, receiver := e2ePair(t, pol)

	var once sync.Once
	var id string
	var mu sync.Mutex
	receiver.Subscribe(func(tr Transfer) {
		mu.Lock()
		myID := id
		mu.Unlock()
		if tr.ID == myID && tr.Done > 0 {
			once.Do(func() { go receiver.Cancel(myID) })
		}
	})
	newID, err := sender.SendToPeer("b", srcRoot)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	id = newID
	mu.Unlock()
	waitState(t, receiver, id, StateCanceled)
	waitState(t, sender, id, StateCanceled)
}

// Policy gating: disabled receiver rejects; ask-mode holds pending until Accept.
func TestPolicyGating(t *testing.T) {
	src := filepath.Join(t.TempDir(), "gate.bin")
	mustWrite(t, src, 1024)
	downDir := t.TempDir()
	pol := &polSource{pol: Policy{Enabled: false, Dir: downDir}}
	sender, receiver := e2ePair(t, pol)

	// Disabled → sender errors with the disabled reason; receiver records nothing.
	id1, err := sender.SendToPeer("b", src)
	if err != nil {
		t.Fatal(err)
	}
	tr := waitState(t, sender, id1, StateError)
	if tr.Error != reasonDisabled {
		t.Fatalf("error %q", tr.Error)
	}
	if len(receiver.Transfers()) != 0 {
		t.Fatal("disabled receiver queued a transfer")
	}

	// Ask mode → pending on the receiver; decline cancels the sender.
	pol.set(Policy{Enabled: true, Dir: downDir, AutoAccept: false})
	id2, err := sender.SendToPeer("b", src)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, receiver, id2, StatePending)
	if p := receiver.Pending(); len(p) != 1 || p[0].ID != id2 {
		t.Fatalf("pending list %+v", p)
	}
	receiver.Accept(id2, false)
	waitState(t, receiver, id2, StateCanceled)
	waitState(t, sender, id2, StateCanceled)

	// Ask mode, accepted → completes.
	id3, err := sender.SendToPeer("b", src)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, receiver, id3, StatePending)
	receiver.Accept(id3, true)
	waitState(t, sender, id3, StateDone)
	waitState(t, receiver, id3, StateDone)
	if _, err := os.Stat(filepath.Join(downDir, "gate.bin")); err != nil {
		t.Fatal(err)
	}
}

// Resume e2e: kill the first session mid-way, re-offer resumes from the .part offset.
func TestResumeAfterInterruptLoopback(t *testing.T) {
	src := filepath.Join(t.TempDir(), "resume.bin")
	content := randish(6 * chunkSize)
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	downDir := t.TempDir()
	pol := &polSource{pol: Policy{Enabled: true, Dir: downDir, AutoAccept: true}}
	sender, receiver := e2ePair(t, pol)

	// Chop the first active session once bytes flow (simulates a dropped link).
	var once sync.Once
	var mu sync.Mutex
	var id string
	receiver.Subscribe(func(tr Transfer) {
		mu.Lock()
		myID := id
		mu.Unlock()
		if tr.ID == myID && tr.State == StateActive && tr.Done > 0 && tr.Done < tr.Bytes {
			once.Do(func() {
				go func() {
					receiver.mu.Lock()
					x := receiver.xfers[myID]
					var cancel context.CancelFunc
					if x != nil {
						cancel = x.cancel
					}
					receiver.mu.Unlock()
					if cancel != nil {
						cancel() // closes the conn → both sides stall → sender re-offers
					}
				}()
			})
		}
	})
	newID, err := sender.SendToPeer("b", src)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	id = newID
	mu.Unlock()
	waitState(t, sender, id, StateDone)
	waitState(t, receiver, id, StateDone)
	got, err := os.ReadFile(filepath.Join(downDir, "resume.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("resumed content mismatch (err=%v)", err)
	}
}
