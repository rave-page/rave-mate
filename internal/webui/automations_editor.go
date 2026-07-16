package webui

// Automations create/edit form - the webview's only authoring path for an automation
// (identity + match rules + ordered action chain). Follows the re-encode modal pattern
// (library_reencode.go): a Go-held working copy, acts that mutate it, modal re-opened on
// shape changes. Path fields re-open on change (they're the pick-dir targets - a picked
// path must show up); the other free-text fields store quietly so tabbing between inputs
// doesn't re-render under the cursor.
//
// Off the actWorker: the store read (Get) and Save's bbolt fsync both run in u.bg. Save goes
// through automation.Manager.Save, so Version() bumps and the ~1Hz automations tick
// (live_ticks.go) repaints the list - no hand-rolled refresh.

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/transcode"
)

const aeMiB = 1024 * 1024

// aeOwner is this modal's slot-owner key (ui.go modalTok): Save + the Get behind Edit both land
// off the actWorker, and neither may patch a modal the user has since replaced.
const aeOwner = "auto-ed"

// aeStep is one chain step plus a stable identity. key is minted per step and travels with it
// across reorder and removal, so an act that names a key names THE step. An index names whatever
// sits at that position when the act lands - and a picked path lands up to ten minutes late, from
// a dialog the user can drive the whole UI behind (pick_actions.go). An index is not an identity.
type aeStep struct {
	key uint64
	act automation.Action
}

// aeSt is the form's working copy. minSize holds the EXACT loaded byte count and is only
// recomputed when the user edits the MB field - editing an unrelated field must never silently
// re-round a threshold some other client wrote in bytes.
type aeSt struct {
	mu        sync.Mutex
	id        string // "" = create
	label     string
	watch     string
	enabled   bool
	extsTx    string // free text (".wav, flac") - normalized on save
	minSize   int64  // exact bytes
	minSizeTx string // MB, as shown
	pattern   string
	minAge    int
	acts      []aeStep
	actSeq    uint64 // step-key source; never reset, so a key from a replaced chain matches nothing
	errTx     string // last validation/save failure, shown in the form
}

func init() {
	onExact("auto-new", func(u *UI, _ actMsg) { u.aeNew() })
	onPrefix("auto-edit:", func(u *UI, m actMsg) { u.aeEdit(m.arg("auto-edit:")) })
	onPrefix("auto-ed:", func(u *UI, m actMsg) { u.aeField(u.actTok(m), m.arg("auto-ed:"), m.Val) })
	onPrefix("auto-ed-add:", func(u *UI, m actMsg) {
		u.aeAdd(u.actTok(m), automation.ActionType(m.arg("auto-ed-add:")))
	})
	onPrefix("auto-ed-rm:", func(u *UI, m actMsg) { u.aeRemove(u.actTok(m), aeKey(m.arg("auto-ed-rm:"))) })
	onPrefix("auto-ed-up:", func(u *UI, m actMsg) { u.aeMove(u.actTok(m), aeKey(m.arg("auto-ed-up:")), -1) })
	onPrefix("auto-ed-down:", func(u *UI, m actMsg) { u.aeMove(u.actTok(m), aeKey(m.arg("auto-ed-down:")), 1) })
	onPrefix("auto-ed-af:", func(u *UI, m actMsg) { u.aeActField(u.actTok(m), m.arg("auto-ed-af:"), m.Val) })
	onExact("auto-ed-save", func(u *UI, m actMsg) { u.aeSave(u.actTok(m)) })
}

// ── open ──

func (u *UI) aeNew() {
	s := &u.ae
	s.mu.Lock()
	s.load(automation.Automation{Enabled: true}) // new automations are armed by default
	s.mu.Unlock()
	u.openModalAs(aeOwner, u.aeModalHTML())
}

// load resets the form to a (fresh, independently-owned) automation. Field-wise, never
// `*s = aeSt{…}`: that would copy over the sync.Mutex we're holding. Caller holds s.mu.
func (s *aeSt) load(a automation.Automation) {
	s.id, s.label, s.watch, s.enabled = a.ID, a.Label, a.WatchDir, a.Enabled
	s.extsTx = strings.Join(a.Match.Extensions, ", ")
	s.minSize, s.minSizeTx = a.Match.MinSizeBytes, aeBytesToMB(a.Match.MinSizeBytes)
	s.pattern, s.minAge = a.Match.FilenamePattern, a.Match.MinAgeDays
	s.acts = make([]aeStep, 0, len(a.Actions))
	for _, act := range a.Actions {
		s.acts = append(s.acts, aeStep{s.nextKey(), act})
	}
	s.errTx = ""
}

// nextKey mints a step identity. Caller holds s.mu.
func (s *aeSt) nextKey() uint64 { s.actSeq++; return s.actSeq }

// find resolves a step key to its index. false = the step is gone: removed, or a different chain
// is loaded now. Either way the caller must DROP the write - it belongs to a step that no longer
// exists, and the position it used to hold is somebody else's. Caller holds s.mu.
func (s *aeSt) find(key uint64) (int, bool) {
	for i := range s.acts {
		if s.acts[i].key == key {
			return i, true
		}
	}
	return 0, false
}

// actions extracts the chain in order - the shape the engine's validators and Save consume, and a
// fresh copy the form can hand out. Caller holds s.mu.
func (s *aeSt) actions() []automation.Action {
	out := make([]automation.Action, len(s.acts))
	for i := range s.acts {
		out[i] = s.acts[i].act
	}
	return out
}

// aeKey parses a step key from an act argument. 0 = unparseable, and no step ever carries 0
// (nextKey starts at 1), so a malformed act resolves to no step.
func aeKey(s string) uint64 {
	k, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return k
}

// aeEdit loads id into the form. Get re-reads + unmarshals from bbolt, so the form owns an
// independent copy - List() hands back elements whose Actions/Extensions still alias the
// service's cache (manager.go), which a form MUST NOT mutate.
// prev pins the slot as it was at click time; the load itself runs inside claimModalWith, so it
// only happens if that pin still holds. Loading first and checking after would seed the form the
// user moved on to with this automation's data, which dropping the render does not undo.
func (u *UI) aeEdit(id string) {
	if u.svc.Automations == nil {
		return
	}
	prev := u.modalCur()
	u.bg(func() {
		a, ok := u.svc.Automations.Get(id)
		if !ok || u.stopped() {
			return
		}
		u.claimModalWith(prev, aeOwner, func() string {
			s := &u.ae
			s.mu.Lock()
			defer s.mu.Unlock()
			s.load(a)
			return u.aeModalHTMLLocked(s)
		})
	})
}

// ── field mutation ──

// aeField applies a top-level form field under tok - the session the value was chosen for (the
// picker's, or the form on screen for a plain click). Path fields re-open the modal (they double as
// the pick-dir target); the rest store quietly - they can't change the form's shape.
func (u *UI) aeField(tok modalTok, f, val string) {
	u.updateModalIf(tok, func() string { // refused = this form is gone; the write would hit its successor
		s := &u.ae
		s.mu.Lock()
		defer s.mu.Unlock()
		reopen := false
		switch f {
		case "label":
			s.label = val
		case "watch":
			s.watch = strings.TrimSpace(val)
			reopen = true // pick-dir:auto-ed:watch lands here - re-open so the picked path shows
		case "enabled":
			s.enabled = val == "true"
		case "exts":
			s.extsTx = val
		case "minsize":
			s.minSizeTx = val
			s.minSize = int64(atof(val) * aeMiB)
		case "pattern":
			s.pattern = strings.TrimSpace(val)
		case "minage":
			s.minAge = atoi(val)
			reopen = true // gates the "schedule-only" warning below the field
		}
		if !reopen {
			return ""
		}
		return u.aeModalHTMLLocked(s)
	})
}

// aeActField applies "<stepKey>:<field>" to one step. Re-opens for shape/validity-relevant
// changes (loudness toggle, preset, output dir); scalars store quietly.
func (u *UI) aeActField(tok modalTok, arg, val string) {
	keyTx, f, ok := strings.Cut(arg, ":")
	if !ok {
		return
	}
	key := aeKey(keyTx)
	u.updateModalIf(tok, func() string {
		s := &u.ae
		s.mu.Lock()
		defer s.mu.Unlock()
		i, ok := s.find(key) // the step, not a position: it can have been removed or reordered
		if !ok {
			return ""
		}
		a := &s.acts[i].act
		reopen := false
		switch f {
		case "preset":
			a.PresetID = val
			reopen = true // clears/raises the "transcode action requires a preset" banner
		case "dir":
			a.OutputDir = strings.TrimSpace(val)
			reopen = true // pick-dir target + gates move/copy validity
		case "thr":
			a.ThresholdDb = atof(val)
		case "minsil":
			a.MinSilenceSeconds = atof(val)
		case "trims":
			a.TrimStart = aeBoolPtr(val == "true")
		case "trime":
			a.TrimEnd = aeBoolPtr(val == "true")
		case "buf":
			a.BufferMinutes = atoi(val)
		case "tmpl":
			a.Template = val
		case "loudon":
			a.LoudnessOn = val == "true"
			reopen = true // shows/hides the target fields
		case "loudi":
			a.LoudnessI = atof(val)
		case "loudtp":
			a.LoudnessTP = atof(val)
		case "loudraise":
			a.LoudnessRaiseOnly = val == "true"
		}
		if !reopen {
			return ""
		}
		return u.aeModalHTMLLocked(s)
	})
}

func (u *UI) aeAdd(tok modalTok, t automation.ActionType) {
	u.updateModalIf(tok, func() string {
		s := &u.ae
		s.mu.Lock()
		defer s.mu.Unlock()
		s.acts = append(s.acts, aeStep{s.nextKey(), automation.Action{Type: t}})
		s.errTx = ""
		return u.aeModalHTMLLocked(s)
	})
}

func (u *UI) aeRemove(tok modalTok, key uint64) {
	u.updateModalIf(tok, func() string {
		s := &u.ae
		s.mu.Lock()
		defer s.mu.Unlock()
		i, ok := s.find(key)
		if !ok {
			return ""
		}
		s.acts = append(s.acts[:i], s.acts[i+1:]...)
		s.errTx = ""
		return u.aeModalHTMLLocked(s)
	})
}

// aeMove reorders the step named by key by d positions.
func (u *UI) aeMove(tok modalTok, key uint64, d int) {
	u.updateModalIf(tok, func() string {
		s := &u.ae
		s.mu.Lock()
		defer s.mu.Unlock()
		i, ok := s.find(key)
		if !ok {
			return ""
		}
		j := i + d
		if j < 0 || j >= len(s.acts) {
			return ""
		}
		s.acts[i], s.acts[j] = s.acts[j], s.acts[i]
		s.errTx = ""
		return u.aeModalHTMLLocked(s)
	})
}

// ── build + save ──

// aeBuild converts the form to an Automation, applying the checks the engine can't make
// (required label/watch dir, filenamePattern must compile) then delegating the chain to
// automation.ValidateActions + ValidateLoudness - the single source of truth for what the engine
// can run (internal/studio deliberately does the same rather than keeping a second copy of the
// rules, so a chain saved here is a chain the wire accepts).
func (u *UI) aeBuild() (automation.Automation, error) {
	s := &u.ae
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.label) == "" {
		return automation.Automation{}, fmt.Errorf("%s", i18n.T("automations.ed.errLabel"))
	}
	if s.watch == "" {
		return automation.Automation{}, fmt.Errorf("%s", i18n.T("automations.ed.errWatch"))
	}
	if s.pattern != "" {
		if _, err := regexp.Compile(s.pattern); err != nil {
			return automation.Automation{}, fmt.Errorf("%s: %s", i18n.T("automations.ed.errPattern"), err)
		}
	}
	acts := s.actions()
	if err := automation.ValidateActions(acts); err != nil {
		return automation.Automation{}, err
	}
	if err := automation.ValidateLoudness(acts, u.aePresets()); err != nil {
		return automation.Automation{}, err
	}
	return automation.Automation{
		ID: s.id, Label: strings.TrimSpace(s.label), WatchDir: s.watch, Enabled: s.enabled,
		Match: automation.Match{
			Extensions:      aeParseExts(s.extsTx),
			MinSizeBytes:    s.minSize,
			FilenamePattern: s.pattern,
			MinAgeDays:      s.minAge,
		},
		Actions: acts,
	}, nil
}

// aeSave validates, then persists off the actWorker (Save fsyncs bbolt). tok is the form session
// that clicked Save, and the ONLY one this call may write to: the fsync lands off the actWorker,
// and the user can close this form - or open another automation - before it returns.
func (u *UI) aeSave(tok modalTok) {
	if u.svc.Automations == nil {
		return
	}
	a, err := u.aeBuild()
	if err != nil {
		u.aeSetErr(tok, err.Error())
		return
	}
	if !u.actStart("auto-ed-save") {
		return
	}
	u.pendingAct("auto-ed-save")
	u.bg(func() {
		defer u.actEnd("auto-ed-save")
		// An id means "update", but the row can have been deleted under an open form (the card's
		// Delete takes no confirmation). Save is a blind Put, so without this check the form
		// silently RESURRECTS an automation the user deleted on purpose.
		if a.ID != "" {
			if _, ok := u.svc.Automations.Get(a.ID); !ok {
				u.aeVanished(tok)
				return
			}
		}
		saved, err := u.svc.Automations.Save(a)
		if u.stopped() {
			return
		}
		if err != nil {
			u.logErr("automation save", err)
			u.aeFailed(tok, err.Error())
			return
		}
		// A create becomes an edit: a second Save must update, not duplicate. Only into the form
		// that asked - if the user cancelled it and opened another automation, writing this id into
		// THAT form would silently retarget its next Save at this one, overwriting a different
		// entity with the edits on screen.
		u.updateModalIf(tok, func() string {
			u.ae.mu.Lock()
			u.ae.id = saved.ID
			u.ae.mu.Unlock()
			return "" // closeModalIf takes the form down next; no re-render
		})
		u.closeModalIf(tok) // false = the user already closed it, or another form owns the slot
		u.patchMain()
		u.toast(i18n.T("automations.ed.saved"))
	})
}

// aeVanished reports the automation was deleted under the open form. The form reverts to a create,
// so the edits on screen are recoverable with one more Save instead of being lost to the error -
// but only while it IS the form on screen: reverting a form the user has since loaded another
// automation into would turn its next Save into a duplicate.
func (u *UI) aeVanished(tok modalTok) {
	if !u.aeSetErrIf(tok, i18n.T("automations.ed.errVanished"), true) {
		u.toast(i18n.T("automations.ed.errVanished")) // form already gone - don't re-open it on them
	}
}

// aeFailed surfaces a save failure in the form that raised it, or as a toast when that form is
// gone: a completion must never re-open a modal the user closed, nor write its error into the form
// that replaced it.
func (u *UI) aeFailed(tok modalTok, msg string) {
	if !u.aeSetErrIf(tok, msg, false) {
		u.toast(msg)
	}
}

// aeSetErr shows a validation failure in tok's form. Called on the act lane, pre-save, where the
// form is by construction the modal on screen - a Save act that arrives without it must not open it.
func (u *UI) aeSetErr(tok modalTok, msg string) { u.aeSetErrIf(tok, msg, false) }

// aeSetErrIf writes msg into tok's form and re-renders it; toCreate also clears the id (the row is
// gone). false = tok no longer owns the modal, so nothing was written.
func (u *UI) aeSetErrIf(tok modalTok, msg string, toCreate bool) bool {
	return u.updateModalIf(tok, func() string {
		s := &u.ae
		s.mu.Lock()
		defer s.mu.Unlock()
		if toCreate {
			s.id = ""
		}
		s.errTx = msg
		return u.aeModalHTMLLocked(s)
	})
}

// aeAllPresets merges builtins + the user's config presets. Pure (no I/O): a render - and the
// smartSelect options closure inside it - runs on the actWorker.
func (u *UI) aeAllPresets() []transcode.Preset {
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	return transcode.AllPresets(custom)
}

// aePresets adapts the same list to the engine's resolver shape, so the editor's loudness verdict
// is computed by automation.ValidateLoudness over the SAME presets the engine resolves - not a
// second lookup that could disagree with it.
func (u *UI) aePresets() automation.PresetResolver {
	all := u.aeAllPresets()
	return func(id string) (transcode.Preset, bool) {
		for _, p := range all {
			if p.ID == id {
				return p, true
			}
		}
		return transcode.Preset{}, false
	}
}

// ── render (pure: reads the working copy + in-memory presets, no I/O) ──

func (u *UI) aeModalHTML() string {
	s := &u.ae
	s.mu.Lock()
	defer s.mu.Unlock()
	return u.aeModalHTMLLocked(s)
}

// aeModalHTMLLocked renders the form. Caller holds s.mu - the mutators already do, and re-taking
// it would drop the write and the render out of one atomic step.
func (u *UI) aeModalHTMLLocked(s *aeSt) string {
	presets := u.aeAllPresets() // pure merge of builtins+config; safe to close over

	var b strings.Builder
	if s.errTx != "" {
		b.WriteString(`<div class=ae-err>` + hint("bad", s.errTx) + `</div>`)
	}
	// identity
	b.WriteString(field(i18n.T("automations.ed.label"), "auto-ed:label", s.label, "text"))
	b.WriteString(`<div class=lib-toolbar>` +
		fieldTip(i18n.T("automations.ed.watchDir"), "auto-ed:watch", s.watch, "text", tipTopic("auto-watch-dir")) +
		btn(i18n.T("common.browse"), "ghost", "pick-dir:auto-ed:watch", "") + `</div>`)
	b.WriteString(toggleRow(i18n.T("common.enabledCap"), "auto-ed:enabled", s.enabled))

	// match
	b.WriteString(section(i18n.T("automations.ed.secMatch"), u.aeMatchHTML(s)))

	// action chain
	b.WriteString(section(i18n.T("automations.ed.secActions"), u.aeChainHTML(s, presets)))

	footer := btnRow(btn(i18n.T("automations.ed.save"), "primary", "auto-ed-save", ""),
		btn(i18n.T("common.cancel"), "ghost", "modal-close", ""))
	title := i18n.T("automations.ed.titleNew")
	if s.id != "" {
		title = i18n.T("automations.ed.titleEdit")
	}
	return modal(title, b.String(), footer)
}

// aeMatchHTML renders the eligibility rules. Caller holds s.mu.
func (u *UI) aeMatchHTML(s *aeSt) string {
	var b strings.Builder
	b.WriteString(fieldEx(i18n.T("automations.ed.exts"), "auto-ed:exts", s.extsTx, "text",
		i18n.T("automations.ed.extsPH"), tipTopic("auto-match-exts")))
	b.WriteString(fpair(
		fieldPH(i18n.T("automations.ed.minSize"), "auto-ed:minsize", s.minSizeTx, "number", "0"),
		fieldEx(i18n.T("automations.ed.minAge"), "auto-ed:minage", aeIntTx(s.minAge), "number", "0",
			tipTopic("auto-min-age")),
	))
	b.WriteString(fieldEx(i18n.T("automations.ed.pattern"), "auto-ed:pattern", s.pattern, "text",
		i18n.T("automations.ed.patternPH"), tipTopic("auto-match-pattern")))
	if s.minAge > 0 {
		// The gate the watcher can never pass - say so where it's set, not in a failed run.
		b.WriteString(hint("warn", i18n.T("automations.ed.minAgeWatchWarn")))
	}
	return b.String()
}

// aeChainHTML renders the ordered steps + the add palette + live validation. Caller holds s.mu.
func (u *UI) aeChainHTML(s *aeSt, presets []transcode.Preset) string {
	var b strings.Builder
	if len(s.acts) == 0 {
		b.WriteString(emptyState(i18n.T("automations.ed.noSteps")))
	}
	for i, st := range s.acts {
		b.WriteString(u.aeStepHTML(i, len(s.acts), st, presets))
	}
	b.WriteString(`<div class=btn-row>`)
	for _, t := range aeAddable {
		v := "outline"
		if t == automation.ActionDelete {
			v = "destructive"
		}
		b.WriteString(btn("+ "+aeTypeLabel(t), v, "auto-ed-add:"+string(t), ""))
	}
	b.WriteString(`</div>`)
	// Live verdict from the engine's own validators - the user learns before the save click, and
	// aeBuild re-runs both as the authority. Same order as aeBuild, so the banner names the same
	// failure Save would.
	if len(s.acts) > 0 {
		acts := s.actions()
		err := automation.ValidateActions(acts)
		if err == nil {
			err = automation.ValidateLoudness(acts, u.aePresets())
		}
		if err != nil {
			b.WriteString(hint("bad", err.Error()))
		}
	}
	return b.String()
}

// aeStepHTML renders one step: header (order + type + reorder/remove) then per-type fields only.
// Every act names the step's KEY, never its index: the DOM this renders can outlive the position
// (a reorder, a removal, a whole other chain loaded) while a native dialog it opened is still up.
func (u *UI) aeStepHTML(i, n int, st aeStep, presets []transcode.Preset) string {
	a := st.act
	af := func(f string) string { return fmt.Sprintf("auto-ed-af:%d:%s", st.key, f) }
	trailing := ""
	if i > 0 {
		trailing += btn("↑", "ghost", fmt.Sprintf("auto-ed-up:%d", st.key), "")
	}
	if i < n-1 {
		trailing += btn("↓", "ghost", fmt.Sprintf("auto-ed-down:%d", st.key), "")
	}
	trailing += btn("✕", "ghost", fmt.Sprintf("auto-ed-rm:%d", st.key), "")

	var b strings.Builder
	b.WriteString(`<div class=np-artist>` + html.EscapeString(aeTypeDesc(a.Type)) + `</div>`)
	switch a.Type {
	case automation.ActionRename:
		b.WriteString(fieldEx(i18n.T("automations.ed.bufferMinutes"), af("buf"), aeIntTx(a.BufferMinutes), "number",
			"180", tipTopic("auto-rename-buffer")))
		b.WriteString(fieldEx(i18n.T("automations.ed.template"), af("tmpl"), a.Template, "text",
			"{YYYY-MM-DD}_{venueSlug}_{eventSlug}{ext}", tipTopic("auto-rename-template")))
	case automation.ActionTrimSilence:
		b.WriteString(fpair(
			fieldEx(i18n.T("automations.ed.thresholdDb"), af("thr"), aeNumTx(a.ThresholdDb), "number", "-50",
				tipTopic("auto-trim-silence")),
			fieldPH(i18n.T("automations.ed.minSilence"), af("minsil"), aeNumTx(a.MinSilenceSeconds), "number", "2"),
		))
		b.WriteString(toggleRow(i18n.T("automations.ed.trimStart"), af("trims"), a.TrimStart == nil || *a.TrimStart))
		b.WriteString(toggleRow(i18n.T("automations.ed.trimEnd"), af("trime"), a.TrimEnd == nil || *a.TrimEnd))
		b.WriteString(aePresetSelect(st.key, af("preset"), a.PresetID, presets, "remux"))
		b.WriteString(aeOutDirHTML(af("dir"), a.OutputDir, i18n.T("automations.ed.alongside")))
		b.WriteString(aeLoudnessHTML(af, a, presets, "remux"))
	case automation.ActionTranscode:
		b.WriteString(aePresetSelect(st.key, af("preset"), a.PresetID, presets, ""))
		b.WriteString(aeOutDirHTML(af("dir"), a.OutputDir, i18n.T("automations.ed.alongside")))
		b.WriteString(aeLoudnessHTML(af, a, presets, ""))
	case automation.ActionMove, automation.ActionCopy:
		b.WriteString(aeOutDirHTML(af("dir"), a.OutputDir, ""))
	case automation.ActionDelete:
		// The one irreversible step: say exactly what it erases and where it stops.
		b.WriteString(hint("bad", i18n.T("automations.ed.deleteWarn")))
		b.WriteString(`<div class=pb-hint>` + html.EscapeString(i18n.T("automations.ed.deleteTerminal")) +
			tipTopic("auto-delete-action") + `</div>`)
	}
	return card(fmt.Sprintf("%d. %s", i+1, aeTypeLabel(a.Type)), trailing, b.String())
}

// aeLoudnessHTML renders the shared loudness block (components.go loudnessFields) as a per-action
// override of the step's preset. Applies to trim-silence too - both steps route through
// doTranscode, so resolvePreset sees the override (engine.go). presets resolves a.PresetID so the
// block can tell the user when the chosen preset copies audio and normalization therefore can't run.
func aeLoudnessHTML(af func(string) string, a automation.Action, presets []transcode.Preset, dfltPreset string) string {
	return loudnessFields(loudnessOpts{
		act:       af,
		toggleLbl: i18n.T("library.enc.normalizeOverride"),
		topic:     "auto-loudness",
		vals:      loudnessVals{On: a.LoudnessOn, I: a.LoudnessI, TP: a.LoudnessTP, RaiseOnly: a.LoudnessRaiseOnly},
		override:  true,
		preset:    aeResolvePreset(a.PresetID, presets, dfltPreset),
	})
}

// aeResolvePreset resolves a step's preset id the way the engine does: blank falls back to dflt
// (trim-silence → remux). nil = unresolved; ValidateActions is the one to say so.
func aeResolvePreset(id string, presets []transcode.Preset, dflt string) *transcode.Preset {
	if id == "" {
		id = dflt
	}
	for i := range presets {
		if presets[i].ID == id {
			return &presets[i]
		}
	}
	return nil
}

// aeOutDirHTML renders an output-folder field + Browse. ph "" = the folder is required.
func aeOutDirHTML(act, cur, ph string) string {
	label := i18n.T("automations.ed.outputDir")
	if ph != "" {
		label = i18n.T("automations.ed.outputDirOptional")
	}
	return `<div class=lib-toolbar>` + fieldPH(label, act, cur, "text", ph) +
		btn(i18n.T("common.browse"), "ghost", "pick-dir:"+act, "") + `</div>`
}

// aePresetSelect renders the transcode-preset picker. The options closure is pure (it closes over
// the already-resolved slice) - a smartSelect closure runs during render and must never do I/O.
func aePresetSelect(key uint64, act, cur string, presets []transcode.Preset, dflt string) string {
	id := fmt.Sprintf("auto-ed-preset-%d", key) // smartSelect ids must be single colon-free tokens
	label := i18n.T("library.enc.preset")
	if dflt != "" && cur == "" {
		cur = dflt // trim-silence falls back to remux in the engine; show what will run
	}
	return smartSelect(id, label, act, cur, func() []ssOpt {
		out := make([]ssOpt, 0, len(presets))
		for _, p := range presets {
			out = append(out, ssOpt{Val: p.ID, Label: p.Label, Sub: p.Desc, Badge: strings.ToUpper(p.Container)})
		}
		return out
	})
}

// ── helpers ──

// aeAddable is the add palette, in chain order (delete last - it's terminal).
var aeAddable = []automation.ActionType{
	automation.ActionRename, automation.ActionTrimSilence, automation.ActionTranscode,
	automation.ActionMove, automation.ActionCopy, automation.ActionDelete,
}

func aeTypeLabel(t automation.ActionType) string {
	switch t {
	case automation.ActionRename:
		return i18n.T("automations.act.rename")
	case automation.ActionTrimSilence:
		return i18n.T("automations.act.trim")
	case automation.ActionTranscode:
		return i18n.T("automations.act.transcode")
	case automation.ActionMove:
		return i18n.T("automations.act.move")
	case automation.ActionCopy:
		return i18n.T("automations.act.copy")
	case automation.ActionDelete:
		return i18n.T("automations.act.delete")
	}
	return string(t)
}

func aeTypeDesc(t automation.ActionType) string {
	switch t {
	case automation.ActionRename:
		return i18n.T("automations.act.renameDesc")
	case automation.ActionTrimSilence:
		return i18n.T("automations.act.trimDesc")
	case automation.ActionTranscode:
		return i18n.T("automations.act.transcodeDesc")
	case automation.ActionMove:
		return i18n.T("automations.act.moveDesc")
	case automation.ActionCopy:
		return i18n.T("automations.act.copyDesc")
	case automation.ActionDelete:
		return i18n.T("automations.act.deleteDesc")
	}
	return ""
}

// aeParseExts normalizes free text ("wav, .FLAC aiff") to the engine's form (lower-case,
// dot-prefixed) - Match.matches compares against a lower-cased filepath.Ext.
func aeParseExts(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		e := strings.ToLower(strings.TrimSpace(f))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		out = append(out, e)
	}
	return out
}

// aeBytesToMB renders bytes as MB for the form; 0 (= no gate) shows blank so the placeholder reads.
// Rounded to 2 decimals for legibility (a raw byte count divides into something like
// 1.430511474609375 MB) - which is lossy, hence aeSt.minSize keeping the exact value.
func aeBytesToMB(b int64) string {
	if b <= 0 {
		return ""
	}
	return trimNum(math.Round(float64(b)/aeMiB*100) / 100)
}

// aeIntTx / aeNumTx render 0 (= "engine default"/"off") as blank, so the field's placeholder
// shows the default instead of a literal 0 the user would have to clear.
func aeIntTx(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func aeNumTx(f float64) string {
	if f == 0 {
		return ""
	}
	return trimNum(f)
}

func aeBoolPtr(b bool) *bool { return &b }
