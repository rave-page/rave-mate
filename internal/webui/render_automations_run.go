package webui

import (
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/i18n"
)

// Automations "Run now" DIALOG state + pure renderer (wave-4 dialog sweep B). The impure half
// stays in automations_runnow.go (the modal's own chain copy under s.mu, run-in-flight tracking,
// i18n, tipTopic markup); this renderer is mirrored in native/zigui/src/dialogs_b.zig
// (gate: zigui_golden_dialogs_b_test.go).
//
// Erases is explicit (autoChainDeletes resolved Go-side) and gates BOTH the acknowledgement block
// and the footer wording - never inferred from an empty string.

// arFootSt is the resolved footer. Gated = the Run button is disabled with Why in its title
// (btnGated: name the missing precondition, never hide the control).
type arFootSt struct {
	Gated   bool   `json:"gated,omitempty"`
	Label   string `json:"label"`
	Why     string `json:"why,omitempty"`
	Variant string `json:"variant,omitempty"` // live button variant (primary / destructive)
	Cancel  string `json:"cancel"`
}

// arBadge is one match-condition badge (state, not a control) shown in rules-first mode.
type arBadge struct {
	Label   string `json:"label"`
	Variant string `json:"variant"`
}

// arPrevRow is one preview file the sweep would act on (name + human size + age meta).
type arPrevRow struct {
	Name string `json:"name"`
	Size string `json:"size"`
	Meta string `json:"meta"`
}

// arModalSt is the run-now dialog. Fields 1-16 (Title..Foot) are the original single-file dialog;
// the rest drive the rules-first DEFAULT (condition badges + live match preview + empty/why state)
// and the mode switch to the secondary single-file flow, plus the coordinator conflict line.
type arModalSt struct {
	Title        string     `json:"title"`
	HasErr       bool       `json:"hasErr,omitempty"`
	Err          string     `json:"err,omitempty"`
	Auto         uiKV       `json:"auto"`
	Watch        uiKV       `json:"watch"`
	Chain        uiKV       `json:"chain"`
	IgnoresMatch string     `json:"ignoresMatch"`
	File         dlgFieldSt `json:"file"`
	Browse       uiBtn      `json:"browse"`
	Erases       bool       `json:"erases,omitempty"` // the chain ends by deleting the file
	DeleteWarn   string     `json:"deleteWarn,omitempty"`
	DeleteScope  string     `json:"deleteScope,omitempty"`
	DeleteTip    string     `json:"deleteTip,omitempty"`   // legacy RAW tooltip markup (bridge)
	DeleteTipS   *tipSt     `json:"deleteTipSt,omitempty"` // structured tooltip - wins over DeleteTip
	Ack          uiToggle   `json:"ack,omitempty"`
	Foot         arFootSt   `json:"foot"`

	// ── rules-first default + mode switch ──
	SpecificFile bool        `json:"specificFile,omitempty"` // secondary single-file mode active
	ModeToggle   uiToggle    `json:"modeToggle"`             // "Run on a specific file instead"
	CondsLabel   string      `json:"condsLabel,omitempty"`   // "Matches"
	CondsAny     string      `json:"condsAny,omitempty"`     // shown when Conds empty ("any file …")
	Conds        []arBadge   `json:"conds,omitempty"`        // active match conditions as badges
	Files        []arPrevRow `json:"files,omitempty"`        // bounded preview of matching files
	More         string      `json:"more,omitempty"`         // "and N more" ("" = none elided)
	TotalLine    string      `json:"totalLine,omitempty"`    // "3 files · 4.2 MB"
	Empty        bool        `json:"empty,omitempty"`        // rules mode, nothing matches now
	EmptyTitle   string      `json:"emptyTitle,omitempty"`
	EmptyHints   []string    `json:"emptyHints,omitempty"`   // why nothing matched (age/pattern/ext/size)
	Conflict     bool        `json:"conflict,omitempty"`     // a running automation will make this wait
	ConflictText string      `json:"conflictText,omitempty"` // "Will wait — <auto> is transcoding <file>."
}

// arModalHTMLOf is the pure run-now renderer. Default = rules-first (conditions + live preview);
// the mode switch reveals the secondary single-file flow (which keeps the ignores-match copy).
func arModalHTMLOf(st arModalSt) string {
	var b strings.Builder
	if st.HasErr {
		b.WriteString(`<div class=ae-err>` + hint("bad", st.Err) + `</div>`)
	}
	b.WriteString(st.Auto.html())
	b.WriteString(st.Watch.html())
	b.WriteString(st.Chain.html())
	// The coordinator's verdict BEFORE the primary: this run will queue behind another.
	if st.Conflict {
		b.WriteString(hint("warn", st.ConflictText))
	}
	b.WriteString(st.ModeToggle.html())
	if st.SpecificFile {
		// Secondary: the rule that does NOT apply here, stated before the file is chosen.
		b.WriteString(hint("info", st.IgnoresMatch))
		b.WriteString(`<div class=lib-toolbar>` + st.File.html() + st.Browse.html() + `</div>`)
	} else {
		// Default: the automation's own conditions as badges, then what they match right now.
		b.WriteString(`<div class=np-meta><span class=np-artist>` + htmlEscape(st.CondsLabel) + `</span>`)
		if len(st.Conds) == 0 {
			b.WriteString(` ` + htmlEscape(st.CondsAny))
		} else {
			for _, c := range st.Conds {
				b.WriteString(` ` + badge(c.Label, c.Variant))
			}
		}
		b.WriteString(`</div>`)
		if st.Empty {
			b.WriteString(emptyState(st.EmptyTitle))
			for _, h := range st.EmptyHints {
				b.WriteString(hint("info", h))
			}
		} else {
			b.WriteString(`<div class="rp-card">`)
			for _, f := range st.Files {
				b.WriteString(`<div class=kv><span class=kv-k>` + htmlEscape(f.Name) +
					` <span class=np-artist>` + htmlEscape(f.Meta) + `</span></span>` +
					`<span class=kv-v>` + htmlEscape(f.Size) + `</span></div>`)
			}
			if st.More != "" {
				b.WriteString(`<div class=kv><span class=kv-k><span class=np-artist>` + htmlEscape(st.More) + `</span></span></div>`)
			}
			b.WriteString(`</div>`)
			b.WriteString(`<div class=np-meta>` + htmlEscape(st.TotalLine) + `</div>`)
		}
	}
	if st.Erases {
		// The chain ends by erasing the file(s). Make the user say it out loud.
		b.WriteString(hint("bad", st.DeleteWarn))
		b.WriteString(`<div class=pb-hint>` + htmlEscape(st.DeleteScope) + tipOr(st.DeleteTipS, st.DeleteTip) + `</div>`)
		b.WriteString(st.Ack.html())
	}
	return modal(st.Title, b.String(), arFooterHTMLOf(st.Foot))
}

// arFooterHTMLOf renders the gated-or-live Run button plus Cancel.
func arFooterHTMLOf(f arFootSt) string {
	cancel := btn(f.Cancel, "ghost", "modal-close", "")
	if f.Gated {
		return btnRow(btnGated(f.Label, f.Why), cancel)
	}
	return btnRow(btn(f.Label, f.Variant, "auto-run-go", ""), cancel)
}

// arCondBadges renders the automation's active Match conditions as state badges (single info hue).
// An empty Match yields no badges - the caller shows the "any file" fact as plain text instead.
func arCondBadges(m automation.Match) []arBadge {
	var out []arBadge
	if len(m.Extensions) > 0 {
		out = append(out, arBadge{Label: i18n.T("automations.run.condExt", i18n.A{"ext": strings.Join(m.Extensions, " ")}), Variant: "info"})
	}
	if m.FilenamePattern != "" {
		out = append(out, arBadge{Label: i18n.T("automations.run.condPattern", i18n.A{"pattern": m.FilenamePattern}), Variant: "info"})
	}
	if m.MinSizeBytes > 0 {
		out = append(out, arBadge{Label: i18n.T("automations.run.condSize", i18n.A{"size": arBytes(m.MinSizeBytes)}), Variant: "info"})
	}
	if m.MinAgeDays > 0 {
		out = append(out, arBadge{Label: i18n.T("automations.run.condAge", i18n.A{"n": strconv.Itoa(m.MinAgeDays)}), Variant: "info"})
	}
	return out
}

// arWhyHints explains why a rules-first sweep matched nothing right now, one hint per active gate.
func arWhyHints(m automation.Match) []string {
	var out []string
	if m.MinAgeDays > 0 {
		out = append(out, i18n.T("automations.run.whyAge", i18n.A{"n": strconv.Itoa(m.MinAgeDays)}))
	}
	if m.FilenamePattern != "" {
		out = append(out, i18n.T("automations.run.whyPattern", i18n.A{"pattern": m.FilenamePattern}))
	}
	if len(m.Extensions) > 0 {
		out = append(out, i18n.T("automations.run.whyExt", i18n.A{"ext": strings.Join(m.Extensions, " ")}))
	}
	if m.MinSizeBytes > 0 {
		out = append(out, i18n.T("automations.run.whySize", i18n.A{"size": arBytes(m.MinSizeBytes)}))
	}
	if len(out) == 0 {
		out = append(out, i18n.T("automations.run.whyNone"))
	}
	return out
}

// arBytes formats a non-negative byte count via the package's humanBytes helper (int64 → uint64).
func arBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	return humanBytes(uint64(n))
}

// fileAge renders a file's mtime as a coarse age relative to now (the min-age gate's own unit).
func fileAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	days := int(time.Since(t).Hours() / 24)
	if days <= 0 {
		return i18n.T("automations.run.ageToday")
	}
	return i18n.Tn("automations.run.ageDays", days)
}
