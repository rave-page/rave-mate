//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Automations golden gate: Zig renderer must be BYTE-IDENTICAL to the Go renderer for
// representative states. Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// autoFixtures: unavailable, empty, populated, escaping edge, long values, unicode.
func autoFixtures() map[string]autoState {
	labels := autoLabels{Enabled: "Enabled", EnabledDL: "enabled", Run: "Run now",
		SchAdd: "Add schedule", Edit: "Edit", Delete: "Delete"}
	base := func() autoState {
		return autoState{
			Title: "Automations", Sub: "File-arrival + scheduled chains", Available: true,
			Unavailable: "Automations unavailable",
			Body: autoBodyState{
				ListTitle: "Automations", SchedTitle: "Schedules", RunsTitle: "Recent runs",
				Labels: labels,
				List:   autoListState{New: "New automation", Empty: "No automations yet", Cards: []autoCard{}},
				Scheds: autoSchedsState{New: "New schedule", GateWhy: "Create an automation first",
					Empty: "No schedules yet", Cards: []autoSchedCard{}},
				Runs: autoRunsState{Empty: "No runs yet", Rows: []autoRunRow{}},
			},
		}
	}

	unavailable := autoState{Title: "Automations", Sub: "ignored", Available: false,
		Unavailable: "Automations unavailable", Body: emptyAutoBody()}

	// no automations at all → the schedules New button is gated
	empty := base()
	empty.Body.Scheds.Gated = true

	populated := base()
	populated.Body.List.Cards = []autoCard{
		{ID: "a1", Label: "Set captures", WatchDir: `C:\sets`, Status: "success", StatusVar: "success",
			Chain: "Trim silence → Transcode → Move to", Enabled: true},
		{ID: "a2", Label: "(unnamed)", WatchDir: "/mnt/recordings", Status: "", StatusVar: "",
			Chain: "No steps", Enabled: false},
		{ID: "a3", Label: "Purge", WatchDir: "/tmp", Status: "partial", StatusVar: "warning",
			Chain: "Delete", Enabled: true},
	}
	populated.Body.Scheds.Cards = []autoSchedCard{
		{ID: "s1", Label: "Nightly", Target: "Set captures", StateText: "daily", StateVar: "info",
			Trigger: "daily at 03:00", Gates: "no gates", LastFired: "last fired 03:00", Enabled: true},
		{ID: "s2", Label: "Orphan", Target: "(automation deleted)", StateText: "Off", StateVar: "secondary",
			Trigger: "every 60 min", Gates: "idle ≥ 10 min · needs obs64.exe", LastFired: "never fired",
			WarnTone: "bad", WarnText: "Its automation no longer exists", Enabled: false},
		{ID: "s3", Label: "Armed but off", Target: "Purge", StateText: "interval", StateVar: "info",
			Trigger: "every 15 min", Gates: "no gates", LastFired: "never fired",
			WarnTone: "warn", WarnText: "The automation itself is disabled", Enabled: true},
	}
	populated.Body.Runs.Rows = []autoRunRow{
		{Name: "set-01.wav", Trigger: "watch", Status: "success", Variant: "success"},
		{Name: "set-02.flac", Trigger: "manual", Status: "error", Variant: "error"},
		{Name: "set-03.wav", Trigger: "schedule", Status: "running", Variant: "info"},
		{Name: "set-04.wav", Trigger: "schedule", Status: "queued", Variant: "secondary"},
	}

	escaping := base()
	escaping.Title = `Auto&mations <"live">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.Body.ListTitle = `L&ist<">`
	escaping.Body.SchedTitle = `Sch'ed&<>`
	escaping.Body.RunsTitle = `R&uns"`
	escaping.Body.Labels = autoLabels{Enabled: `En&abled"<>'`, EnabledDL: `en&abled"<>'`,
		Run: `R&un "now"`, SchAdd: `Add & <sch>`, Edit: `E'dit&`, Delete: `De<lete>"`}
	escaping.Body.List.New = `N&ew "auto"`
	escaping.Body.List.Cards = []autoCard{
		{ID: `a:1"x'<&>`, Label: `A&B <"quoted'>`, WatchDir: `C:\p&th<"x">`, Status: `st&"at'<>`,
			StatusVar: "warning", Chain: `T&rim → M"ove'<>`, Enabled: true},
	}
	escaping.Body.Scheds.New = `S&ch "new"`
	escaping.Body.Scheds.GateWhy = `need &<an> "automation"'`
	escaping.Body.Scheds.Cards = []autoSchedCard{
		{ID: `s:1&"'<>`, Label: `N&ightly "purge"`, Target: `T&arget<'>`, StateText: `da&ily"`,
			StateVar: "info", Trigger: `at &03:00 <"x">`, Gates: `g&ates'<">`, LastFired: `f&ired"<>'`,
			WarnTone: "bad", WarnText: `w&arn "text"'<>`, Enabled: true},
	}
	escaping.Body.Runs.Rows = []autoRunRow{
		{Name: `f&ile "1"<>.wav`, Trigger: `tr&igger'<">`, Status: `st&at"`, Variant: "error"},
	}

	long := base()
	longS := strings.Repeat("very-long-", 120)
	long.Body.List.Cards = []autoCard{
		{ID: strings.Repeat("id-", 200), Label: longS, WatchDir: strings.Repeat("d/", 400),
			Status: strings.Repeat("s", 90), StatusVar: "secondary", Chain: longS, Enabled: false},
	}
	long.Body.Scheds.Cards = []autoSchedCard{
		{ID: strings.Repeat("sid-", 150), Label: longS, Target: longS, StateText: strings.Repeat("k", 200),
			StateVar: "info", Trigger: longS, Gates: strings.Repeat("g", 700), LastFired: longS,
			WarnTone: "warn", WarnText: longS, Enabled: true},
	}
	long.Body.Runs.Rows = []autoRunRow{
		{Name: strings.Repeat("n", 500) + ".wav", Trigger: longS, Status: strings.Repeat("q", 80), Variant: "info"},
	}

	unicode := base()
	unicode.Title = "オートメーション 🎧"
	unicode.Sub = "größer Автоматизация"
	unicode.Body.ListTitle = "Автоматизации"
	unicode.Body.SchedTitle = "スケジュール"
	unicode.Body.RunsTitle = "запуски"
	unicode.Body.Labels = autoLabels{Enabled: "Включено", EnabledDL: "включено", Run: "Запустити",
		SchAdd: "追加", Edit: "Змінити", Delete: "видалити"}
	unicode.Body.List.Cards = []autoCard{
		{ID: "a☂", Label: "Кириллица + 中文 🎛️", WatchDir: "C:\\Музыка\\сеты", Status: "успех",
			StatusVar: "success", Chain: "Обрезка → Перекодировать", Enabled: true},
	}
	unicode.Body.Scheds.Cards = []autoSchedCard{
		{ID: "s☂", Label: "Ночная", Target: "Кириллица", StateText: "щоденно", StateVar: "info",
			Trigger: "每天 03:00", Gates: "простой ≥ 10 мин", LastFired: "никогда", Enabled: true},
	}
	unicode.Body.Runs.Rows = []autoRunRow{
		{Name: "сет-01.wav", Trigger: "手動", Status: "успіх", Variant: "success"},
	}

	return map[string]autoState{
		"unavailable": unavailable,
		"empty":       empty,
		"populated":   populated,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
	}
}

func TestZigAutomationsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range autoFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderAutomations(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", automationsHTML(st), zig)

			bjs := stateJSON(st.Body)
			if bjs == nil {
				t.Fatal("body marshal failed")
			}
			zigBody, ok := zigui.RenderAutomationsBody(bjs)
			if !ok {
				t.Fatal("zig body render failed")
			}
			assertBytesEqual(t, "body", autoBodyHTML(st.Body), zigBody)
		})
	}
}
