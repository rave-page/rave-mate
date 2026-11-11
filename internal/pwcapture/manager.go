package pwcapture

import (
	"context"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

// Config gates and paces discovery.
type Config struct {
	Enabled       func() bool   // feature gate; nil => on whenever capture is available
	DiscoverEvery time.Duration // node-poll interval; 0 => 3s
}

// Manager discovers PipeWire video producer nodes and registers the capturable ones as medialink
// sources, reconciling on every poll so a node that appears (VRChat starts) is offered and one that
// vanishes (VRChat quits) is withdrawn - the reconnect/leak-free behaviour the brief requires lives
// here (registration churn) and in the capture Source (mid-stream reconnect by node name).
type Manager struct {
	log *logbus.Bus
	cfg Config

	mu         sync.Mutex
	router     Router
	nodes      []Node          // last discovered snapshot (ctl)
	registered map[string]Node // sourceID -> node currently registered

	cancel context.CancelFunc
	done   chan struct{}
}

// CaptureBuilt reports whether the PipeWire capture shim is compiled in AND libpipewire loads
// (the package-level form of Manager.Available, for callers without a Manager - e.g. ctl pw-nodes).
func CaptureBuilt() bool { return captureAvailable() }

// New builds a Manager. cfg.Enabled nil = always on when capture is available.
func New(log *logbus.Bus, cfg Config) *Manager {
	return &Manager{log: log, cfg: cfg, registered: map[string]Node{}}
}

// SetRouter attaches the medialink source registrar.
func (m *Manager) SetRouter(r Router) {
	m.mu.Lock()
	m.router = r
	m.mu.Unlock()
}

// Available reports whether PipeWire capture is built AND libpipewire loads at runtime.
func (m *Manager) Available() bool { return captureAvailable() }

// Nodes returns the last discovered video-node snapshot (for ctl).
func (m *Manager) Nodes() []Node {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Node(nil), m.nodes...)
}

// Start begins the discovery loop. A no-op (logged once) when capture is unavailable.
func (m *Manager) Start(ctx context.Context) error {
	if !captureAvailable() {
		if m.log != nil {
			m.log.Info(source, "PipeWire capture unavailable (no -tags pipewire build or libpipewire-0.3 missing) - no video nodes offered", nil)
		}
		return nil
	}
	cctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.done = make(chan struct{})
	go m.loop(cctx)
	return nil
}

// Stop ends discovery and withdraws every registered source.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
		<-m.done
	}
	m.applyReconcile(nil)
}

func (m *Manager) loop(ctx context.Context) {
	defer close(m.done)
	every := m.cfg.DiscoverEvery
	if every <= 0 {
		every = 3 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	m.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.scan(ctx)
		}
	}
}

// scan discovers nodes and reconciles source registrations.
func (m *Manager) scan(ctx context.Context) {
	if m.cfg.Enabled != nil && !m.cfg.Enabled() {
		m.setNodes(nil)
		m.applyReconcile(nil)
		return
	}
	nodes, ok := DiscoverNodes(ctx)
	if !ok {
		return // transient pw-dump failure: keep current registrations
	}
	m.setNodes(nodes)
	desired := map[string]Node{}
	for _, n := range nodes {
		if n.Capturable && n.Width > 0 && n.Height > 0 {
			desired[sourceID(n.Name)] = n
		}
	}
	m.applyReconcile(desired)
}

func (m *Manager) setNodes(nodes []Node) {
	m.mu.Lock()
	m.nodes = nodes
	m.mu.Unlock()
}

// applyReconcile registers newly-appeared sources and unregisters vanished ones. desired nil =
// withdraw everything (stop/disable). Router calls run outside the lock (RegisterSource re-
// advertises); only the closure is stored, so there is no reentrancy with openTap.
func (m *Manager) applyReconcile(desired map[string]Node) {
	m.mu.Lock()
	router := m.router
	var toAdd []Node
	var toDel []string
	for id, n := range desired {
		if _, ok := m.registered[id]; !ok {
			toAdd = append(toAdd, n)
		}
	}
	for id := range m.registered {
		if _, ok := desired[id]; !ok {
			toDel = append(toDel, id)
		}
	}
	m.registered = map[string]Node{}
	for id, n := range desired {
		m.registered[id] = n
	}
	m.mu.Unlock()

	if router == nil {
		return
	}
	for _, n := range toAdd {
		router.RegisterSource(medialink.SourceDesc{
			ID: sourceID(n.Name), Name: n.Label(), Kind: medialink.KindVideo,
			Codec: medialink.CodecNRGBA, Width: n.Width, Height: n.Height, FPS: n.FPS,
		}, m.openTap)
		if m.log != nil {
			m.log.Info(source, "PipeWire video node offered", map[string]any{
				"node": n.Name, "class": n.MediaClass, "size": sizeStr(n.Width, n.Height), "fps": n.FPS})
		}
	}
	for _, id := range toDel {
		router.UnregisterSource(id)
		if m.log != nil {
			m.log.Info(source, "PipeWire video node withdrawn", map[string]any{"source": id})
		}
	}
}

// openTap is the medialink.SourceOpen: start a capture of the offered node's PipeWire stream.
func (m *Manager) openTap(ctx context.Context, off medialink.Offer) (medialink.Source, error) {
	name := strings.TrimPrefix(off.SourceID, "pw:")
	return openCapture(ctx, m.log, name)
}

func sizeStr(w, h int) string {
	if w <= 0 || h <= 0 {
		return "?"
	}
	return itoa(w) + "x" + itoa(h)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
