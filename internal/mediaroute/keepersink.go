package mediaroute

import (
	"sync/atomic"

	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/senderkeeper"
)

// Keeper is the destination host seam (senderkeeper.Supervisor in production, a fake in tests).
type Keeper interface {
	Has(name string) bool // already hosted => adoption, no new-sender admission
	Ensure(name string, w, h int) (senderkeeper.Sender, bool, error)
	Clear(name string) error
	Release(name string) error
}

// keeperSink is a GPU destination hosted by rave-mate-keeper (1b): the decode child writes into its
// texture by handle; nothing here touches a pixel. Close DETACHES (the keeper keeps the sender alive
// across our restarts - that is the point); only an explicit stop releases it (destroyer).
type keeperSink struct {
	m       *Manager // registry owner (kSinks); nil in isolation
	k       Keeper
	s       senderkeeper.Sender
	dropped atomic.Uint64 // CPU frames offered to a keeper destination (no frame path here) - counted
	lost    atomic.Bool   // keeper no longer holds it (died/killed): never reused, never parked
}

var (
	_ medialink.ZeroCopySink     = (*keeperSink)(nil)
	_ medialink.PipelineReporter = (*keeperSink)(nil)
	_ destroyer                  = (*keeperSink)(nil)
)

// destroyer is implemented by sinks whose Close only detaches; an explicit stop calls Destroy.
type destroyer interface{ Destroy() error }

// Write: a fully transparent park frame becomes a keeper Clear; any other host frame is dropped and
// counted - a keeper destination is written by the decode child (HW or CPU decode mode, 1e) only.
func (k *keeperSink) Write(f *medialink.Frame) error {
	if f.Kind == medialink.KindVideo && allZero(f.Payload) {
		return k.k.Clear(k.s.Name)
	}
	k.dropped.Add(1)
	return nil
}

func (k *keeperSink) SharedTexture() (uint64, uint32, int, int, string, bool) {
	if k.s.Handle == 0 {
		return 0, 0, 0, 0, "", false
	}
	f := k.s.Fmt
	if f == 0 {
		f = 87
	}
	return k.s.Handle, f, k.s.W, k.s.H, k.s.Name, true
}

func (k *keeperSink) PipeStats() medialink.PipelineStats {
	return medialink.PipelineStats{Dropped: k.dropped.Load()}
}

// Close detaches; the sender stays in the keeper.
func (k *keeperSink) Close() error { return nil }

// Destroy releases the sender in the keeper (explicit stop).
func (k *keeperSink) Destroy() error {
	if k.m != nil {
		k.m.mu.Lock()
		if k.m.kSinks[k.s.Name] == k {
			delete(k.m.kSinks, k.s.Name)
		}
		k.m.mu.Unlock()
	}
	if k.lost.Load() {
		return nil // nothing left in the keeper to release
	}
	return k.k.Release(k.s.Name)
}

// allZero reports a fully transparent frame (the park frame). Early-exit on the first non-zero byte,
// so a real picture costs a few bytes of scan; a 4K blank costs one 33 MB pass - park is rare.
func allZero(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// keeperHas reports whether name is an existing keeper-hosted destination (=> treated as KEPT by
// admission: adopting it allocates nothing and Resolume sees no new sender).
func (m *Manager) keeperHas(name string) bool {
	return m.keeper != nil && m.cfg != nil && m.cfg().ZeroCopyDecode() && m.cfg().SenderKeeperEnabled() && m.keeper.Has(name)
}

// isLostKeeper reports whether s is a keeper destination the keeper no longer holds.
func isLostKeeper(s medialink.Sink) bool {
	ks, ok := s.(*keeperSink)
	return ok && ks.lost.Load()
}

// checkKeeper (scan cadence, never the frame path) verifies every keeper-hosted destination still
// exists. A keeper crash/kill leaves the decode child writing into an orphaned texture Resolume no
// longer lists: mark it lost, drop it from the parked set and close its live route. The resume layer
// re-offers; the reopen goes through keeper.Ensure (one guarded re-create when the keeper is back) or,
// keeper unreachable / re-create spent, the in-process sender - never a dead route.
func (m *Manager) checkKeeper() {
	m.mu.Lock()
	if m.keeper == nil || len(m.kSinks) == 0 {
		m.mu.Unlock()
		return
	}
	names := make([]string, 0, len(m.kSinks))
	for n := range m.kSinks {
		names = append(names, n)
	}
	m.mu.Unlock()
	for _, n := range names {
		if m.keeper.Has(n) || m.keeper.Has(n) { // 2nd look redials: a stale connection is not a loss
			continue
		}
		m.mu.Lock()
		ks := m.kSinks[n]
		delete(m.kSinks, n)
		if ks != nil {
			ks.lost.Store(true)
		}
		if p, ok := m.parked[n]; ok && isLostKeeper(p.inner) {
			delete(m.parked, n)
		}
		var sessions []string
		for sess, r := range m.receives {
			if linkPrefix+r.Name == n {
				sessions = append(sessions, sess)
			}
		}
		m.mu.Unlock()
		m.log.Warn(source, "sender keeper lost a receive destination - falling back to the in-process sender (route re-establishes)",
			map[string]any{"sender": n, "routes": len(sessions)})
		for _, sess := range sessions {
			m.router.CloseRoute(sess)
		}
	}
}

// closeOrDestroy is the explicit-stop teardown: release a keeper destination, close anything else.
func closeOrDestroy(s medialink.Sink) {
	if d, ok := s.(destroyer); ok {
		_ = d.Destroy()
		return
	}
	_ = s.Close()
}
