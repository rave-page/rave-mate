package webui

// actionMenu: compact "⋯" dropdown of one-shot actions, built on smartSelect (mp-more
// pattern generalized). Demotes secondary button walls into a menu without information
// loss: each item keeps its full label + optional Sub line (the tooltip text).
// Item Val = the target act (colons fine); picking dispatches it via menugo:.
// id must be a unique colon-free token per instance.

// actionMenu registers + renders the menu. resolveActionMenu owns the option shape and
// actionMenuHTML the wrapper markup (both below), so the Go and Zig render paths share
// ONE source; TestActionMenuResolvedParity pins them.
func actionMenu(id, label string, items []ssOpt) string {
	return actionMenuHTML(resolveActionMenu(id, label, items))
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
// smartSelectRaw → resolveSmartSelect + selHTMLRaw). actionMenu above now delegates
// here (library batch), so there is exactly one markup source;
// TestActionMenuResolvedParity keeps pinning the two entry points to the same bytes.

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
