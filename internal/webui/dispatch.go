package webui

import (
	"sort"
	"strings"
	"sync"
)

// Action registry. Tab renderers self-register handlers in their own files' init() so parallel
// work never collides on a central switch. onAction consults exact handlers first, then the
// longest-matching prefix handler (so "peer-connect:" wins over a hypothetical "peer-").

// Mods carries the originating click's modifier state ("s"=Shift, "c"=Ctrl/Cmd) -
// range/toggle multi-select in list views.
type actMsg struct {
	Act, Val, Form, ID, Mods string

	// tok pins the modal session a PICKED path must be applied under (pick_actions.go). Unexported
	// on purpose: json.Unmarshal cannot reach it, so neither the DOM nor a ctl operator can forge a
	// session - only pickApply sets it, from a token Go held across the native dialog. Zero on a
	// page act, where the modal on screen is by construction the one that was clicked.
	tok modalTok
}

func (m actMsg) shift() bool { return strings.Contains(m.Mods, "s") }
func (m actMsg) ctrl() bool  { return strings.Contains(m.Mods, "c") }

// actTok resolves the modal session this act must write its form state under: the picker's pinned
// session for a returning dialog, else whatever is on screen now. Handlers that mutate modal state
// pass the result to updateModalIf, which re-checks it under the slot lock.
func (u *UI) actTok(m actMsg) modalTok {
	if m.tok.live() {
		return m.tok
	}
	return u.modalCur()
}

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
