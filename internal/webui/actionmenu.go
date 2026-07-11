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
