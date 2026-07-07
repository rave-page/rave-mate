package webui

import (
	"sort"
	"strings"
	"sync"
)

// Action registry. Tab renderers self-register handlers in their own files' init() so parallel
// work never collides on a central switch. onAction consults exact handlers first, then the
// longest-matching prefix handler (so "peer-connect:" wins over a hypothetical "peer-").

type actMsg struct{ Act, Val, Form, ID string }

type actHandler func(u *UI, m actMsg)

var (
	regMu         sync.Mutex
	exactHandlers = map[string]actHandler{}
	prefixReg     []prefixHandler
	prefixSorted  bool
)

type prefixHandler struct {
	prefix string
	h      actHandler
}

// onExact registers a handler for an exact action name.
func onExact(act string, h actHandler) {
	regMu.Lock()
	exactHandlers[act] = h
	regMu.Unlock()
}

// onPrefix registers a handler for any action starting with prefix (e.g. "peer-connect:").
func onPrefix(prefix string, h actHandler) {
	regMu.Lock()
	prefixReg = append(prefixReg, prefixHandler{prefix, h})
	prefixSorted = false
	regMu.Unlock()
}

// dispatch routes m to a registered handler; returns false if none matched.
func (u *UI) dispatch(m actMsg) bool {
	regMu.Lock()
	if !prefixSorted {
		sort.SliceStable(prefixReg, func(i, j int) bool { return len(prefixReg[i].prefix) > len(prefixReg[j].prefix) })
		prefixSorted = true
	}
	if h, ok := exactHandlers[m.Act]; ok {
		regMu.Unlock()
		h(u, m)
		return true
	}
	for _, ph := range prefixReg {
		if strings.HasPrefix(m.Act, ph.prefix) {
			regMu.Unlock()
			ph.h(u, m)
			return true
		}
	}
	regMu.Unlock()
	return false
}

// arg trims prefix from the action name (helper for prefix handlers).
func (m actMsg) arg(prefix string) string { return strings.TrimPrefix(m.Act, prefix) }

// ── core registrations (modal lifecycle) ──

func init() {
	onExact("modal-close", func(u *UI, _ actMsg) { u.closeModal() })
}
