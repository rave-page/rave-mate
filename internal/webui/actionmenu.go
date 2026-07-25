package webui

// actionMenu: compact "⋯" dropdown of one-shot actions, built on smartSelect (mp-more
// pattern generalized). Demotes secondary button walls into a menu without information
// loss: each item keeps its full label + optional Sub line (the tooltip text).
// Item Val = the target act (colons fine); picking dispatches it via menugo:.
// id must be a unique colon-free token per instance.

func actionMenu(id, label string, items []ssOpt) string {
	opts := make([]ssOpt, 0, len(items)+1)
	opts = append(opts, ssOpt{Val: "", Label: label})
	opts = append(opts, items...)
	return `<span class=amenu>` + smartSelect(id, "", "menugo:", "", func() []ssOpt { return opts }) + `</span>`
}

func init() {
	onPrefix("menugo:", func(u *UI, m actMsg) {
		if act := m.arg("menugo:"); act != "" {
			u.dispatch(actMsg{Act: act})
		}
	})
}

// --- publish (zigui) ---
// Resolved-state twin of actionMenu for Zig-migrated tabs (same split as
// smartSelectRaw → resolveSmartSelect + selHTMLRaw). actionMenu above is left
// untouched for the Go-rendered tabs; TestActionMenuResolvedParity pins the two
// to the same bytes.

// resolveActionMenu registers + resolves an actionMenu into pure render state. The
// menu label rides as the leading empty-Val option (it becomes CurLabel), exactly
// like actionMenu builds it.
func resolveActionMenu(id, label string, items []ssOpt) selState {
	return resolveSmartSelect(id, "menugo:", "", func() []ssOpt {
		opts := make([]ssOpt, 0, len(items)+1)
		opts = append(opts, ssOpt{Val: "", Label: label})
		return append(opts, items...)
	})
}

// actionMenuHTML renders an actionMenu from resolved state.
func actionMenuHTML(s selState) string { return `<span class=amenu>` + selHTML(s) + `</span>` }
