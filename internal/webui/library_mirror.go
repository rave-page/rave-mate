package webui

// Controller-side Library mirror: when a paired peer is selected in the Library target
// switcher, lib-body embeds the PEER's Go-rendered Library tab in a same-origin iframe and
// replays its live eval/patch stream - the exact document the peer's own pipeline renders,
// remote-driven. Every input inside the surface forwards to the peer and EXECUTES THERE
// (gridfix, cue edits, transcodes, tag writes, audition audio - all on the peer's machine
// against its files). The peer's visible window is untouched (headless session, remoteui_host).

import (
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/zigui"
)

// mirrorOpenTimeout bounds the open→doc wait; on expiry the banner shows an error (a peer
// running an older build never answers ChanRemoteUI).
const mirrorOpenTimeout = 12 * time.Second

// mirror session status values.
const (
	mirrorConnecting = "connecting"
	mirrorLive       = "live"
	mirrorError      = "error"
	mirrorClosed     = "closed"
)

type mirrorSt struct {
	mu      sync.Mutex
	target  string // peer nodeID the session points at ("" = none)
	sid     string // current session id (controller-minted; stale frames are dropped by it)
	status  string
	errMsg  string
	docSize int // last applied doc size (diagnostics)
}

var (
	mirrorMu  sync.Mutex
	mirrorSts = map[*UI]*mirrorSt{}
)

func (u *UI) mirror() *mirrorSt {
	mirrorMu.Lock()
	defer mirrorMu.Unlock()
	s := mirrorSts[u]
	if s == nil {
		s = &mirrorSt{}
		mirrorSts[u] = s
	}
	return s
}

func init() {
	// rmirror-post carries the mirrored page's original window.rave payload in Form
	// (Val fallback = ctl `act rmirror-post <payload>` drives the mirror act-level).
	onExact("rmirror-post", func(u *UI, m actMsg) {
		if m.Form != "" {
			u.mirrorForwardAct(m.Form)
		} else {
			u.mirrorForwardAct(m.Val)
		}
	})
	onExact("rmirror-reconnect", func(u *UI, _ actMsg) { u.patchMain() }) // re-render reopens
}

// ── render ──────────────────────────────────────────────────────────────────────

// libMirrorSt is the resolved mirror surface (#lib-body while a peer is targeted).
type libMirrorSt struct {
	NoLink    bool           `json:"noLink,omitempty"` // no peer-link hub: the whole body is one card
	NoLinkMsg string         `json:"noLinkMsg,omitempty"`
	Banner    libMirrorBanSt `json:"banner"`
}

// libMirrorBanSt is the status strip (#rmirror-banner, patched on every session state move).
// HasNote / IsErr are explicit: an empty i18n string must not flip the branch.
type libMirrorBanSt struct {
	Status    string `json:"status"` // connecting|live|error|closed - spliced into the class UNESCAPED (const)
	Title     string `json:"title"`
	Tip       string `json:"tip"`             // legacy RAW tooltip markup (bridge)
	TipS      *tipSt `json:"tipSt,omitempty"` // structured tipTopic("remote-library")
	HasNote   bool   `json:"hasNote,omitempty"`
	Note      string `json:"note,omitempty"`
	IsErr     bool   `json:"isErr,omitempty"`
	Err       string `json:"err,omitempty"`
	Reconnect string `json:"reconnect,omitempty"`
}

// libMirrorBody renders the remote Library surface and (re)opens the session. Every render
// recreates the iframe (lib-body innerHTML swap), so a fresh session is opened each time -
// the peer streams a fresh doc, which also resyncs any patches lost while the tab was away.
func (u *UI) libMirrorBody(target string) string {
	st := u.libMirrorState(target)
	if zigui.Available() {
		if h, ok := zigui.RenderLibMirror(stateJSON(st)); ok {
			return h
		}
	}
	return libMirrorBodyHTML(st)
}

// libMirrorState carries the session (re)open SIDE EFFECT and its order is load-bearing: the
// banner resolves AFTER status flips to connecting, exactly where the old renderer built it.
func (u *UI) libMirrorState(target string) libMirrorSt {
	st := u.mirror()
	if u.rui == nil {
		return libMirrorSt{NoLink: true, NoLinkMsg: i18n.T("library.mirror.noLink")}
	}
	sid := randToken()
	st.mu.Lock()
	prevTarget, prevSid := st.target, st.sid
	st.target, st.sid, st.status, st.errMsg = target, sid, mirrorConnecting, ""
	st.mu.Unlock()
	hub := u.rui
	if prevSid != "" {
		unregisterRuiProxy(prevSid)
	}
	u.bg(func() {
		if prevSid != "" {
			_ = hub.send(prevTarget, ruiMsg{T: ruiKindClose, SID: prevSid})
		}
		if err := hub.send(target, ruiMsg{T: ruiKindOpen, SID: sid}); err != nil {
			u.mirrorFail(sid, err.Error())
			return
		}
		time.AfterFunc(mirrorOpenTimeout, func() {
			st.mu.Lock()
			timedOut := st.sid == sid && st.status == mirrorConnecting
			st.mu.Unlock()
			if timedOut {
				u.mirrorFail(sid, i18n.T("library.mirror.timeout"))
			}
		})
	})
	return libMirrorSt{Banner: u.mirrorBannerState()}
}

// libMirrorBodyHTML is the pure mirror-surface renderer.
func libMirrorBodyHTML(st libMirrorSt) string {
	if st.NoLink {
		return `<div class=rp-card>` + emptyState(st.NoLinkMsg) + `</div>`
	}
	var b strings.Builder
	b.WriteString(`<div id=rmirror-banner>` + mirrorBannerHTMLOf(st.Banner) + `</div>`)
	b.WriteString(`<div class=rmirror-frame><iframe id=__rmirror title="remote library"></iframe></div>`)
	return b.String()
}

// mirrorBannerHTML is the status strip above the mirror (patched independently on state moves).
func (u *UI) mirrorBannerHTML() string {
	st := u.mirrorBannerState()
	if zigui.Available() {
		if h, ok := zigui.RenderLibMirrorBanner(stateJSON(st)); ok {
			return h
		}
	}
	return mirrorBannerHTMLOf(st)
}

// mirrorBannerState resolves the banner (peer name lookup + i18n + tooltip markup).
func (u *UI) mirrorBannerState() libMirrorBanSt {
	st := u.mirror()
	st.mu.Lock()
	target, status, errMsg := st.target, st.status, st.errMsg
	st.mu.Unlock()
	name := target
	for _, p := range u.controllablePeers() {
		if p.NodeID == target {
			name = p.Name
		}
	}
	b := libMirrorBanSt{
		Status: status,
		Title:  i18n.T("library.mirror.banner", i18n.A{"name": name}),
		TipS:   tipTopicSt("remote-library"),
	}
	switch status {
	case mirrorConnecting:
		b.HasNote, b.Note = true, i18n.T("library.mirror.connecting")
	case mirrorLive:
		b.HasNote, b.Note = true, i18n.T("library.mirror.audioNote")
	case mirrorError, mirrorClosed:
		msg := errMsg
		if msg == "" {
			msg = i18n.T("library.mirror.closed")
		}
		b.IsErr, b.Err, b.Reconnect = true, msg, i18n.T("library.mirror.reconnect")
	}
	return b
}

// mirrorBannerHTMLOf is the pure banner renderer.
func mirrorBannerHTMLOf(st libMirrorBanSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rmirror-bar rmirror-` + st.Status + `">`)
	b.WriteString(`<span class=rmirror-dot></span><span class=rmirror-title>` +
		html.EscapeString(st.Title) + `</span>`)
	b.WriteString(tipOr(st.TipS, st.Tip))
	if st.HasNote {
		b.WriteString(`<span class=rmirror-note>` + html.EscapeString(st.Note) + `</span>`)
	}
	if st.IsErr {
		b.WriteString(`<span class="rmirror-note rmirror-err">` + html.EscapeString(st.Err) + `</span>`)
		b.WriteString(btn(st.Reconnect, "outline", "rmirror-reconnect", ""))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func (u *UI) mirrorPatchBanner() {
	u.eval("window.__patch('rmirror-banner'," + jsQuote(u.mirrorBannerHTML()) + ")")
}

// mirrorFail marks the session errored (if sid still current) and repaints the banner.
func (u *UI) mirrorFail(sid, msg string) {
	st := u.mirror()
	st.mu.Lock()
	if st.sid != sid {
		st.mu.Unlock()
		return
	}
	st.status, st.errMsg = mirrorError, msg
	st.mu.Unlock()
	u.mirrorPatchBanner()
}

// ── inbound (hub mirror sink) ───────────────────────────────────────────────────

// onMirrorMsg receives complete host→controller messages; stale peer/sid frames are dropped.
func (u *UI) onMirrorMsg(peer string, m ruiMsg) {
	st := u.mirror()
	st.mu.Lock()
	target, sid := st.target, st.sid
	st.mu.Unlock()
	if peer != target || m.SID != sid {
		return
	}
	switch m.T {
	case ruiKindDoc:
		st.mu.Lock()
		st.status, st.errMsg, st.docSize = mirrorLive, "", len(m.Data)
		st.mu.Unlock()
		u.mirrorRegisterProxy(peer, m.SID)
		u.mirrorApplyDoc(m.Data)
		u.mirrorPatchBanner()
	case ruiKindEval:
		u.mirrorApplyEval(m.Data)
	case ruiKindClosed:
		st.mu.Lock()
		if m.Data == "" {
			st.status = mirrorClosed
		} else {
			st.status, st.errMsg = mirrorError, m.Data
		}
		st.mu.Unlock()
		u.mirrorPatchBanner()
	case ruiKindFetchRes:
		u.mirrorFetchRes(m)
	}
}

// mirrorApplyDoc loads the peer's document into the iframe (bridge injected) and installs the
// parent-side forward/post shims. Evals arriving before the iframe finished loading buffer on
// the element (cap 256, drop-newest; the peer's next patch of a fragment repaints it).
func (u *UI) mirrorApplyDoc(doc string) {
	srcdoc := injectMirrorBridge(u.mirrorRewriteMediaIn(doc))
	js := `window.__rmirrorPost=function(p){if(window.rave)window.rave(JSON.stringify({act:'rmirror-post',form:p}))};` +
		`window.__rmirrorFwd=function(js){var f=document.getElementById('__rmirror');if(!f)return;var w=f.contentWindow;` +
		`if(w&&w.__rx){w.__rx(js);}else{f.__pend=f.__pend||[];if(f.__pend.length<256)f.__pend.push(js);}};` +
		`var f=document.getElementById('__rmirror');if(f){` +
		`f.onload=function(){var w=f.contentWindow;var p=f.__pend||[];f.__pend=[];if(w&&w.__rx){for(var i=0;i<p.length;i++){w.__rx(p[i]);}}};` +
		`f.srcdoc=` + jsQuote(srcdoc) + `;}`
	u.eval(js)
}

// mirrorApplyEval forwards one host eval batch into the iframe (FIFO through the bounded eval
// queue; a dropped batch self-heals on the fragment's next patch).
func (u *UI) mirrorApplyEval(js string) {
	u.eval("window.__rmirrorFwd&&window.__rmirrorFwd(" + jsQuote(u.mirrorRewriteMediaIn(js)) + ")")
}

// injectMirrorBridge splices the transport runtime into the peer's document: window.rave
// forwards page input to the parent (→ peer), and __rx executes the peer's render stream.
// Security: __rx eval-executes ONLY the paired host's Go-generated JS, delivered over the
// Ed25519-authenticated, per-frame-MAC'd peer link (same trust contract as the local shell's
// eval binding, ui.go) - never third-party or user-typed input.
func injectMirrorBridge(doc string) string {
	bridge := `<script>window.rave=function(p){try{parent.__rmirrorPost(p)}catch(e){}};window.__rave_evalResult=function(){};</script>` +
		"<script>" + runtimeJS + "</script>" +
		`<script>window.__rx=function(js){try{(0,eval)(js)}catch(e){}};</script>`
	if i := strings.LastIndex(doc, "</body>"); i >= 0 {
		return doc[:i] + bridge + doc[i:]
	}
	return doc + bridge
}

// ── input + lifecycle ───────────────────────────────────────────────────────────

// mirrorForwardAct relays the mirrored page's input payload to the peer (executes there).
// Exception: the cue-prep opens never forward - `ce-open:<path>` (#89), `ce-open-pl:<id>` +
// `ce-open-dir:<dir>` (#90) launch the LOCAL cue editor on cached copies of the peer's
// files (library_remotecue.go). Bare ce-open-dir (older peer render) still forwards.
func (u *UI) mirrorForwardAct(payload string) {
	if payload == "" || u.rui == nil {
		return
	}
	st := u.mirror()
	st.mu.Lock()
	target, sid, status := st.target, st.sid, st.status
	st.mu.Unlock()
	if target == "" || (status != mirrorLive && status != mirrorConnecting) {
		return
	}
	if kind, arg, ok := rceIntercept(payload); ok {
		switch kind {
		case "track":
			u.rceOpen(target, arg)
		case "pl":
			u.rceOpenPlaylist(target, atoi64(arg))
		case "dir":
			u.rceOpenDir(target, arg)
		}
		return
	}
	hub := u.rui
	u.bg(func() { _ = hub.send(target, ruiMsg{T: ruiKindAct, SID: sid, Data: payload}) })
}

// mirrorShutdown ends the current session (target switch / UI stop). Best-effort close frame.
func (u *UI) mirrorShutdown() {
	if u.rui == nil {
		return
	}
	st := u.mirror()
	st.mu.Lock()
	target, sid := st.target, st.sid
	st.target, st.sid, st.status, st.errMsg = "", "", "", ""
	st.mu.Unlock()
	if target == "" || sid == "" {
		return
	}
	unregisterRuiProxy(sid)
	hub := u.rui
	u.bg(func() { _ = hub.send(target, ruiMsg{T: ruiKindClose, SID: sid}) })
}

// mirrorPeerState (hub peer-state hook): degrade the banner when the mirrored peer drops.
func (u *UI) mirrorPeerState(connected map[string]bool) {
	st := u.mirror()
	st.mu.Lock()
	lost := st.target != "" && !connected[st.target] &&
		(st.status == mirrorLive || st.status == mirrorConnecting)
	if lost {
		st.status, st.errMsg = mirrorError, i18n.T("library.mirror.disconnected")
	}
	st.mu.Unlock()
	if lost {
		u.mirrorPatchBanner()
	}
}

// mirrorRegisterProxy binds the session to the local loopback media proxy: /rmt/<sid>/…
// fetches the peer's token-guarded media bytes over the link (remoteui_media.go).
func (u *UI) mirrorRegisterProxy(peer, sid string) {
	hub := u.rui
	if hub == nil || u.mpProxyPort() == 0 {
		return
	}
	registerRuiProxy(sid, &ruiProxy{
		fetch: func(path string, off int64, ln int) (ruiMsg, error) {
			return hub.fetchRemote(peer, sid, path, off, ln)
		},
		cache: map[string][]byte{}, cacheCT: map[string]string{},
	})
}

// mirrorRewriteMediaIn maps the peer's media placeholder to this side's loopback proxy so
// covers/thumbnails (and ranged media streams) resolve locally.
func (u *UI) mirrorRewriteMediaIn(payload string) string {
	if !strings.Contains(payload, ruiMediaPlaceholder) {
		return payload
	}
	st := u.mirror()
	st.mu.Lock()
	sid := st.sid
	st.mu.Unlock()
	port := u.mpProxyPort()
	if port == 0 || sid == "" {
		return payload
	}
	return strings.ReplaceAll(payload, ruiMediaPlaceholder, fmt.Sprintf("http://127.0.0.1:%d/rmt/%s/", port, sid))
}

// mirrorFetchRes routes a media-fetch reply to its blocked proxy request.
func (u *UI) mirrorFetchRes(m ruiMsg) {
	if u.rui != nil {
		u.rui.deliverFetch(m)
	}
}
