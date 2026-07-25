package webui

import (
	"strconv"
	"strings"

	"rave.page/mate/internal/authz"
	"rave.page/mate/internal/bridge"
	"rave.page/mate/internal/i18n"
)

// Account-bridge surfaces: enrol an authenticator, serve the Local Studio channel over the
// relay, and manage the trusted sessions that skip the code next time.

func init() {
	// Enrol: mint a secret + show the otpauth URI. Nothing is armed until a code is confirmed -
	// a mis-scan must never lock the user out of their own instance.
	onExact("bridge-enrol", func(u *UI, _ actMsg) {
		g := u.svc.AuthGate
		if g == nil {
			return
		}
		u.bg(func() {
			uri, secret, err := g.BeginEnrolment()
			if err != nil {
				u.toast(i18n.T("settings.toast.bridgeEnrolFailed", i18n.A{"error": err.Error()}))
				return
			}
			// The URI and the secret are shown ONCE, to the user, and never logged.
			u.mu.Lock()
			u.bridgeURI, u.bridgeSecret = uri, secret
			u.mu.Unlock()
			u.patchMain()
		})
	})

	// Stash the code as it's typed. A settings re-render (live tick, toast, toggle) replaces
	// the input element, so relying on the DOM still holding the value at submit time is
	// fragile - keep the truth in Go.
	onExact("bridge-code", func(u *UI, m actMsg) {
		u.mu.Lock()
		u.bridgeCode = strings.TrimSpace(m.Val)
		u.mu.Unlock()
	})

	// Confirm the code typed back from the authenticator → arm the gate.
	onExact("bridge-confirm", func(u *UI, m actMsg) {
		g := u.svc.AuthGate
		if g == nil {
			return
		}
		code := strings.TrimSpace(parseForm(m.Form)["code"])
		if code == "" {
			code = strings.TrimSpace(m.Val)
		}
		if code == "" {
			u.mu.Lock()
			code = u.bridgeCode
			u.mu.Unlock()
		}
		u.bg(func() {
			if err := g.ConfirmEnrolment(code); err != nil {
				u.toast(i18n.T("settings.toast.bridgeCodeRejected"))
				return
			}
			u.mu.Lock()
			u.bridgeURI, u.bridgeSecret, u.bridgeCode = "", "", "" // stop showing the secret the moment it's confirmed
			u.mu.Unlock()
			u.toast(i18n.T("settings.toast.bridgeEnrolled"))
			u.patchMain()
		})
	})

	// Remove the authenticator. Trusted sessions SURVIVE - pulling the authenticator must not
	// lock the user out of devices they already paired.
	onExact("bridge-unenrol", func(u *UI, _ actMsg) {
		g := u.svc.AuthGate
		if g == nil {
			return
		}
		u.bg(func() {
			if err := g.Unenrol(); err != nil {
				u.toast(err.Error())
				return
			}
			u.mu.Lock()
			u.bridgeURI, u.bridgeSecret, u.bridgeCode = "", "", ""
			u.mu.Unlock()
			u.toast(i18n.T("settings.toast.bridgeUnenrolled"))
			u.patchMain()
		})
	})

	// Dismiss the enrolment panel without confirming (the secret stays pending, unarmed).
	onExact("bridge-enrol-cancel", func(u *UI, _ actMsg) {
		u.mu.Lock()
		u.bridgeURI, u.bridgeSecret, u.bridgeCode = "", "", ""
		u.mu.Unlock()
		u.patchMain()
	})

	// Revoke one trusted session → that device must present a code again.
	onPrefix("bridge-revoke:", func(u *UI, m actMsg) {
		g := u.svc.AuthGate
		if g == nil {
			return
		}
		peer := m.arg("bridge-revoke:")
		u.bg(func() {
			if err := g.Revoke(peer); err != nil {
				u.toast(err.Error())
				return
			}
			u.toast(i18n.T("settings.toast.bridgeRevoked"))
			u.patchMain()
		})
	})

	onExact("bridge-revoke-all", func(u *UI, _ actMsg) {
		g := u.svc.AuthGate
		if g == nil {
			return
		}
		u.bg(func() {
			if err := g.RevokeAll(); err != nil {
				u.toast(err.Error())
				return
			}
			u.toast(i18n.T("settings.toast.bridgeRevokedAll"))
			u.patchMain()
		})
	})
}

// bridgeBits are the impure facts the account-bridge card renders from (relay state + gate),
// lifted out so the state mapping needs no live authz.Gate.
type bridgeBits struct {
	HasState    bool // relay manager present AND the feature enabled
	State       bridge.State
	LocalStudio bool
	HasGate     bool
	URI, Secret string // pending enrolment (shown once)
	Enrolled    bool
	Persistent  bool // an OS secret store backs the enrolment
	Sessions    []authz.Session
}

// bridgeCardState resolves the account-bridge card: relay state, the Local Studio sub-toggle,
// authenticator enrolment and the trusted-session list. Pure renderer: bridgeCardHTML.
func (u *UI) bridgeCardState() bridgeSt {
	g := u.svc.AuthGate
	f := &u.svc.Cfg.Features.AccountBridge
	b := bridgeBits{LocalStudio: f.LocalStudio, HasGate: g != nil}
	if u.svc.Bridge != nil && f.Enabled {
		b.HasState, b.State = true, u.svc.Bridge.State()
	}
	if g != nil {
		u.mu.Lock()
		b.URI, b.Secret = u.bridgeURI, u.bridgeSecret
		u.mu.Unlock()
		if b.URI == "" { // pending enrolment wins - don't touch the store we don't render from
			b.Enrolled, b.Persistent = g.Enrolled(), g.Persistent()
		}
		b.Sessions = g.Sessions() // side effect: lazily reaps expired tokens (as before)
	}
	return bridgeCardStateOf(b)
}

// bridgeCardStateOf maps the gathered facts to render state.
func bridgeCardStateOf(b bridgeBits) bridgeSt {
	// Serving the Local Studio channel over the relay is a separate, deliberate step: it lets a
	// browser ANYWHERE drive this machine, not only one on this box.
	s := bridgeSt{
		Studio: newToggle(i18n.T("settings.body.bridge.localStudio"), "set:bridge-studio", b.LocalStudio),
		TipS:   tipTopicSt("bridge-local-studio"),
	}
	if b.HasState {
		switch st := b.State; {
		case st.SignedOut:
			// Being signed out is an ordinary state, not a fault - don't alarm the user.
			s.St = newStatus("idle", i18n.T("settings.status.bridge.signedOut"),
				i18n.T("settings.status.bridge.signedOutHint"))
		case st.Error != "":
			s.St = newStatus("warn", i18n.T("settings.status.bridge.error"), st.Error)
		case st.Registered:
			s.St = newStatus("go", i18n.T("settings.status.bridge.online"),
				i18n.T("settings.status.bridge.devices", i18n.A{"devices": strconv.Itoa(st.Devices), "links": strconv.Itoa(st.Links)}))
		default:
			s.St = newStatus("idle", i18n.T("settings.status.bridge.connecting"), "")
		}
	}
	// ── the access gate ──────────────────────────────────────────────────────
	if !b.HasGate {
		return s
	}
	s.HasGate, s.GateTitle = true, i18n.T("settings.body.bridge.gateTitle")
	s.Gate = bridgeGateState(b)
	return s
}

// bridgeGateState resolves enrolment + the trusted sessions.
func bridgeGateState(b bridgeBits) bridgeGateSt {
	g := bridgeGateSt{
		Rows: []uiStatus{}, Sessions: []bridgeSessSt{},
		SessionsTitle: i18n.T("settings.body.bridge.sessionsTitle"),
	}
	switch {
	case b.URI != "":
		// Pending enrolment: show the URI + secret ONCE and ask for a code back. Deliberately
		// plain text - there is no QR encoder in the tree and the supply-chain rule forbids
		// adding a dependency for one (tracked as a follow-up). The code input is hand-rolled
		// (raw named input + submit): the field() helper emits no name attribute, so parseForm
		// would see nothing.
		label := i18n.T("settings.body.bridge.codeLabel")
		g.Kind = "enrol"
		g.Help = i18n.T("settings.body.bridge.enrolHelp")
		g.Secret, g.URI = b.Secret, b.URI
		g.CodeLabel, g.CodeDL = label, strings.ToLower(label)
		g.Confirm = i18n.T("settings.body.bridge.confirm")
		g.Cancel = nbtn(i18n.T("common.cancel"), "ghost", "bridge-enrol-cancel", "")
		g.Burn = i18n.T("settings.body.bridge.burnNote")
	case b.Enrolled:
		g.Kind = "enrolled"
		g.Rows = append(g.Rows, newStatus("go", i18n.T("settings.status.bridge.enrolled"), ""))
		if !b.Persistent {
			// No OS secret store (macOS/Linux today): the secret is memory-only, so it dies with
			// the process. Say so plainly rather than let the user discover it after a restart.
			g.Rows = append(g.Rows, newStatus("warn", i18n.T("settings.status.bridge.notPersisted"),
				i18n.T("settings.status.bridge.notPersistedHint")))
		}
		g.Btn = nbtn(i18n.T("settings.body.bridge.unenrol"), "destructive", "bridge-unenrol", "")
	default:
		g.Kind = "none"
		g.Note = i18n.T("settings.body.bridge.noAuthenticator")
		g.Btn = nbtn(i18n.T("settings.body.bridge.enrol"), "primary", "bridge-enrol", "")
	}
	// ── trusted sessions ─────────────────────────────────────────────────────
	if len(b.Sessions) == 0 {
		g.Empty = i18n.T("settings.empty.bridgeSessions")
		return g
	}
	for _, s := range b.Sessions {
		label := s.Label
		if strings.TrimSpace(label) == "" {
			label = shortID(s.PeerID)
		}
		g.Sessions = append(g.Sessions, bridgeSessSt{
			Title: label,
			Sub: i18n.T("settings.body.bridge.sessionSub", i18n.A{
				"transport": string(s.Transport),
				"expires":   s.ExpiresAt.Local().Format("2006-01-02 15:04"),
			}),
			Revoke: nbtn(i18n.T("settings.body.bridge.revoke"), "destructive", "bridge-revoke:"+s.PeerID, ""),
		})
	}
	g.RevokeAll = nbtn(i18n.T("settings.body.bridge.revokeAll"), "outline", "bridge-revoke-all", "")
	return g
}
