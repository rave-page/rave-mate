// Package midiemit is a software MIDI test controller: a thread-safe emitter that lazily opens a
// MIDI output port and sends CC / note messages. It backs the webui "MIDI Controller" panel so a
// DJ app (Serato DJ Pro etc.) can MIDI-learn custom mappings against a virtual loopback port
// (LoopBe1 / loopMIDI). Output only - no input, no hardware-unlock spoofing.
package midiemit

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/midi"
)

const (
	source = "midiemit"
	// noteOffDelay is the momentary pad-note hold before the auto Note-Off.
	noteOffDelay = 140 * time.Millisecond
)

// Out is the MIDI output the emitter sends on (satisfied by *midi.Output; fake in tests).
type Out interface {
	Send(status, data1, data2 byte)
	Close()
}

// openFunc opens a port matching substr, returning the sink + its resolved name.
type openFunc func(substr string) (Out, string, error)

// Emitter is the thread-safe software MIDI controller. It opens the configured output port lazily
// on first send (default: match "loopbe" → LoopBe1, else the first available port) and reopens on
// SetPort. Every send is logged for confirmation.
type Emitter struct {
	log    *logbus.Bus
	openFn openFunc

	mu     sync.Mutex
	want   string // desired port-name substring ("" = auto: loopbe/first)
	out    Out
	name   string // resolved active port name
	closed bool
}

// New builds an emitter. want is the initial port substring ("" = auto: LoopBe1/first port).
func New(log *logbus.Bus, want string) *Emitter {
	return &Emitter{log: log, openFn: openMidiOut, want: strings.TrimSpace(want)}
}

// openMidiOut is the production Out factory (winmm on Windows; ErrUnsupported elsewhere): the
// requested substring first, then "loopbe" (LoopBe1), then the first available port.
func openMidiOut(substr string) (Out, string, error) {
	if want := strings.TrimSpace(substr); want != "" {
		if o, err := midi.OpenOutput(want); err == nil {
			return o, o.Name, nil
		}
	}
	if o, err := midi.OpenOutput("loopbe"); err == nil { // LoopBe1 default target
		return o, o.Name, nil
	}
	o, err := midi.OpenOutput("") // any port
	if err != nil {
		return nil, "", fmt.Errorf("no MIDI output port - install loopMIDI or connect a device")
	}
	return o, o.Name, nil
}

// Ports lists available MIDI output port names (empty on error / unsupported platform).
func (e *Emitter) Ports() []string {
	ports, err := midi.OutPorts()
	if err != nil {
		return nil
	}
	return ports
}

// Want returns the configured port substring ("" = auto).
func (e *Emitter) Want() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.want
}

// ActivePort returns the resolved name of the currently open port ("" until first opened).
func (e *Emitter) ActivePort() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.name
}

// SetPort selects a new output port by name (substring match), reopening on the next send. An
// empty name reverts to auto (loopbe/first).
func (e *Emitter) SetPort(name string) {
	e.mu.Lock()
	e.want = strings.TrimSpace(name)
	if e.out != nil {
		e.out.Close()
		e.out, e.name = nil, ""
	}
	e.mu.Unlock()
	if e.log != nil {
		e.log.Info(source, "output port set", map[string]any{"port": name})
	}
}

// ensure opens the port on demand. Caller holds e.mu.
func (e *Emitter) ensure() (Out, error) {
	if e.closed {
		return nil, fmt.Errorf("midiemit: emitter closed")
	}
	if e.out != nil {
		return e.out, nil
	}
	out, name, err := e.openFn(e.want)
	if err != nil {
		return nil, err
	}
	e.out, e.name = out, name
	if e.log != nil {
		e.log.Info(source, "MIDI output open", map[string]any{"port": name})
	}
	return out, nil
}

// send opens the port on demand and emits one short message, logging it.
func (e *Emitter) send(status, d1, d2 byte, tag string) error {
	e.mu.Lock()
	out, err := e.ensure()
	if err != nil {
		e.mu.Unlock()
		if e.log != nil {
			e.log.Warn(source, "no MIDI output", map[string]any{"error": err.Error()})
		}
		return err
	}
	port := e.name
	out.Send(status, d1, d2)
	e.mu.Unlock()
	if e.log != nil {
		e.log.Info(source, tag, map[string]any{"port": port, "status": status, "d1": d1, "d2": d2})
	}
	return nil
}

// SendCC emits a Control Change (0xB0|ch) on channel ch (0-15), controller cc, value val.
func (e *Emitter) SendCC(ch, cc, val byte) error {
	return e.send(0xB0|(ch&0x0F), cc&0x7F, val&0x7F, "cc")
}

// SendNoteOn emits a Note On (0x90|ch) at the given note + velocity.
func (e *Emitter) SendNoteOn(ch, note, vel byte) error {
	return e.send(0x90|(ch&0x0F), note&0x7F, vel&0x7F, "noteOn")
}

// SendNoteOff emits a Note Off (0x80|ch) at the given note (velocity 0).
func (e *Emitter) SendNoteOff(ch, note byte) error {
	return e.send(0x80|(ch&0x0F), note&0x7F, 0, "noteOff")
}

// TriggerPad emits a momentary pad hit: Note On now, Note Off after a bounded delay.
func (e *Emitter) TriggerPad(ch, note, vel byte) error {
	if err := e.SendNoteOn(ch, note, vel); err != nil {
		return err
	}
	debuglog.Go(e.log, source, func() {
		time.Sleep(noteOffDelay)
		_ = e.SendNoteOff(ch, note)
	})
	return nil
}

// sweepStep is the delay between the ramp values a Sweep sends (so a DJ app's MIDI-learn reliably
// catches the movement of a control it's arming).
const sweepStep = 18 * time.Millisecond

// PulseCC emits a momentary CC hit: value 127 now, value 0 after a bounded delay. Used for the
// mixer Play/Cue controls, which round-trip as booleans on the receive side (custom.go).
func (e *Emitter) PulseCC(ch, cc byte) error {
	if err := e.SendCC(ch, cc, 127); err != nil {
		return err
	}
	debuglog.Go(e.log, source, func() {
		time.Sleep(noteOffDelay)
		_ = e.SendCC(ch, cc, 0)
	})
	return nil
}

// SweepCC ramps a CC 0 -> 127 -> 0 in bounded steps so a DJ app arming MIDI-learn on the target
// control catches the movement. The first step sends synchronously (surfacing a no-port error);
// the rest run on a background ticker.
func (e *Emitter) SweepCC(ch, cc byte) error {
	ramp := []byte{0, 21, 42, 64, 85, 106, 127, 106, 85, 64, 42, 21, 0}
	if err := e.SendCC(ch, cc, ramp[0]); err != nil {
		return err
	}
	debuglog.Go(e.log, source, func() {
		for _, v := range ramp[1:] {
			time.Sleep(sweepStep)
			_ = e.SendCC(ch, cc, v)
		}
	})
	return nil
}

// Panic silences everything: All-Notes-Off (CC 123) on channel ch + a Note Off across [lo,hi].
func (e *Emitter) Panic(ch, lo, hi byte) {
	_ = e.SendCC(ch, 123, 0)
	for n := lo; n <= hi; n++ {
		_ = e.SendNoteOff(ch, n)
		if n == 0xFF { // guard byte wrap when hi == 255
			break
		}
	}
}

// Close releases the output port (idempotent).
func (e *Emitter) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.out != nil {
		e.out.Close()
		e.out, e.name = nil, ""
	}
}
