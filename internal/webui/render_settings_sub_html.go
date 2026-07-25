package webui

import (
	"html"
	"strings"
)

// Settings SUB-VIEW render state + the PURE renderers over it (golden reference for
// native/zigui/src/settings_sub.zig, zigui_golden_settings_sub_test.go). These are the four card
// bodies the settings port carried as trusted raw HTML - the wave-3 seams listed in the header of
// settings.zig - now crossing as structured state:
//
//	settings_gridfix.go       gridfixCardState()   → card "gridfix" body      (block kind "gridfix")
//	settings_gridfix_model.go gridfixModelState()  → card "gridfixmodel" body (block kind "gridfixmodel")
//	bridge_actions.go         bridgeCardState()    → card "accountbridge" body(block kind "bridge")
//	update_actions.go         updateFlowState()    → #inst-update region      (block kind "updregion")
//
// The impure halves stay in those files (service/config/probe/gate reads, i18n.T, smart-select
// registration); everything numeric is pre-formatted Go-side and every nested slice is non-nil
// (a nil slice marshals to JSON null, which the Zig parser rejects - the whole tab would then
// silently fall back to Go).

// ── gridfix card ──

// gfBtn is one engine-variant action. Gate non-empty = the dependency is missing: btnGated
// (disabled, title names what's missing) instead of a live button - explicit flag, never
// "empty act means gated".
type gfBtn struct {
	Label   string `json:"label"`
	Variant string `json:"variant,omitempty"`
	Act     string `json:"act,omitempty"`
	Gate    string `json:"gate,omitempty"`
}

// gfVarSt is one gridfix engine variant (cpu|cuda) resolved for render.
type gfVarSt struct {
	Key  string `json:"key"` // trusted literal (inst-gridfix-<key>)
	Tone string `json:"tone"`
	// Line may already carry HTML entities: the Go original concatenates an esc()'d version
	// string onto the i18n line and hands the result to hint(), which escapes AGAIN. Replicated
	// deliberately (golden-gated) - both renderers escape Line exactly once here.
	Line    string  `json:"line"`
	Btns    []gfBtn `json:"btns,omitempty"` // install / remove; empty = no btn-row
	HasNote bool    `json:"hasNote,omitempty"`
	Note    string  `json:"note,omitempty"` // CUDA-present hint under the buttons
}

// gfCardSt is the beatgrid-fixer card body (engine state + install controls + knobs).
type gfCardSt struct {
	LeadKind string    `json:"leadKind,omitempty"` // ""|"hint"|"note" - the leading probe verdict
	LeadTone string    `json:"leadTone,omitempty"`
	Lead     string    `json:"lead,omitempty"`
	Vars     []gfVarSt `json:"vars,omitempty"` // empty before the probe lands / with no base python
	Recheck  uiBtn     `json:"recheck"`
	Engine   selState  `json:"engine"`
	Python   uiField   `json:"python"` // path row: field + Browse
	Browse   uiBtn     `json:"browse"`
	MinQ     uiField   `json:"minq"`
	Thresh   uiField   `json:"thresh"`
	Lock     uiToggle  `json:"lock"`
	HasCal   bool      `json:"hasCal,omitempty"`
	Cal      string    `json:"cal,omitempty"`
	CalNote  string    `json:"calNote"`
	Note     string    `json:"note"`
}

// gfVarHTML renders one engine variant: status line, actions, its progress target.
func gfVarHTML(v gfVarSt) string {
	out := hint(v.Tone, v.Line)
	if len(v.Btns) > 0 {
		bs := make([]string, 0, len(v.Btns))
		for _, b := range v.Btns {
			if b.Gate != "" {
				bs = append(bs, btnGated(b.Label, b.Gate))
				continue
			}
			bs = append(bs, btn(b.Label, b.Variant, b.Act, ""))
		}
		out += btnRow(bs...)
	}
	if v.HasNote {
		out += setNote(v.Note)
	}
	return out + `<div id=inst-gridfix-` + v.Key + `></div>`
}

// gfCardHTML renders the gridfix card body.
func gfCardHTML(s gfCardSt) string {
	var b strings.Builder
	switch s.LeadKind {
	case "hint":
		b.WriteString(hint(s.LeadTone, s.Lead))
	case "note":
		b.WriteString(setNote(s.Lead))
	}
	for _, v := range s.Vars {
		b.WriteString(gfVarHTML(v))
	}
	b.WriteString(btnRow(s.Recheck.html()))
	b.WriteString(selHTML(s.Engine))
	b.WriteString(`<div class=set-pathrow>` + s.Python.html() + s.Browse.html() + `</div>`)
	b.WriteString(s.MinQ.html())
	b.WriteString(s.Thresh.html())
	b.WriteString(s.Lock.html())
	if s.HasCal {
		b.WriteString(setNote(s.Cal))
	}
	b.WriteString(setNote(s.CalNote))
	b.WriteString(setNote(s.Note))
	return b.String()
}

// ── gridfix model card ──

// gfModelSt is the model/training card body: active-checkpoint picker + fine-tune state.
type gfModelSt struct {
	Sel     selState `json:"sel"`
	Dataset string   `json:"dataset"`
	Running bool     `json:"running"`
	BarPct  string   `json:"barPct,omitempty"` // pre-formatted fill width (floats never cross)
	BarCap  string   `json:"barCap,omitempty"`
	Cancel  uiBtn    `json:"cancel"`
	// verdict/err/train/few are the idle branch; each carries its own flag so a blank i18n
	// string can never flip a branch.
	HasVerdict  bool   `json:"hasVerdict,omitempty"`
	VerdictTone string `json:"verdictTone,omitempty"`
	Verdict     string `json:"verdict,omitempty"`
	Err         string `json:"err,omitempty"`
	CanTrain    bool   `json:"canTrain,omitempty"`
	Train       uiBtn  `json:"train"`
	Few         bool   `json:"few,omitempty"`
	FewHint     string `json:"fewHint,omitempty"`
	Note        string `json:"note"`
}

// gfModelHTML renders the gridfix model card body.
func gfModelHTML(s gfModelSt) string {
	var b strings.Builder
	b.WriteString(selHTML(s.Sel))
	b.WriteString(setNote(s.Dataset))
	if s.Running {
		b.WriteString(`<div id=gfm-live>` + progressBarStr(s.BarPct, s.BarCap) + `</div>`)
		b.WriteString(btnRow(s.Cancel.html()))
	} else {
		if s.HasVerdict {
			b.WriteString(hint(s.VerdictTone, s.Verdict))
		}
		if s.Err != "" {
			b.WriteString(hint("bad", s.Err))
		}
		if s.CanTrain {
			b.WriteString(btnRow(s.Train.html()))
		}
		if s.Few {
			b.WriteString(setNote(s.FewHint))
		}
	}
	b.WriteString(setNote(s.Note))
	return b.String()
}

// ── account-bridge card ──

// bridgeSessSt is one trusted session row.
type bridgeSessSt struct {
	Title  string `json:"title"`
	Sub    string `json:"sub"`
	Revoke uiBtn  `json:"revoke"`
}

// bridgeGateSt is the access-gate section body. Kind ∈ enrol|enrolled|none.
type bridgeGateSt struct {
	Kind string `json:"kind"`
	// enrol: the pending secret is shown ONCE + a code is asked back (hand-rolled form -
	// field() emits no name attribute, so parseForm would see nothing).
	Help      string `json:"help,omitempty"`
	Secret    string `json:"secret,omitempty"`
	URI       string `json:"uri,omitempty"`
	CodeLabel string `json:"codeLabel,omitempty"`
	CodeDL    string `json:"codeDL,omitempty"` // Go strings.ToLower(CodeLabel)
	Confirm   string `json:"confirm,omitempty"`
	Cancel    uiBtn  `json:"cancel"`
	Burn      string `json:"burn,omitempty"`
	// enrolled: status rows (enrolled, plus the not-persisted warning where there is no OS
	// secret store); none: the no-authenticator note. Btn is that branch's single action.
	Rows []uiStatus `json:"rows,omitempty"`
	Note string     `json:"note,omitempty"`
	Btn  uiBtn      `json:"btn"`
	// trusted sessions
	SessionsTitle string         `json:"sessionsTitle"`
	Empty         string         `json:"empty,omitempty"`
	Sessions      []bridgeSessSt `json:"sessions,omitempty"`
	RevokeAll     uiBtn          `json:"revokeAll"`
}

// bridgeSt is the account-bridge card body.
type bridgeSt struct {
	St        uiStatus     `json:"st"` // Variant "" = no live-state row (bridge off / absent)
	Studio    uiToggle     `json:"studio"`
	Tip       string       `json:"tip"` // pre-rendered tipTopic markup (trusted, raw)
	HasGate   bool         `json:"hasGate,omitempty"`
	GateTitle string       `json:"gateTitle,omitempty"`
	Gate      bridgeGateSt `json:"gate"`
}

// bridgeCardHTML renders the account-bridge card body.
func bridgeCardHTML(s bridgeSt) string {
	var b strings.Builder
	b.WriteString(s.St.html())
	b.WriteString(toggleRowTipDL(s.Studio.Label, s.Studio.DL, s.Studio.Act, s.Studio.On, s.Tip))
	if !s.HasGate {
		return b.String()
	}
	b.WriteString(section(s.GateTitle, bridgeGateHTML(s.Gate)))
	return b.String()
}

// bridgeGateHTML renders enrolment + the trusted sessions.
func bridgeGateHTML(g bridgeGateSt) string {
	var b strings.Builder
	switch g.Kind {
	case "enrol":
		b.WriteString(setNote(g.Help))
		b.WriteString(`<div class="bridge-secret mono">` + html.EscapeString(g.Secret) + `</div>`)
		b.WriteString(`<div class="bridge-uri mono">` + html.EscapeString(g.URI) + `</div>`)
		b.WriteString(`<form data-act=bridge-confirm class=bridge-confirm>` +
			`<label class=field data-label=` + attrQ(g.CodeDL) +
			`><span class=field-label>` + html.EscapeString(g.CodeLabel) + `</span>` +
			`<input class=field-input type=text name=code data-act=bridge-code data-label=` + attrQ(g.CodeDL) +
			` inputmode=numeric autocomplete=one-time-code maxlength=6 value=""></label>` +
			`<div class=btn-row>` +
			`<button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(g.Confirm) + `</button>` +
			g.Cancel.html() + `</div></form>`)
		b.WriteString(setNote(g.Burn))
	case "enrolled":
		for _, r := range g.Rows {
			b.WriteString(r.html())
		}
		b.WriteString(btnRow(g.Btn.html()))
	default:
		b.WriteString(setNote(g.Note))
		b.WriteString(btnRow(g.Btn.html()))
	}
	b.WriteString(`<div class=set-sub>` + html.EscapeString(g.SessionsTitle) + `</div>`)
	if len(g.Sessions) == 0 {
		return b.String() + emptyState(g.Empty)
	}
	for _, s := range g.Sessions {
		b.WriteString(listRow(s.Title, s.Sub, s.Revoke.html()))
	}
	return b.String() + btnRow(g.RevokeAll.html())
}

// ── update flow (#inst-update) ──

// updFlowSt is the #inst-update region: the updater state machine's verdict + its ONE action.
// Kind "" renders nothing (no manager, dev build with no feed, or a poll that never ran).
type updFlowSt struct {
	Kind     string `json:"kind"` // ""|idle|avail|dl|ready|staged
	Tone     string `json:"tone,omitempty"`
	Text     string `json:"text,omitempty"`
	HasNotes bool   `json:"hasNotes,omitempty"`
	Notes    string `json:"notes,omitempty"` // release notes (set-note)
	Err      string `json:"err,omitempty"`   // trailing bad hint (message already prefixed)
	Pct      string `json:"pct,omitempty"`   // dl: pre-formatted fill width
	Cap      string `json:"cap,omitempty"`
	HasBtn   bool   `json:"hasBtn,omitempty"`
	Btn      uiBtn  `json:"btn"`
}

// updFlowHTMLOf renders the #inst-update inner markup.
func updFlowHTMLOf(s updFlowSt) string {
	switch s.Kind {
	case "idle":
		return hint(s.Tone, s.Text)
	case "dl":
		return progressBarStr(s.Pct, s.Cap)
	case "avail", "ready", "staged":
		out := hint(s.Tone, s.Text)
		if s.HasNotes {
			out += setNote(s.Notes)
		}
		if s.Err != "" {
			out += hint("bad", s.Err)
		}
		if s.HasBtn {
			out += btnRow(s.Btn.html())
		}
		return out
	}
	return ""
}
