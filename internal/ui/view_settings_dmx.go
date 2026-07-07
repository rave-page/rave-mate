package ui

// DMX / VRSL settings card: Art-Net ingest → VRSL video grid (Spout/PNG) + optional re-emit.
// Education-first: every control carries a "?" tooltip explaining DMX/Art-Net/universes/VRSL
// for newcomers.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// dmxCard configures the DMX plane (module "dmx").
func (u *UI) dmxCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.DMX

	// Status dot (the per-universe traffic readout lives on the Live tab's DMX card).
	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			return
		}
		if u.svc.DMX == nil {
			s.set(colBrandAmber, "unavailable")
			return
		}
		snap := u.svc.DMX.Status()
		receiving := false
		for _, un := range snap.Universes {
			if un.PPS > 0 {
				receiving = true
			}
		}
		switch {
		case !snap.Running:
			s.set(colBrandAmber, "not running (port busy?)")
		case receiving:
			s.set(colBrandMint, "receiving DMX")
		case len(snap.Universes) > 0:
			s.set(colBrandMint, fmt.Sprintf("idle - %d universe(s) seen", len(snap.Universes)))
		default:
			s.set(colBrandAmber, "listening - no DMX yet")
		}
	})
	toggle := u.moduleToggle("dmx", &f.Enabled)

	listen := newEntry()
	listen.SetPlaceHolder(":6454")
	listen.SetText(f.ListenAddr)
	listen.OnChanged = func(s string) { f.ListenAddr = s; u.saveCfg() }

	unis := newEntry()
	unis.SetPlaceHolder("0 (comma-separated, e.g. 0,1,2)")
	unis.SetText(intsToCSV(f.Universes))
	unis.OnChanged = func(s string) { f.Universes = csvToInts(s); u.saveCfg() }

	gridChk := widget.NewCheck("Render the VRSL video grid", func(v bool) { f.Grid.Enabled = v; u.saveCfg() })
	gridChk.SetChecked(f.Grid.Enabled)

	modeSel := widget.NewSelect([]string{"mono", "rgb9"}, func(s string) { f.Grid.Mode = s; u.saveCfg() })
	modeSel.SetSelected(orDefault(f.Grid.Mode, "mono"))

	spoutName := newEntry()
	spoutName.SetPlaceHolder("rave-mate-vrsl")
	spoutName.SetText(f.Grid.SpoutName)
	spoutName.OnChanged = func(s string) { f.Grid.SpoutName = s; u.saveCfg() }

	fps := newEntry()
	fps.SetPlaceHolder("30")
	if f.Grid.FPSCap > 0 {
		fps.SetText(strconv.Itoa(f.Grid.FPSCap))
	}
	fps.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 60 {
			f.Grid.FPSCap = n
			u.saveCfg()
		}
	}

	reEmit := widget.NewCheck("Re-emit received DMX over Art-Net", func(v bool) { f.ReEmit = v; u.saveCfg() })
	reEmit.SetChecked(f.ReEmit)

	emitTarget := newEntry()
	emitTarget.SetPlaceHolder("255.255.255.255:6454 (broadcast)")
	emitTarget.SetText(f.EmitTarget)
	emitTarget.OnChanged = func(s string) { f.EmitTarget = s; u.saveCfg() }

	body := container.NewVBox(
		mutedLabel("Receive lighting data from a console or lighting app over Art-Net and turn it into a VRSL video grid a VRChat world can decode - plus optionally forward the DMX on to another machine."),
		labelWithHelp("How to connect a console",
			"DMX is the standard lighting-control protocol: 512 dimmer values (channels) per 'universe'. Art-Net carries those universes over the network as UDP on port 6454.\n\nIn your lighting software (a console, QLC+, xLights, …) add an Art-Net output node, point it at this machine's IP address (or broadcast), universe number as below, and start sending. rave-mate also answers Art-Net discovery polls, so many consoles list it automatically."),

		widget.NewSeparator(),
		container.NewBorder(nil, nil, labelWithHelp("Listen address",
			"Where rave-mate receives Art-Net. Default binds every network interface on UDP port 6454 (the fixed Art-Net port). Enter ip:port to bind one interface. Toggle the feature off/on to apply."), nil, listen),
		container.NewBorder(nil, nil, labelWithHelp("Universes",
			"A universe is one block of 512 DMX channels - bigger rigs use several. List the Art-Net universe numbers to render (0-based port addresses), e.g. 0,1,2. Empty = universe 0 (mono) or 0–8 (RGB mode). Toggle off/on to apply."), nil, unis),

		widget.NewSeparator(),
		container.NewHBox(gridChk, helpIcon("VRSL (VR Stage Lighting) is the standard for club lighting in VRChat: light values are encoded as pixel blocks in the video stream, and the world's shaders decode them back into lights. rave-mate renders that pixel grid directly - no extra grid tool needed. Capture it in OBS, stream it, and a VRSL-enabled world lights up.")),
		container.NewBorder(nil, nil, labelWithHelp("Grid mode",
			"mono: one grey block per universe, 1 channel per cell - the common setup.\nrgb9: the extended 9-universe layout - universes 1-3 become the red channel, 4-6 green, 7-9 blue, packed into 3 colour blocks. Use only if your world's VRSL is set to extended RGB."), nil, modeSel),
		container.NewBorder(nil, nil, labelWithHelp("Sender name",
			"The grid is published as a live video feed over Spout (a GPU frame-sharing standard on Windows). In OBS add a 'Spout2 Capture' source and pick this name. Builds without Spout write the grid to a PNG file instead (point an OBS Image source at it)."), nil, spoutName),
		container.NewBorder(nil, nil, labelWithHelp("Max grid FPS",
			"Upper limit for grid re-renders per second (1–60, default 30). The grid only re-renders when a DMX value actually changed, so the cap matters only under constant animation."), nil, fps),
		mutedLabel("Color accuracy: the grid is written in LINEAR color (VRSL 2.7.0+ decodes linear). Keep the capture/encode chain gamma-neutral - an sRGB conversion anywhere skews every light value."),

		widget.NewSeparator(),
		container.NewHBox(reEmit, helpIcon("Forwards every universe received here back out over Art-Net - chain rave-mate in front of another machine or lighting tool that also needs the DMX feed. Sent at max 44 packets/s per universe with a 1/s keep-alive, per the Art-Net spec.")),
		container.NewBorder(nil, nil, labelWithHelp("Re-emit target",
			"ip:port to forward to. Empty = broadcast to the whole network on port 6454 (any listening node picks it up). Enter a specific machine's IP to unicast."), nil, emitTarget),

		widget.NewSeparator(),
		mutedLabel("Live per-universe traffic lives on the Live tab (DMX card - enable it via Edit dashboard). Also `rave-mate ctl dmx-status`."),
	)
	return featureCard("DMX / VRSL", "Art-Net lighting in → VRSL video grid for VRChat worlds (+ optional DMX forward).", toggle, st, body)
}

// buildDMXStatusContent is the "dmx" Live card: per-universe Art-Net traffic + the grid
// sink state. nil when the DMX plane is unavailable.
func (u *UI) buildDMXStatusContent() fyne.CanvasObject {
	if u.svc.DMX == nil {
		return nil
	}
	traffic := widget.NewLabel("")
	traffic.Wrapping = fyne.TextWrapWord
	update := func() {
		snap := u.svc.DMX.Status()
		var b strings.Builder
		if !snap.Running {
			b.WriteString("not running (feature off, or port busy)\n")
		}
		for _, un := range snap.Universes {
			fmt.Fprintf(&b, "universe %d: %.1f pkt/s from %s\n", un.Universe, un.PPS, un.SourceIP)
		}
		if snap.GridBackend != "" {
			fmt.Fprintf(&b, "grid → %s (%d frames)", snap.GridBackend, snap.GridFrames)
			if snap.GridErr != "" {
				fmt.Fprintf(&b, " - ERROR %s", snap.GridErr)
			}
		}
		s := strings.TrimRight(b.String(), "\n")
		if s == "" {
			s = "listening - no DMX yet"
		}
		traffic.SetText(s)
	}
	update()
	tick := time.NewTicker(2 * time.Second)
	u.closers = append(u.closers, tick.Stop)
	goUI("live-dmx", func() {
		for range tick.C {
			fyne.Do(update)
		}
	})
	return traffic
}

// dmxMidiCard configures the DMX→MIDI VRChat bridge (module "dmxmidi").
func (u *UI) dmxMidiCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.DMXMIDI

	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			return
		}
		if u.svc.DMXMIDI == nil {
			s.set(colBrandAmber, "unavailable")
			return
		}
		snap := u.svc.DMXMIDI.Status()
		switch {
		case !snap.Running:
			s.set(colBrandAmber, "not running (MIDI port missing?)")
		case snap.Sent > 0:
			s.set(colBrandMint, fmt.Sprintf("→ %s · %d msg(s) · %d queued · cap %d/s", snap.Port, snap.Sent, snap.Backlog, snap.Rate))
		default:
			s.set(colBrandMint, fmt.Sprintf("ready on %s - waiting for DMX changes", snap.Port))
		}
	})
	toggle := u.moduleToggle("dmxmidi", &f.Enabled)

	device := newEntry()
	device.SetPlaceHolder("first MIDI output port")
	device.SetText(f.Device)
	device.OnChanged = func(s string) { f.Device = s; u.saveCfg() }

	unis := newEntry()
	unis.SetPlaceHolder("0 (comma-separated, max 4)")
	unis.SetText(intsToCSV(f.Universes))
	unis.OnChanged = func(s string) { f.Universes = csvToInts(s); u.saveCfg() }

	rate := newEntry()
	rate.SetPlaceHolder("400")
	if f.MaxPerSecond > 0 {
		rate.SetText(strconv.Itoa(f.MaxPerSecond))
	}
	rate.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n >= 50 && n <= 1000 {
			f.MaxPerSecond = n
			u.saveCfg()
		}
	}

	body := container.NewVBox(
		mutedLabel("Turn received DMX into MIDI control-change messages on a virtual MIDI port, so a VRChat world with a MIDI listener reacts to your lighting console - on this machine only."),
		labelWithHelp("How to set it up",
			"1. Create a virtual MIDI port (loopMIDI is the standard free tool).\n2. Enter its name below and enable the bridge.\n3. Launch VRChat with the launch option --midi=<port name>.\n4. Enable the DMX / VRSL feature above - it receives the Art-Net this bridge forwards.\n\nMIDI reaches only your own client (VRChat design); the world must relay values to other players itself."),

		widget.NewSeparator(),
		container.NewBorder(nil, nil, labelWithHelp("MIDI output port",
			"Name (or part of it) of the virtual MIDI port VRChat listens on. Empty = the first output port on the system. Toggle off/on to apply."), nil, device),
		container.NewBorder(nil, nil, labelWithHelp("Universes",
			"Art-Net universes to bridge, in order (e.g. 0,1). Each universe's 512 channels map onto 4 MIDI channels × 128 CC numbers; DMX value 0–255 becomes CC value 0–127. Max 4 universes (2048 addresses = the whole MIDI CC space)."), nil, unis),
		container.NewBorder(nil, nil, labelWithHelp("Max messages/s",
			"Hard safety cap (50–1000, default 400). VRChat crashes when more than ~128 MIDI events arrive in one frame, so the bridge sends only channels whose value actually changed, merges rapid changes into one message, and paces everything under this cap - 1000/s stays safe even at 10 fps in-headset."), nil, rate),
		mutedLabel("Smooth fades are halved on the wire: DMX has 256 steps, MIDI CC 127 - sub-step changes are skipped, cutting traffic without visible loss."),
	)
	return featureCard("DMX → VRChat MIDI", "Rate-limited MIDI CC bridge for VRChat worlds with a MIDI listener (VRSL/MDMX local preview).", toggle, st, body)
}

// intsToCSV renders a universe list as "0,1,2" ("" for empty).
func intsToCSV(v []int) string {
	if len(v) == 0 {
		return ""
	}
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// csvToInts parses "0, 1,2" into a universe list (invalid/negative entries dropped).
func csvToInts(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n >= 0 && n <= 0x7FFF {
			out = append(out, n)
		}
	}
	return out
}
