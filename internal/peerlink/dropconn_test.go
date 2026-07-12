package peerlink

import (
	"context"
	"testing"
	"time"
)

type nopConn struct{}

func (nopConn) Send(context.Context, []byte) error { return nil }
func (nopConn) Recv(context.Context) ([]byte, error) {
	select {} // never delivers; tests only exercise close paths
}
func (nopConn) Close() {}

func testConn(m *Manager, node string) *connState {
	res := &Result{PeerNodeID: node}
	cs := &connState{link: newLink(nopConn{}, res), res: res}
	cs.ctx, cs.cancel = context.WithCancel(context.Background())
	cs.link.onClose = func(error) { m.dropConn(cs) }
	return cs
}

// Replacing a stale conn must close it OUTSIDE m.mu (Close fires onClose→dropConn, which
// locks m.mu - closing under the lock self-deadlocked and froze every render/send behind
// Connections()). And the stale link's onClose must not evict the fresh registration.
func TestReplaceStaleConnNoDeadlockKeepsFresh(t *testing.T) {
	m := &Manager{conns: map[string]*connState{}}
	old := testConn(m, "peerA")
	m.conns["peerA"] = old

	fresh := testConn(m, "peerA")
	done := make(chan struct{})
	go func() { // mirrors runHandshake's register block
		defer close(done)
		m.mu.Lock()
		prev := m.conns["peerA"]
		m.conns["peerA"] = fresh
		m.mu.Unlock()
		if prev != nil {
			prev.cancel()
			prev.link.Close()
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("register-over-stale deadlocked")
	}
	if got := m.conns["peerA"]; got != fresh {
		t.Fatal("stale link's onClose evicted the fresh connection")
	}
	fresh.link.Close() // own close still drops it
	if _, ok := m.conns["peerA"]; ok {
		t.Fatal("own close did not drop the connection")
	}
}
