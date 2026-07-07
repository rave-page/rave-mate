package webui

import (
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/twitch"
)

// Twitch tab actions: title-preset apply/manage dialogs + per-message moderation. All namespaced
// tw-*. Chat send + OBS stream/rec reuse the already-wired twitch-send / obs-stream: / obs-record:.

func init() {
	// ── title presets ──
	onPrefix("tw-apply:", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil || u.svc.Twitch == nil {
			return
		}
		idx, _ := strconv.Atoi(m.arg("tw-apply:"))
		if h := u.twitchApplyModal(idx); h != "" {
			u.openModal(h)
		}
	})
	onExact("tw-apply-do", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil || u.svc.Twitch == nil {
			u.closeModal()
			return
		}
		form := parseForm(m.Form)
		idx, _ := strconv.Atoi(form["__idx"])
		f := &u.svc.Cfg.Features.Twitch
		if idx < 0 || idx >= len(f.Presets) {
			u.closeModal()
			return
		}
		p := &f.Presets[idx]
		if p.Vars == nil {
			p.Vars = map[string]string{}
		}
		for _, v := range twitch.TemplateVars(p.Template) {
			p.Vars[v] = form[v]
		}
		p.GameName = form["__game"]
		u.saveCfg()
		preset := *p // copy before off-thread apply (avoid racing slice mutation)
		u.closeModal()
		u.toast(i18n.T("twitch.toast.updatingTitle"))
		u.bg(func() {
			ctx, cancel := u.actx()
			defer cancel()
			if err := u.svc.Twitch.ApplyTitlePreset(ctx, preset); err != nil {
				u.logErr("twitch title", err)
				u.toast(i18n.T("twitch.toast.titleUpdateFailed") + err.Error())
			} else {
				u.toast(i18n.T("twitch.toast.titleUpdated"))
			}
		})
	})
	onExact("tw-presets", func(u *UI, _ actMsg) {
		if u.svc.Cfg != nil {
			u.openModal(u.twitchManageModal())
		}
	})
	onExact("tw-preset-add", func(u *UI, _ actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		f := &u.svc.Cfg.Features.Twitch
		f.Presets = append(f.Presets, config.TitlePreset{Name: "New preset", Template: "{genre} set @ {club}"})
		u.saveCfg()
		u.openModal(u.twitchEditModal(len(f.Presets) - 1))
		u.patchTwitchPresets()
	})
	onPrefix("tw-preset-edit:", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		idx, _ := strconv.Atoi(m.arg("tw-preset-edit:"))
		if h := u.twitchEditModal(idx); h != "" {
			u.openModal(h)
		}
	})
	onExact("tw-preset-save", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			u.closeModal()
			return
		}
		form := parseForm(m.Form)
		idx, _ := strconv.Atoi(form["__idx"])
		f := &u.svc.Cfg.Features.Twitch
		if idx < 0 || idx >= len(f.Presets) {
			u.closeModal()
			return
		}
		f.Presets[idx].Name = strings.TrimSpace(form["name"])
		f.Presets[idx].Template = form["template"]
		f.Presets[idx].GameName = form["game"]
		u.saveCfg()
		u.openModal(u.twitchManageModal())
		u.patchTwitchPresets()
	})
	onPrefix("tw-preset-del:", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		idx, _ := strconv.Atoi(m.arg("tw-preset-del:"))
		f := &u.svc.Cfg.Features.Twitch
		if idx < 0 || idx >= len(f.Presets) {
			return
		}
		f.Presets = append(f.Presets[:idx], f.Presets[idx+1:]...)
		u.saveCfg()
		u.openModal(u.twitchManageModal())
		u.patchTwitchPresets()
	})

	// ── per-message moderation ──
	onExact("tw-mod", func(u *UI, m actMsg) {
		parts := strings.SplitN(m.Val, "|", 3)
		mid, uid, name := "", "", ""
		if len(parts) > 0 {
			mid = parts[0]
		}
		if len(parts) > 1 {
			uid = parts[1]
		}
		if len(parts) > 2 {
			name = parts[2]
		}
		u.openModal(twitchModModal(mid, uid, name))
	})
	onExact("tw-mod-do", func(u *UI, m actMsg) {
		if u.svc.Twitch == nil {
			u.closeModal()
			return
		}
		action, arg, _ := strings.Cut(m.Val, "|")
		var cmd twitch.ModerateCmd
		switch action {
		case "delete":
			cmd = twitch.ModerateCmd{Action: "delete", MessageID: arg}
		case "timeout":
			cmd = twitch.ModerateCmd{Action: "timeout", UserID: arg, Duration: 600, Reason: "timeout"}
		case "ban":
			cmd = twitch.ModerateCmd{Action: "ban", UserID: arg, Reason: "banned"}
		default:
			u.closeModal()
			return
		}
		u.closeModal()
		u.toast(i18n.T("twitch.toast.applyingMod"))
		u.bg(func() {
			ctx, cancel := u.actx()
			defer cancel()
			if err := u.svc.Twitch.Moderate(ctx, cmd); err != nil {
				u.logErr("twitch moderate", err)
				u.toast(i18n.T("twitch.toast.modFailed") + err.Error())
			} else {
				u.toast(i18n.T("twitch.toast.done"))
			}
		})
	})

	// ~1 Hz feed tail-follow + OBS cockpit/viewer refresh while the tab is showing.
	onLiveTick("twitch", func(u *UI) {
		u.eval("var lv=document.getElementById('twitch-feed');if(lv){var ab=lv.scrollHeight-lv.scrollTop-lv.clientHeight<40;lv.innerHTML=" +
			jsQuote(u.twitchFeedHTML()) + ";if(ab)lv.scrollTop=lv.scrollHeight;}")
		if u.svc.OBSControl != nil {
			u.eval("window.__patch('twitch-obs'," + jsQuote(u.twitchObsHTML()) + ")")
		}
	})
}

func (u *UI) patchTwitchPresets() {
	if u.svc.Cfg != nil {
		u.eval("window.__patch('twitch-presets'," + jsQuote(u.twitchPresetsHTML()) + ")")
	}
}

// ── modal builders ──

// twitchApplyModal: fill the preset's {variables} + optional category, then apply the title.
func (u *UI) twitchApplyModal(idx int) string {
	f := &u.svc.Cfg.Features.Twitch
	if idx < 0 || idx >= len(f.Presets) {
		return ""
	}
	p := f.Presets[idx]
	var body strings.Builder
	body.WriteString(`<form data-act=tw-apply-do class=tw-form>`)
	body.WriteString(`<input type=hidden name=__idx value=` + strconv.Itoa(idx) + `>`)
	body.WriteString(`<div class=tw-tmpl>` + htmlEscape(p.Template) + `</div>`)
	for _, v := range twitch.TemplateVars(p.Template) {
		body.WriteString(twFormInput(v, v, p.Vars[v], ""))
	}
	body.WriteString(twFormInput(i18n.T("twitch.label.category"), "__game", p.GameName, i18n.T("twitch.label.categoryPlaceholder")))
	body.WriteString(`<button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("twitch.label.setTitle") + `</button></form>`)
	return modal(i18n.T("twitch.label.applyPreset", i18n.A{"name": p.Name}), body.String(), "")
}

// twitchManageModal: list presets with edit/delete + an add button.
func (u *UI) twitchManageModal() string {
	f := &u.svc.Cfg.Features.Twitch
	var list strings.Builder
	for i, p := range f.Presets {
		list.WriteString(itemRow(p.Name, p.Template,
			btn(i18n.T("twitch.label.edit"), "outline", "tw-preset-edit:"+strconv.Itoa(i), ""),
			btn(i18n.T("common.delete"), "destructive", "tw-preset-del:"+strconv.Itoa(i), "")))
	}
	if len(f.Presets) == 0 {
		list.WriteString(emptyState(i18n.T("twitch.empty.noPresets")))
	}
	footer := btn(i18n.T("twitch.label.addPreset"), "primary", "tw-preset-add", "") + btn(i18n.T("common.close"), "outline", "modal-close", "")
	return modal(i18n.T("twitch.label.titlePresets"), list.String(), footer)
}

// twitchEditModal: edit one preset's name/template/category.
func (u *UI) twitchEditModal(idx int) string {
	f := &u.svc.Cfg.Features.Twitch
	if idx < 0 || idx >= len(f.Presets) {
		return ""
	}
	p := f.Presets[idx]
	var body strings.Builder
	body.WriteString(`<form data-act=tw-preset-save class=tw-form>`)
	body.WriteString(`<input type=hidden name=__idx value=` + strconv.Itoa(idx) + `>`)
	body.WriteString(twFormInput(i18n.T("twitch.label.name"), "name", p.Name, ""))
	body.WriteString(twFormInput(i18n.T("twitch.label.template"), "template", p.Template, i18n.T("twitch.label.templatePlaceholder")))
	body.WriteString(twFormInput(i18n.T("twitch.label.category"), "game", p.GameName, i18n.T("twitch.label.categoryPlaceholder")))
	body.WriteString(`<p class=tw-hint>` + i18n.T("twitch.label.templateHint") + `</p>`)
	body.WriteString(`<button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("common.save") + `</button></form>`)
	return modal(i18n.T("twitch.label.editPreset"), body.String(), "")
}

// twitchModModal: delete / timeout / ban a chat message's author.
func twitchModModal(messageID, userID, name string) string {
	var body strings.Builder
	body.WriteString(`<p class=tw-hint>` + i18n.T("twitch.label.moderateUser", i18n.A{"name": htmlEscape(name)}) + `</p><div class=tw-modacts>`)
	if messageID != "" {
		body.WriteString(btn(i18n.T("twitch.label.deleteMessage"), "warn", "tw-mod-do", "delete|"+messageID))
	}
	if userID != "" {
		body.WriteString(btn(i18n.T("twitch.label.timeout10"), "outline", "tw-mod-do", "timeout|"+userID))
		body.WriteString(btn(i18n.T("twitch.label.banUser"), "destructive", "tw-mod-do", "ban|"+userID))
	}
	body.WriteString(`</div>`)
	return modal(i18n.T("twitch.label.moderation"), body.String(), "")
}

// twFormInput renders a labelled plain text input inside a modal form (no data-act - the form
// submits the whole field set).
func twFormInput(label, name, value, placeholder string) string {
	return `<label class=field><span class=field-label>` + htmlEscape(label) + `</span>` +
		`<input class=field-input name="` + htmlEscape(name) + `" value="` + htmlEscape(value) +
		`" placeholder="` + htmlEscape(placeholder) + `" autocomplete=off></label>`
}
