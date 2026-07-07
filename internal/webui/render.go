package webui

import (
	"encoding/base64"
	"fmt"
	"html"
	"sort"
	"strings"
	"sync"

	"rave.page/mate/internal/i18n"
)

// ── CSS (rave.page design system, inlined; no asset server) ──

var (
	cssOnce  sync.Once
	cssCache string
)

// buildCSS concatenates the design-system tokens + kit + this app's layout into one stylesheet,
// inlining the Orbitron font as a data: URI so the document is fully self-contained.
func buildCSS() string {
	cssOnce.Do(func() {
		colors, _ := assetsFS.ReadFile("assets/ds/colors_and_type.css")
		styles, _ := assetsFS.ReadFile("assets/ds/styles.css")
		app, _ := assetsFS.ReadFile("assets/app.css")
		font, _ := assetsFS.ReadFile("assets/ds/fonts/Orbitron-VariableFont_wght.ttf")

		c := string(colors)
		if len(font) > 0 {
			uri := "url('data:font/ttf;base64," + base64.StdEncoding.EncodeToString(font) + "') format('truetype-variations')"
			c = strings.Replace(c, "url('fonts/Orbitron-VariableFont_wght.ttf') format('truetype-variations')", uri, 1)
		}
		s := strings.Replace(string(styles), `@import url("./colors_and_type.css");`, "", 1)
		comp, _ := assetsFS.ReadFile("assets/components.css")

		// per-tab CSS (assets/tabs/*.css) - lets each tab renderer own its styles without
		// colliding on app.css. Concatenated in name order for determinism.
		var extra strings.Builder
		if ents, err := assetsFS.ReadDir("assets/tabs"); err == nil {
			names := make([]string, 0, len(ents))
			for _, e := range ents {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".css") {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			for _, n := range names {
				if d, err := assetsFS.ReadFile("assets/tabs/" + n); err == nil {
					extra.Write(d)
					extra.WriteByte('\n')
				}
			}
		}
		cssCache = c + "\n" + s + "\n" + string(app) + "\n" + string(comp) + "\n" + extra.String()
	})
	return cssCache
}

// ── tabs (mirror the Fyne buildTabItems gating) ──

type tabDef struct {
	id, label string
	enabled   bool
}

func (u *UI) tabs() []tabDef {
	f := u.svc.Cfg.Features
	// labels are localized (i18n.T("tab.<id>")); ids stay stable (ctl + dispatch key on id).
	t := func(id string) string { return i18n.T("tab." + id) }
	return []tabDef{
		{"live", t("live"), true},
		{"overlays", t("overlays"), true},
		{"publish", t("publish"), u.svc.Recorder != nil},
		{"library", t("library"), f.Library.Enabled},
		{"editor", t("editor"), f.MediaEditor.Enabled},
		{"automations", t("automations"), u.svc.Automations != nil},
		{"peers", t("peers"), f.Peers.Enabled},
		{"twitch", t("twitch"), f.Twitch.Enabled},
		{"vrchat", t("vrchat"), f.VRChat.Enabled},
		{"motion", t("motion"), u.svc.VRCTools != nil || u.svc.VROverlay != nil},
		{"worlds", t("worlds"), f.WorldSync.Enabled},
		{"appgroups", t("appgroups"), f.AppGroups.Enabled},
		{"logs", t("logs"), true},
		{"settings", t("settings"), true},
	}
}

// tabLabels returns the enabled tab labels (for ctl SelectTab's available-names return).
func (u *UI) tabLabels() []string {
	var out []string
	for _, t := range u.tabs() {
		if t.enabled {
			out = append(out, t.label)
		}
	}
	return out
}

// ── document + fragments (all HTML rendered in Go) ──

func (u *UI) shellHTML() string {
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=en><head><meta charset=utf-8>")
	b.WriteString(`<meta name=viewport content="width=device-width,initial-scale=1">`)
	b.WriteString("<title>rave-mate</title><style>")
	b.WriteString(buildCSS())
	b.WriteString("</style></head><body>")
	b.WriteString(`<nav id=nav>`)
	b.WriteString(u.navHTML())
	b.WriteString(`</nav><main id=main>`)
	b.WriteString(u.mainHTML())
	b.WriteString(`</main><div id=__modal></div></body></html>`)
	return b.String()
}

func (u *UI) navHTML() string {
	return `<div class=brand>rave·<b>mate</b></div><div id=nav-list>` + u.navListHTML() + `</div>`
}

func (u *UI) navListHTML() string {
	active := u.activeTab()
	var b strings.Builder
	for _, t := range u.tabs() {
		if !t.enabled {
			continue
		}
		cls := ""
		if t.id == active {
			cls = " active"
		}
		fmt.Fprintf(&b, `<a class="%s" data-act="tab" data-val="%s" title=%s>%s<span>%s</span></a>`,
			strings.TrimSpace(cls), t.id, attrQ(i18n.T("navtitle."+t.id)), navIconSVG(t.id), html.EscapeString(t.label))
	}
	return b.String()
}

// per-tab hover help (title-attr) is localized via i18n.T("navtitle.<id>") - en.json is the
// source of truth (title-attr stopgap until a tooltip primitive exists).

// navIcons: per-tab inline stroke icons (lucide-style, 24×24 grid), keyed by tab id.
// stroke=currentColor inherits the nav link muted/hover/active colors. Keep every
// attribute value quoted (see graph.go - unquoted values before /> break parsing).
var navIcons = map[string]string{
	"live":        `<circle cx="12" cy="12" r="2"/><path d="M4.9 19.1C1 15.2 1 8.8 4.9 4.9"/><path d="M7.8 16.2c-2.3-2.3-2.3-6.1 0-8.5"/><path d="M16.2 7.8c2.3 2.3 2.3 6.1 0 8.5"/><path d="M19.1 4.9C23 8.8 23 15.2 19.1 19.1"/>`,
	"overlays":    `<path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z"/><path d="m22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65"/><path d="m22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65"/>`,
	"publish":     `<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/>`,
	"library":     `<path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/>`,
	"editor":      `<path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"/><path d="m15 5 4 4"/>`,
	"automations": `<polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/>`,
	"peers":       `<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>`,
	"twitch":      `<path d="M21 2H3v16h5v4l4-4h5l4-4V2z"/><path d="M11 11V7"/><path d="M16 11V7"/>`,
	"vrchat":      `<path d="M3 8a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v6a2 2 0 0 1-2 2h-3.6a2 2 0 0 1-1.6-.8l-.8-1.1a1.25 1.25 0 0 0-2 0l-.8 1.1a2 2 0 0 1-1.6.8H5a2 2 0 0 1-2-2Z"/><circle cx="8" cy="11" r="1"/><circle cx="16" cy="11" r="1"/>`,
	"motion":      `<circle cx="12" cy="5" r="2"/><path d="M12 7v5"/><path d="m8 21 4-9 4 9"/><path d="M7 10.5 12 12l5-1.5"/>`,
	"worlds":      `<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>`,
	"appgroups":   `<rect width="7" height="7" x="3" y="3" rx="1"/><rect width="7" height="7" x="14" y="3" rx="1"/><rect width="7" height="7" x="14" y="14" rx="1"/><rect width="7" height="7" x="3" y="14" rx="1"/>`,
	"logs":        `<polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/>`,
	"settings":    `<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>`,
}

// navIconSVG wraps a tab's icon body in the shared <svg> chrome ("" if no icon).
func navIconSVG(id string) string {
	body, ok := navIcons[id]
	if !ok {
		return ""
	}
	return `<svg class="nav-ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` + body + `</svg>`
}

func (u *UI) mainHTML() string {
	switch u.activeTab() {
	case "live":
		return u.renderLive()
	case "logs":
		return u.renderLogs()
	case "settings":
		return u.renderSettings()
	case "overlays":
		return u.renderOverlays()
	case "peers":
		return u.renderPeers()
	case "publish":
		return u.renderPublish()
	case "automations":
		return u.renderAutomations()
	case "appgroups":
		return u.renderAppGroups()
	case "vrchat":
		return u.renderVRChat()
	case "motion":
		u.moEnsure()
		return u.renderMotion()
	case "worlds":
		return u.renderWorlds()
	case "twitch":
		return u.renderTwitch()
	case "library":
		return u.renderLibrary()
	case "editor":
		return u.renderEditor()
	default:
		return u.renderPlaceholder(u.activeTab())
	}
}

func (u *UI) renderPlaceholder(id string) string {
	label := id
	for _, t := range u.tabs() {
		if t.id == id {
			label = t.label
		}
	}
	return fmt.Sprintf(`<h1 class=page-title>%s</h1><p class=page-sub>%s</p>`+
		`<div class=placeholder>%s</div>`,
		html.EscapeString(label), html.EscapeString(i18n.T("placeholder.porting")),
		html.EscapeString(i18n.T("placeholder.comingSoon", i18n.A{"name": label})))
}

func trimLower(s string) string  { return strings.ToLower(strings.TrimSpace(s)) }
func htmlEscape(s string) string { return html.EscapeString(s) }
