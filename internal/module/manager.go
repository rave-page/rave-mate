// Package module is the lifecycle supervisor for feature subsystems. The app is the one
// daemon; the manager starts only the modules whose feature is enabled and can start/stop
// them live when a user toggles a feature - a disabled feature owns no goroutines, ports,
// or subprocesses. Heavy/isolated jobs run as worker subprocesses (internal/worker),
// supervised separately.
package module

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

const source = "module"

// Service is one toggleable feature subsystem. Start is non-blocking and must bind any
// goroutines it spawns to ctx (cancelled on stop). Stop is optional extra cleanup beyond
// ctx cancellation.
type Service struct {
	Name    string
	Enabled func() bool // current desired state (reads config)
	Start   func(ctx context.Context) error
	Stop    func()
}

// Manager supervises registered services.
type Manager struct {
	log    *logbus.Bus
	parent context.Context

	mu       sync.Mutex
	services []*Service
	running  map[string]context.CancelFunc
	notify   func(title, body string) // user-facing alert on module failure (may be nil)
}

// NewManager builds a manager; parent bounds every module's lifetime.
func NewManager(log *logbus.Bus, parent context.Context) *Manager {
	return &Manager{log: log, parent: parent, running: map[string]context.CancelFunc{}}
}

// SetNotifier wires a user-facing alert (e.g. a desktop toast) raised when a module fails
// or panics. Nil (the default / service mode) just logs.
func (m *Manager) SetNotifier(fn func(title, body string)) {
	m.mu.Lock()
	m.notify = fn
	m.mu.Unlock()
}

// alert logs an error + raises the user notifier if set. Never blocks on a slow notifier.
func (m *Manager) alert(title, body string) {
	m.mu.Lock()
	fn := m.notify
	m.mu.Unlock()
	if fn != nil {
		debuglog.Go(m.log, source, func() { fn(title, body) })
	}
}

// callStart runs svc.Start under a panic guard so a module that panics on startup is
// contained (logged + reported) instead of taking the whole daemon down.
func (m *Manager) callStart(svc *Service, ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			m.log.Error(source, "module panicked on start",
				map[string]any{"module": svc.Name, "panic": fmt.Sprintf("%v", r), "stack": string(debug.Stack())})
		}
	}()
	return svc.Start(ctx)
}

// callStop runs svc.Stop under a panic guard (a botched teardown must not crash shutdown).
func (m *Manager) callStop(svc *Service) {
	defer func() {
		if r := recover(); r != nil {
			m.log.Error(source, "module panicked on stop",
				map[string]any{"module": svc.Name, "panic": fmt.Sprintf("%v", r), "stack": string(debug.Stack())})
		}
	}()
	svc.Stop()
}

// Add registers a service (does not start it).
func (m *Manager) Add(svc *Service) {
	m.mu.Lock()
	m.services = append(m.services, svc)
	m.mu.Unlock()
}

// StartEnabled starts every registered service whose feature is currently enabled.
func (m *Manager) StartEnabled() {
	m.mu.Lock()
	svcs := append([]*Service{}, m.services...)
	m.mu.Unlock()
	for _, svc := range svcs {
		if svc.Enabled == nil || svc.Enabled() {
			m.startLocked(svc)
		}
	}
}

func (m *Manager) startLocked(svc *Service) {
	m.mu.Lock()
	if _, ok := m.running[svc.Name]; ok {
		m.mu.Unlock()
		return // already running
	}
	ctx, cancel := context.WithCancel(m.parent)
	m.running[svc.Name] = cancel
	m.mu.Unlock()

	if svc.Start == nil {
		return
	}
	if err := m.callStart(svc, ctx); err != nil {
		m.log.Error(source, "module failed to start", map[string]any{"module": svc.Name, "error": err.Error()})
		m.alert("Module failed", svc.Name+" couldn't start: "+err.Error()+". Other features keep running.")
		m.stop(svc.Name)
		return
	}
	m.log.Info(source, "module started", map[string]any{"module": svc.Name})
}

// SetEnabled applies a live toggle: starts the module if turning on, stops it if off.
func (m *Manager) SetEnabled(name string, on bool) {
	svc := m.find(name)
	if svc == nil {
		return
	}
	if on {
		m.startLocked(svc)
	} else {
		m.stop(name)
	}
}

// IsRunning reports whether a module currently holds a live context.
func (m *Manager) IsRunning(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[name]
	return ok
}

func (m *Manager) stop(name string) {
	m.mu.Lock()
	cancel, ok := m.running[name]
	if ok {
		delete(m.running, name)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	cancel()
	if svc := m.find(name); svc != nil && svc.Stop != nil {
		m.callStop(svc)
	}
	m.log.Info(source, "module stopped", map[string]any{"module": name})
}

// StopAll stops every running module (shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	names := make([]string, 0, len(m.running))
	for n := range m.running {
		names = append(names, n)
	}
	m.mu.Unlock()
	for _, n := range names {
		m.stop(n)
	}
}

func (m *Manager) find(name string) *Service {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.services {
		if s.Name == name {
			return s
		}
	}
	return nil
}
