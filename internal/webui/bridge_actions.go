package webui

import (
	"html"
	"strconv"
	"strings"

	"rave.page/mate/internal/authz"
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
		u.bg(func() {
			if err := g.ConfirmEnrolment(code); err != nil {
				u.toast(i18n.T("settings.toast.bridgeCodeRejected"))
				return
			}
			u.mu.Lock()
			u.bridgeURI, u.bridgeSecret = "", "" // stop showing the secret the moment it's confirmed
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
			u.bridgeURI, u.bridgeSecret = "", ""
			u.mu.Unlock()
			u.toast(i18n.T("settings.toast.bridgeUnenrolled"))
			u.patchMain()
		})
	})

	// Dismiss the enrolment panel without confirming (the secret stays pending, unarmed).
	onExact("bridge-enrol-cancel", func(u *UI, _ actMsg) {
		u.mu.Lock()
		u.bridgeURI, u.bridgeSecret = "", ""
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

// bridgeCardBody renders the account-bridge settings card: state, the Local Studio sub-toggle,
// authenticator enrolment, and the trusted-session list.
func (u *UI) bridgeCardBody() string {
	g := u.svc.AuthGate
	f := &u.svc.Cfg.Features.AccountBridge
	var b strings.Builder

	// Live connection state.
	if u.svc.Bridge != nil && f.Enabled {
		s := u.svc.Bridge.State()
		switch {
		case s.Error != "":
			b.WriteString(statusRow("warn", i18n.T("settings.status.bridge.error"), s.Error))
		case s.Registered:
			b.WriteString(statusRow("go", i18n.T("settings.status.bridge.online"),
				i18n.T("settings.status.bridge.devices", i18n.A{"devices": strconv.Itoa(s.Devices), "links": strconv.Itoa(s.Links)})))
		default:
			b.WriteString(statusRow("idle", i18n.T("settings.status.bridge.connecting"), ""))
		}
	}

	// Serving the Local Studio channel over the relay is a separate, deliberate step: it lets a
	// browser ANYWHERE drive this machine, not only one on this box.
	b.WriteString(toggleRowTip(i18n.T("settings.body.bridge.localStudio"), "set:bridge-studio",
		f.LocalStudio, tipTopic("bridge-local-studio")))

	// ── the access gate ──────────────────────────────────────────────────────
	if g == nil {
		return b.String()
	}
	b.WriteString(section(i18n.T("settings.body.bridge.gateTitle"), u.bridgeGateBody(g)))
	return b.String()
}

// bridgeGateBody renders enrolment + the trusted sessions.
func (u *UI) bridgeGateBody(g *authz.Gate) string {
	var b strings.Builder

	u.mu.Lock()
	uri, secret := u.bridgeURI, u.bridgeSecret
	u.mu.Unlock()

	switch {
	case uri != "":
		// Pending enrolment: show the URI + secret ONCE and ask for a code back. Deliberately
		// plain text - there is no QR encoder in the tree and the supply-chain rule forbids
		// adding a dependency for one (tracked as a follow-up).
		b.WriteString(`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.bridge.enrolHelp")) + `</div>`)
		b.WriteString(`<div class="bridge-secret mono">` + html.EscapeString(secret) + `</div>`)
		b.WriteString(`<div class="bridge-uri mono">` + html.EscapeString(uri) + `</div>`)
		b.WriteString(`<form data-act=bridge-confirm>` +
			field(i18n.T("settings.body.bridge.codeLabel"), "", "", "text") +
			btnRow(
				btn(i18n.T("settings.body.bridge.confirm"), "primary", "bridge-confirm", ""),
				btn(i18n.T("common.cancel"), "ghost", "bridge-enrol-cancel", ""),
			) + `</form>`)
		b.WriteString(`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.bridge.burnNote")) + `</div>`)

	case g.Enrolled():
		b.WriteString(statusRow("go", i18n.T("settings.status.bridge.enrolled"), ""))
		if !g.Persistent() {
			// No OS secret store (macOS/Linux today): the secret is memory-only, so it dies with
			// the process. Say so plainly rather than let the user discover it after a restart.
			b.WriteString(statusRow("warn", i18n.T("settings.status.bridge.notPersisted"),
				i18n.T("settings.status.bridge.notPersistedHint")))
		}
		b.WriteString(btnRow(btn(i18n.T("settings.body.bridge.unenrol"), "destructive", "bridge-unenrol", "")))

	default:
		b.WriteString(`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.bridge.noAuthenticator")) + `</div>`)
		b.WriteString(btnRow(btn(i18n.T("settings.body.bridge.enrol"), "primary", "bridge-enrol", "")))
	}

	// ── trusted sessions ─────────────────────────────────────────────────────
	sessions := g.Sessions()
	b.WriteString(`<div class=set-sub>` + html.EscapeString(i18n.T("settings.body.bridge.sessionsTitle")) + `</div>`)
	if len(sessions) == 0 {
		b.WriteString(emptyState(i18n.T("settings.empty.bridgeSessions")))
		return b.String()
	}
	for _, s := range sessions {
		label := s.Label
		if strings.TrimSpace(label) == "" {
			label = shortID(s.PeerID)
		}
		sub := i18n.T("settings.body.bridge.sessionSub", i18n.A{
			"transport": string(s.Transport),
			"expires":   s.ExpiresAt.Local().Format("2006-01-02 15:04"),
		})
		b.WriteString(listRow(label, sub,
			btn(i18n.T("settings.body.bridge.revoke"), "destructive", "bridge-revoke:"+s.PeerID, "")))
	}
	b.WriteString(btnRow(btn(i18n.T("settings.body.bridge.revokeAll"), "outline", "bridge-revoke-all", "")))
	return b.String()
}
