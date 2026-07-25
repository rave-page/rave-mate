package webui

// actionMenu: compact "⋯" dropdown of one-shot actions, built on smartSelect (mp-more
// pattern generalized). Demotes secondary button walls into a menu without information
// loss: each item keeps its full label + optional Sub line (the tooltip text).
// Item Val = the target act (colons fine); picking dispatches it via menugo:.
// id must be a unique colon-free token per instance.

// actionMenu registers + renders the menu. resolveActionMenu owns the option shape and
// actionMenuOf the wrapper markup, so the Zig render path shares ONE source.
func actionMenu(id, label string, items []ssOpt) string {
	return actionMenuOf(resolveActionMenu(id, label, items))
}

func init() {
	onPrefix("menugo:", func(u *UI, m actMsg) {
		if act := m.arg("menugo:"); act != "" {
			u.dispatch(actMsg{Act: act})
		}
	})
}
