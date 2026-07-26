package webui

// Loopback page model - the child's `virtual:true` mode. It satisfies the `shell` seam with NO
// WebView2, NO cgo and NO window, and answers the ctl runtime calls from a scripted table, so the
// whole PSH1 transport (both lanes, ordering, caps, acks, reattach, shutdown) is testable in a REAL
// child process on any build. It is a TRANSPORT FIXTURE, not a DOM: it never executes JS. Production
// selection can never reach it (procInit.Virtual is set by tests only).

import (
	"encoding/json"
	"strings"
	"sync"
)

// lbFixture is what the model answers ctl queries with. Set by the daemon-side test through the
// document it loads: a `<!--LBFIX {json}-->` comment in the initial/doc HTML (the only channel a
// real child has - it cannot import the test's memory).
type lbFixture struct {
	Snapshot string            `json:"snapshot"` // __snapshot()
	Reads    map[string]string `json:"reads"`    // __read(q) → value (exact key match, lowercased)
	Clicks   []string          `json:"clicks"`   // __click(q) hits when q is a substring of an entry
}

type loopbackWindow struct {
	onAction func(string)
	onReady  func()

	mu    sync.Mutex
	doc   string
	fix   lbFixture
	frags map[string]string // fragment id → last __patch html

	closeOnce sync.Once
	done      chan struct{}
}

func newLoopbackWindow(onAction func(string), onReady func()) *loopbackWindow {
	return &loopbackWindow{onAction: onAction, onReady: onReady,
		frags: map[string]string{}, done: make(chan struct{})}
}

func (s *loopbackWindow) run(initialHTML string, _ bool) {
	s.setHTML(initialHTML)
	if s.onReady != nil {
		go s.onReady()
	}
	<-s.done // block like a message loop
}

func (s *loopbackWindow) setHTML(html string) {
	s.mu.Lock()
	s.doc = html
	s.frags = map[string]string{}
	if fx, ok := lbParseFixture(html); ok {
		s.fix = fx
	}
	s.mu.Unlock()
}

func (s *loopbackWindow) resize(int, int) {}
func (s *loopbackWindow) show()           {}
func (s *loopbackWindow) hwnd() uintptr   { return 0 }
func (s *loopbackWindow) terminate()      { s.closeOnce.Do(func() { close(s.done) }) }

func (s *loopbackWindow) post(payload string) bool {
	if s.onAction != nil {
		s.onAction(payload)
	}
	return true
}

// eval "runs" one script: applies every __patch, replays every window.rave() act, and answers every
// __rave_evalResult call (literal second argument verbatim - that is the ordered-lane ack; a
// JSON.stringify wrapper is answered from the fixture table).
func (s *loopbackWindow) eval(js string) {
	for _, p := range lbPatches(js) {
		s.mu.Lock()
		s.frags[p.id] = p.html
		s.mu.Unlock()
	}
	for _, payload := range lbActs(js) {
		if s.onAction != nil {
			s.onAction(payload)
		}
	}
	for _, c := range lbResultCalls(js) {
		res := c.literal
		if !c.isLiteral {
			res = s.answer(c.inner)
		}
		deliverEval(c.id, res)
	}
}

// answer produces the JSON the page would have returned for one ctl primitive.
func (s *loopbackWindow) answer(inner string) string {
	s.mu.Lock()
	fix := s.fix
	s.mu.Unlock()
	switch {
	case strings.Contains(inner, "window.__snapshot"):
		return lbJSON(fix.Snapshot)
	case strings.Contains(inner, "window.__click("):
		q := strings.ToLower(lbFirstArg(inner, "window.__click("))
		for _, c := range fix.Clicks {
			if q != "" && strings.Contains(strings.ToLower(c), q) {
				return "true"
			}
		}
		return "false"
	case strings.Contains(inner, "window.__read("):
		q := strings.ToLower(lbFirstArg(inner, "window.__read("))
		if v, ok := fix.Reads[q]; ok {
			return lbJSON(v)
		}
		return "null"
	case strings.Contains(inner, "window.__set("):
		q := strings.ToLower(lbFirstArg(inner, "window.__set("))
		if _, ok := fix.Reads[q]; ok {
			return "true"
		}
		return "false"
	case strings.Contains(inner, "window.__type("),
		strings.Contains(inner, "window.__tap("), strings.Contains(inner, "window.__ctx("):
		return "true"
	}
	return "null"
}

// ── script scanners (deliberately literal: the model parses the daemon's own emitters) ──

type lbPatch struct{ id, html string }

// lbPatches extracts every window.__patch('id', html) call's arguments.
func lbPatches(js string) []lbPatch {
	var out []lbPatch
	const p = "window.__patch("
	for i := 0; ; {
		k := strings.Index(js[i:], p)
		if k < 0 {
			return out
		}
		i += k + len(p)
		id, n := lbString(js[i:])
		if n == 0 {
			continue
		}
		rest := js[i+n:]
		if !strings.HasPrefix(rest, ",") {
			continue
		}
		html, m := lbString(rest[1:])
		if m == 0 {
			continue
		}
		out = append(out, lbPatch{id: id, html: html})
		i += n + 1 + m
	}
}

// lbActs extracts every window.rave("payload") act replay (ctl Act / __ctl-tab intents).
func lbActs(js string) []string {
	var out []string
	const p = "window.rave("
	for i := 0; ; {
		k := strings.Index(js[i:], p)
		if k < 0 {
			return out
		}
		i += k + len(p)
		v, n := lbString(js[i:])
		if n == 0 {
			continue
		}
		out = append(out, v)
		i += n
	}
}

type lbResultCall struct {
	id        string
	literal   string // the second argument when it is a plain string literal (the ack: '1')
	isLiteral bool
	inner     string // the wrapped ctl script, when the second argument is JSON.stringify(...)
}

// lbResultCalls finds every __rave_evalResult(id, …) invocation. A literal second argument is the
// ordered-lane ack; anything else is a ctl round-trip whose payload is the wrapper's inner script.
func lbResultCalls(js string) []lbResultCall {
	var out []lbResultCall
	const p = "__rave_evalResult("
	for i := 0; ; {
		k := strings.Index(js[i:], p)
		if k < 0 {
			return out
		}
		i += k + len(p)
		id, n := lbString(js[i:])
		if n == 0 {
			continue
		}
		i += n
		if !strings.HasPrefix(js[i:], ",") {
			continue
		}
		c := lbResultCall{id: id}
		if v, m := lbString(js[i+1:]); m > 0 {
			c.literal, c.isLiteral = v, true
			i += 1 + m
		} else {
			c.inner = lbInner(js)
		}
		out = append(out, c)
	}
}

// lbInner pulls the ctl script out of control.go's async wrapper.
func lbInner(js string) string {
	const open = "var r=await (async()=>{"
	a := strings.Index(js, open)
	if a < 0 {
		return ""
	}
	rest := js[a+len(open):]
	b := strings.Index(rest, "})();window.__rave_evalResult(")
	if b < 0 {
		return ""
	}
	return rest[:b]
}

// lbString decodes a leading JS/JSON string literal (jsQuote's output, or a single-quoted literal
// as dispatchEvals' ack writes). Returns the value and the bytes consumed (0 = not a literal).
func lbString(s string) (string, int) {
	if s == "" {
		return "", 0
	}
	switch s[0] {
	case '"':
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == '"' {
				var v string
				if json.Unmarshal([]byte(s[:i+1]), &v) != nil {
					return "", 0
				}
				return v, i + 1
			}
		}
	case '\'':
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == '\'' {
				return strings.ReplaceAll(s[1:i], `\'`, "'"), i + 1
			}
		}
	}
	return "", 0
}

// lbFirstArg decodes the first string argument of the call starting at marker.
func lbFirstArg(js, marker string) string {
	k := strings.Index(js, marker)
	if k < 0 {
		return ""
	}
	v, _ := lbString(js[k+len(marker):])
	return v
}

func lbJSON(s string) string { return jsQuote(s) }

// lbFixtureMark wraps the scripted answers inside a document the daemon side loads.
const lbFixtureMark = "<!--LBFIX "

// lbParseFixture reads the fixture comment out of a document ("" / absent = keep the previous one).
func lbParseFixture(html string) (lbFixture, bool) {
	k := strings.Index(html, lbFixtureMark)
	if k < 0 {
		return lbFixture{}, false
	}
	rest := html[k+len(lbFixtureMark):]
	e := strings.Index(rest, "-->")
	if e < 0 {
		return lbFixture{}, false
	}
	var fx lbFixture
	if json.Unmarshal([]byte(rest[:e]), &fx) != nil {
		return lbFixture{}, false
	}
	if fx.Reads == nil {
		fx.Reads = map[string]string{}
	}
	return fx, true
}

// lbMakeFixtureDoc builds a document carrying a fixture (test helper on the daemon side).
func lbMakeFixtureDoc(fx lbFixture) string {
	raw, err := json.Marshal(fx)
	if err != nil {
		return "<html></html>"
	}
	return "<html><body>" + lbFixtureMark + string(raw) + "--></body></html>"
}
