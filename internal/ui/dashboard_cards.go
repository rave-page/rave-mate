package ui

// Modular Live-cockpit cards: every card registers here as a module; config.DashboardCards
// (ordered enabled ids; empty = defaults) picks + orders what renders on the Live tab.
// "Edit dashboard" (top-right) toggles + reorders - v1 is a VBox with checks/arrows,
// no drag-drop.

import (
	"slices"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// dashCardDef is one dashboard module.
type dashCardDef struct {
	id, title, sub string
	help           string // ?-tooltip on the card title (education-first)
	defaultOn      bool
	build          func(u *UI) fyne.CanvasObject // nil result = unavailable (service absent)
}

// dashCardDefs is the registry, in default render order.
func dashCardDefs() []dashCardDef {
	return []dashCardDef{
		{
			id: "nowplaying", title: "Now playing", defaultOn: true,
			help:  "The track on air right now: the audible deck fused from every enabled DJ source (Traktor, MIDI, rekordbox, …). When no local deck is live it falls back to a paired peer's now-playing (peer bridge) - a set playing on one machine shows on every paired instance.",
			build: (*UI).buildNowPlayingLCD,
		},
		{
			id: "status", title: "Status", defaultOn: true,
			help:  "Live health of this rave-mate: which rave.page API it talks to, sign-in state, whether the Traktor listener is bound to its port, and the stream bridge state.",
			build: (*UI).buildStatusContent,
		},
		{
			id: "decks", title: "Decks (merged)", defaultOn: true,
			help:  "Per-deck data fused from every enabled DJ source (Traktor, MIDI, NML, …) by per-field priority. The italic tag shows which source won each deck's fields. Sources are configured in Settings ▸ DJ sources.",
			build: (*UI).buildDecksContent,
		},
		{
			id: "sources", title: "Sources & coverage", defaultOn: false,
			help:  "The DJ-data source matrix: which connection methods are enabled/receiving and what fields each provides per deck. Use it to see why a deck field is missing or which source to enable.",
			build: (*UI).buildSourcesContent,
		},
		{
			id: "obs", title: "Streaming cockpit", defaultOn: true,
			help:  "Viewer count plus per-instance OBS stream/record control with bitrate + health - including OBS on a paired instance or another LAN machine (configure endpoints in Settings ▸ Recording ▸ OBS).",
			build: (*UI).buildObsCockpitContent,
		},
		{
			id: "mediasync", title: "Media sync", defaultOn: false,
			help:  helpMediaSync + " Tunables + synced sources: Settings ▸ Recording ▸ Media sync.",
			build: (*UI).buildMediaSyncContent,
		},
		{
			id: "dmx", title: "DMX / lighting", defaultOn: false,
			help:  "Live Art-Net traffic per universe + the VRSL grid sink state. Listen address, universes and grid: Settings ▸ Integrations ▸ DMX / VRSL.",
			build: (*UI).buildDMXStatusContent,
		},
		{
			id: "stt", title: "Speak-to-chat", defaultOn: false,
			help:  "The last recognized transcript with copy / send / retry - review what speech-to-text heard before it goes to Twitch chat. Mic, model + install: Settings ▸ Integrations ▸ Speak-to-chat.",
			build: (*UI).buildSTTContent,
		},
		{
			id: "network", title: "Network", defaultOn: true,
			help:  "Traffic this app moves per second, plus session totals. PEER = the LAN link between paired rave-mates (DJ data, remote control, file sync). API = rave.page (stream ingest, library sync, auth).",
			build: (*UI).buildNetworkContent,
		},
		{
			id: "timing", title: "Timing", defaultOn: true,
			help:  "Round-trip time (RTT) of pings to each paired peer - how long a message takes there and back. A wired LAN sits under ~5 ms; spikes or climbing values usually mean Wi-Fi trouble or a saturated link.",
			build: (*UI).buildTimingContent,
		},
		{
			id: "sysperf", title: "System performance", defaultOn: true,
			help:  "Is this machine keeping up? rave-mate vs whole-system CPU and RAM over the last 2 minutes, plus explicit HEADROOM - how much room is left before things stutter.",
			build: (*UI).buildSysPerfContent,
		},
	}
}

// resolveDashCards maps the saved id list to the ordered enabled set: empty saved →
// registry defaults; unknown + duplicate ids dropped.
func resolveDashCards(saved []string, defs []dashCardDef) []string {
	if len(saved) == 0 {
		out := make([]string, 0, len(defs))
		for _, d := range defs {
			if d.defaultOn {
				out = append(out, d.id)
			}
		}
		return out
	}
	known := make(map[string]bool, len(defs))
	for _, d := range defs {
		known[d.id] = true
	}
	out := make([]string, 0, len(saved))
	for _, id := range saved {
		if known[id] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// dashCardIDs returns the persisted layout (in-memory fallback when Cfg is nil).
func (u *UI) dashCardIDs() []string {
	if u.svc.Cfg != nil {
		return u.svc.Cfg.DashboardCards
	}
	return u.dashCards
}

func (u *UI) setDashCardIDs(ids []string) {
	if u.svc.Cfg != nil {
		u.svc.Cfg.DashboardCards = ids
		u.saveCfg()
		return
	}
	u.dashCards = ids
}

// showDashEditor opens the "Edit dashboard" popover: every module with an enable
// check + ↑/↓ reorder. Changes persist + re-render immediately; the last enabled
// card can't be disabled (an empty list would silently snap back to defaults).
func (u *UI) showDashEditor(anchor fyne.CanvasObject, defs []dashCardDef, onChange func()) {
	win := currentWindow()
	if win == nil {
		return
	}
	enabled := resolveDashCards(u.dashCardIDs(), defs)
	byID := make(map[string]dashCardDef, len(defs))
	for _, d := range defs {
		byID[d.id] = d
	}

	list := container.NewVBox()
	var rebuild func()
	save := func() {
		u.setDashCardIDs(slices.Clone(enabled))
		onChange()
		rebuild()
	}
	row := func(d dashCardDef, pos int) fyne.CanvasObject { // pos = index in enabled; -1 = disabled
		chk := widget.NewCheck(d.title, nil)
		chk.SetChecked(pos >= 0)
		chk.OnChanged = func(on bool) {
			switch {
			case on && !slices.Contains(enabled, d.id):
				enabled = append(enabled, d.id)
				save()
			case !on && len(enabled) <= 1:
				rebuild() // keep at least one card
			case !on:
				enabled = slices.DeleteFunc(enabled, func(id string) bool { return id == d.id })
				save()
			}
		}
		up := widget.NewButtonWithIcon("", theme.MoveUpIcon(), func() {
			if pos > 0 {
				enabled[pos-1], enabled[pos] = enabled[pos], enabled[pos-1]
				save()
			}
		})
		down := widget.NewButtonWithIcon("", theme.MoveDownIcon(), func() {
			if pos >= 0 && pos < len(enabled)-1 {
				enabled[pos], enabled[pos+1] = enabled[pos+1], enabled[pos]
				save()
			}
		})
		up.Importance, down.Importance = widget.LowImportance, widget.LowImportance
		if pos <= 0 {
			up.Disable()
		}
		if pos < 0 || pos == len(enabled)-1 {
			down.Disable()
		}
		return container.NewBorder(nil, nil, container.NewHBox(up, down), helpIcon(d.help), chk)
	}
	rebuild = func() {
		objs := make([]fyne.CanvasObject, 0, len(defs))
		for i, id := range enabled { // enabled first, user order
			objs = append(objs, row(byID[id], i))
		}
		for _, d := range defs { // then disabled, registry order
			if !slices.Contains(enabled, d.id) {
				objs = append(objs, row(d, -1))
			}
		}
		list.Objects = objs
		list.Refresh()
	}
	rebuild()

	box := container.NewVBox(
		widget.NewLabelWithStyle("Dashboard cards", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		mutedLabel("Toggle + reorder - changes save instantly."),
		list,
	)
	pop := widget.NewPopUp(box, win.Canvas())
	sz := pop.MinSize()
	cs := win.Canvas().Size()
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(anchor)
	x := pos.X + anchor.Size().Width - sz.Width // right-align under the button
	if x+sz.Width > cs.Width-4 {
		x = cs.Width - sz.Width - 4
	}
	if x < 4 {
		x = 4
	}
	pop.ShowAtPosition(fyne.NewPos(x, pos.Y+anchor.Size().Height+4))
}
