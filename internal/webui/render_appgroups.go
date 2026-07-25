package webui

import (
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/appgroups"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/zigui"
)

// App Groups is the pilot Zig-rendered tab (native/zigui): Go resolves all state
// (data + i18n) into agState, the Zig lib renders HTML byte-identical to the Go
// renderer below. The Go renderer stays: fallback + golden reference
// (zigui_golden_test.go asserts Zig == Go byte-exact).

// agState is the resolved render state for the App Groups view (JSON → Zig).
type agState struct {
	Title       string    `json:"title"`
	Subtitle    string    `json:"subtitle"`
	Available   bool      `json:"available"`
	Unavailable string    `json:"unavailable"`
	Empty       string    `json:"empty"`
	Admin       string    `json:"admin"`  // elevated-app badge text
	Launch      string    `json:"launch"` // launch button label
	Groups      []agGroup `json:"groups"`
}

type agGroup struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Up      string  `json:"up"`      // resolved "{running}/{total} up"
	Variant string  `json:"variant"` // success|warning|muted
	Apps    []agApp `json:"apps"`
}

type agApp struct {
	Base     string `json:"base"` // filepath.Base of the exe
	Elevated bool   `json:"elevated"`
}

// appGroupsState resolves config + live running counts + i18n into render state.
// One process-table snapshot for ALL groups (was one full walk per group per render/tick).
func (u *UI) appGroupsState() agState {
	st := agState{
		Title:       i18n.T("tab.appgroups"),
		Subtitle:    i18n.T("appgroups.subtitle"),
		Available:   u.svc.AppGroups != nil,
		Unavailable: i18n.T("appgroups.unavailable"),
		Empty:       i18n.T("appgroups.empty"),
		Admin:       i18n.T("appgroups.admin"),
		Launch:      i18n.T("common.launch"),
		Groups:      []agGroup{},
	}
	groups := u.svc.Cfg.Features.AppGroups.Groups
	var counts []appgroups.Counts
	if u.svc.AppGroups != nil {
		counts = u.svc.AppGroups.RunningCounts(groups)
	} else {
		counts = make([]appgroups.Counts, len(groups))
	}
	for i, g := range groups {
		running, total := counts[i].Running, counts[i].Total
		variant := "muted"
		if running > 0 && running >= total {
			variant = "success"
		} else if running > 0 {
			variant = "warning"
		}
		ag := agGroup{
			ID:      g.ID,
			Name:    g.Name,
			Up:      i18n.T("appgroups.upCount", i18n.A{"running": fmt.Sprint(running), "total": fmt.Sprint(total)}),
			Variant: variant,
			Apps:    []agApp{},
		}
		for _, a := range g.Apps {
			ag.Apps = append(ag.Apps, agApp{Base: filepath.Base(a.Path), Elevated: a.Elevated})
		}
		st.Groups = append(st.Groups, ag)
	}
	return st
}

// stateJSON marshals render state for the Zig renderer (nil → zigui returns !ok →
// Go fallback). Param is `any` at the json boundary only; states are plain structs.
// Every Zig-path render funnels through here, so it is also where the marshal half of the
// phase-A round trip is measured (zigui.PerfCounts, `ctl perf` section [zigui]) - two
// time.Now() calls against a marshal that costs µs.
func stateJSON(v any) []byte {
	t0 := time.Now()
	b, err := json.Marshal(v)
	zigui.NoteMarshal(len(b), time.Since(t0)) // len(nil) == 0 → a failed marshal counts bytes 0
	if err != nil {
		return nil
	}
	return b
}

// renderAppGroups: crash-recovery launcher - list groups, running count, Launch.
// Render path: RZW1 binary state (v2) → JSON state (v1) → the Go renderer below (see wire.go).
func (u *UI) renderAppGroups() string {
	st := u.appGroupsState()
	if zigui.Available() {
		if h, ok := zigWire("RenderAppGroupsV2", wireAgState(st), zigui.RenderAppGroupsV2,
			zigui.RenderAppGroups, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return appGroupsHTML(st)
}

// appGroupsBody is the #appgroups-body inner fragment (~1 Hz tick patch target).
func (u *UI) appGroupsBody() string {
	st := u.appGroupsState()
	if zigui.Available() {
		if h, ok := zigWire("RenderAppGroupsBodyV2", wireAgState(st), zigui.RenderAppGroupsBodyV2,
			zigui.RenderAppGroupsBody, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return appGroupsBodyHTML(st)
}

// appGroupsHTML is the pure Go renderer (golden reference; byte-identical to Zig).
func appGroupsHTML(st agState) string {
	if !st.Available {
		return panel(st.Title, "") + emptyState(st.Unavailable)
	}
	return panel(st.Title, st.Subtitle) +
		`<div id=appgroups-body>` + appGroupsBodyHTML(st) + `</div>`
}

func appGroupsBodyHTML(st agState) string {
	if len(st.Groups) == 0 {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(`<div class=grid>`)
	for _, g := range st.Groups {
		var apps strings.Builder
		for _, a := range g.Apps {
			tag := ""
			if a.Elevated {
				tag = ` ` + badge(st.Admin, "warning")
			}
			apps.WriteString(`<div class=kv><span class=kv-k>` + html.EscapeString(a.Base) + tag + `</span><span class=kv-v></span></div>`)
		}
		b.WriteString(`<div class="rp-card"><div class=card-label>` + html.EscapeString(g.Name) + `</div>` +
			`<div class=np-meta>` + badgeDot(g.Up, g.Variant) + `</div>` +
			apps.String() +
			btnRow(btn(st.Launch, "go", "ag-launch:"+g.ID, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// badgeDot pairs a status dot with a badge.
func badgeDot(text, variant string) string { return dot(variant) + ` ` + badge(text, variant) }
