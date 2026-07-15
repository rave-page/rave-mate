package dmx

// Lightcue record/playback driver: the DMX analogue of vroverlay/motion.go. Capture polls the
// universe store at Hz (gated on the store generation so idle ticks are skipped) into a
// lightcue.Recorder; playback samples a lightcue.Player step/hold and emits each universe through
// the shared Art-Net emitter. Takes persist as the frozen contract JSON under <configDir>/
// dmx_recordings. Store polling + JSON writes = DB-bound low-throughput = in-proc carve-out.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/artnet"
	"rave.page/mate/internal/lightcue"
)

// oneMB is the gist string-load budget: a take past it is logged as a warning on save.
const oneMB = 1 << 20

// StoreSource adapts *artnet.Store to lightcue.DMXSource (per-universe Get; zero-filled if unseen).
type StoreSource struct{ store *artnet.Store }

// Snapshot returns the current 512 slots for each requested universe.
func (s StoreSource) Snapshot(universes []uint16) map[uint16][512]byte {
	m := make(map[uint16][512]byte, len(universes))
	for _, u := range universes {
		d, _ := s.store.Get(u) // zero-filled when never seen - keeps the recorded span consistent
		m[u] = d
	}
	return m
}

// StartRecord begins capturing the configured universes at the lightcue Hz. Returns false if the
// DMX plane isn't running (the store is the capture source). Stops any playback first.
func (r *Router) StartRecord() bool {
	r.mu.Lock()
	running := r.running
	r.mu.Unlock()
	if !running {
		return false
	}
	unis := toU16(r.cfgFn().ResolvedUniverses())
	hz := r.lcCfgFn().ResolvedHz()
	r.recMu.Lock()
	r.playing = false
	r.rec = lightcue.NewRecorder(hz)
	r.recStart = time.Now()
	r.recUnis = unis
	r.recPrimed = false
	r.recording = true
	r.recMu.Unlock()
	r.log.Info(source, "lightcue record start", map[string]any{"hz": hz, "universes": len(unis)})
	return true
}

// StopRecord finalizes + saves the take; returns the saved name ("" if nothing captured / failed).
func (r *Router) StopRecord() string {
	r.recMu.Lock()
	if !r.recording || r.rec == nil {
		r.recMu.Unlock()
		return ""
	}
	r.recording = false
	rec := r.rec
	r.rec = nil
	r.recMu.Unlock()

	name := "take-" + time.Now().Format("20060102-150405")
	take := rec.Recording(name)
	if take == nil {
		r.log.Warn(source, "lightcue record empty (no frames captured)", nil)
		return ""
	}
	if r.recDir != "" {
		if err := lightcue.Save(filepath.Join(r.recDir, name+".json"), take); err != nil {
			r.log.Warn(source, "lightcue save failed", map[string]any{"error": err.Error()})
			return ""
		}
	}
	if data, err := lightcue.Marshal(take); err == nil && len(data) > oneMB {
		r.log.Warn(source, "lightcue take exceeds 1MB - lower Hz / shorter take / fewer universes",
			map[string]any{"bytes": len(data), "name": name})
	}
	r.log.Info(source, "lightcue saved", map[string]any{"name": name, "frames": len(take.Frames), "sec": take.Duration})
	return name
}

// Play loads a saved take and starts emitting it through the shared Art-Net emitter.
func (r *Router) Play(name string) bool {
	take, err := r.LoadTake(name)
	if err != nil {
		r.log.Warn(source, "lightcue load failed", map[string]any{"name": name, "error": err.Error()})
		return false
	}
	r.recMu.Lock()
	r.recording = false
	r.player = lightcue.NewPlayer(take)
	r.playStart = time.Now()
	r.playName = name
	r.playing = true
	r.recMu.Unlock()
	r.log.Info(source, "lightcue play", map[string]any{"name": name, "sec": take.Duration})
	return true
}

// StopPlay halts playback.
func (r *Router) StopPlay() {
	r.recMu.Lock()
	r.playing = false
	r.recMu.Unlock()
}

// ToggleLoop flips looping playback; returns the new state.
func (r *Router) ToggleLoop() bool {
	r.recMu.Lock()
	r.loop = !r.loop
	v := r.loop
	r.recMu.Unlock()
	return v
}

func (r *Router) isPlaying() bool {
	r.recMu.Lock()
	defer r.recMu.Unlock()
	return r.playing
}

// runRecord samples the store into the recorder at Hz while recording, skipping idle ticks (no
// store change) but always keeping the first frame (full initial state). One goroutine, ctx-bound.
func (r *Router) runRecord(ctx context.Context) {
	src := StoreSource{store: r.store}
	tick := time.NewTicker(recInterval(r.lcCfgFn().ResolvedHz()))
	defer tick.Stop()
	var lastGen uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.recMu.Lock()
			rec, recording, start, unis, primed := r.rec, r.recording, r.recStart, r.recUnis, r.recPrimed
			r.recMu.Unlock()
			if !recording || rec == nil {
				continue
			}
			gen := r.store.Generation()
			if primed && gen == lastGen {
				continue // idle: nothing changed since the last kept frame
			}
			lastGen = gen
			rec.Observe(time.Since(start).Seconds(), src.Snapshot(unis))
			if !primed {
				r.recMu.Lock()
				r.recPrimed = true
				r.recMu.Unlock()
			}
		}
	}
}

// runPlay samples the player step/hold at Hz and emits each universe. One goroutine, ctx-bound.
func (r *Router) runPlay(ctx context.Context) {
	tick := time.NewTicker(recInterval(r.lcCfgFn().ResolvedHz()))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.recMu.Lock()
			player, playing, start, loop := r.player, r.playing, r.playStart, r.loop
			r.recMu.Unlock()
			if !playing || player == nil {
				continue
			}
			t := time.Since(start).Seconds()
			if t > player.Duration() {
				if !loop {
					r.StopPlay()
					continue
				}
				r.recMu.Lock()
				r.playStart = time.Now()
				r.recMu.Unlock()
				t = 0
			}
			r.mu.Lock()
			em := r.emitter
			r.mu.Unlock()
			if em == nil {
				continue // emitter dial failed - nothing to play out
			}
			for u, data := range player.Sample(t) {
				em.SendDMX(u, data[:])
			}
		}
	}
}

// RecordStatus is the lightcue engine snapshot for the settings/ctl surface.
type RecordStatus struct {
	Recording bool
	Playing   bool
	Loop      bool
	Name      string  // playing take name
	Elapsed   float64 // recording elapsed seconds
	Duration  float64 // playing take length seconds
	Dir       string
}

// RecordStatus returns the live lightcue engine snapshot.
func (r *Router) RecordStatus() RecordStatus {
	r.recMu.Lock()
	defer r.recMu.Unlock()
	st := RecordStatus{Recording: r.recording, Playing: r.playing, Loop: r.loop, Name: r.playName, Dir: r.recDir}
	if r.recording {
		st.Elapsed = time.Since(r.recStart).Seconds()
	}
	if r.player != nil {
		st.Duration = r.player.Duration()
	}
	return st
}

// Takes lists saved take names (newest first), without the .json extension.
func (r *Router) Takes() []string {
	if r.recDir == "" {
		return nil
	}
	ents, err := os.ReadDir(r.recDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

// LoadTake reads a saved take by name (reconstructs full in-memory frames).
func (r *Router) LoadTake(name string) (*lightcue.Recording, error) {
	return lightcue.Load(filepath.Join(r.recDir, name+".json"))
}

// recInterval is the Hz sample period (Hz clamped ≥1).
func recInterval(hz int) time.Duration {
	if hz < 1 {
		hz = 1
	}
	return time.Second / time.Duration(hz)
}

// toU16 converts config universe ints to uint16 (drops out-of-range values).
func toU16(in []int) []uint16 {
	out := make([]uint16, 0, len(in))
	for _, v := range in {
		if v >= 0 && v <= 0xFFFF {
			out = append(out, uint16(v))
		}
	}
	return out
}
