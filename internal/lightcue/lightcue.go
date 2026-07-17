// Package lightcue is the runtime-agnostic DMX lighting-cue recording + playback
// engine - the DMX analogue of vrmotion. No net, no Art-Net: the DMX plane feeds
// full universe snapshots in via a DMXSource and the recorder/player operate purely
// on plain Go values. Recordings persist + publish as the frozen cross-repo contract
// JSON (delta-encoded, step/hold) the VRChat world replays on a synced clock.
package lightcue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// universeSize is the DMX slot count per universe (channels 1..512, slots 0..511).
const universeSize = 512

// contractVersion is the published JSON schema version (v field).
const contractVersion = 1

// maxHz caps decimation at the Art-Net emitter rate (emitter is ≤44Hz).
const maxHz = 44

// Frame is a full snapshot of every recorded universe at time T (seconds since start).
// Map key = 15-bit Art-Net universe; value = that universe's 512 slots (a value array,
// copied on assignment - no aliasing with the source store).
type Frame struct {
	T         float64
	Universes map[uint16][512]byte
}

// Recording is a finalized DMX capture session. BaseUniverse/UniverseCount describe the
// contiguous universe span the contract flattens (flat width = UniverseCount*512).
type Recording struct {
	Name          string
	Hz            int
	Duration      float64
	BaseUniverse  uint16
	UniverseCount int
	Frames        []Frame
}

// DMXSource is implemented by the DMX plane to feed live universe snapshots.
type DMXSource interface {
	// Snapshot returns the current 512 slots for each requested universe.
	Snapshot(universes []uint16) map[uint16][512]byte
}

// Recorder accumulates full-snapshot frames at a target sample rate.
type Recorder struct {
	hz       int
	interval float64
	frames   []Frame
	lastT    float64
	hasLast  bool
}

// NewRecorder builds a recorder at hz samples/sec (hz<=0 → 30, capped at 44 = emitter rate).
func NewRecorder(hz int) *Recorder {
	if hz <= 0 {
		hz = 30
	}
	if hz > maxHz {
		hz = maxHz
	}
	return &Recorder{hz: hz, interval: 1.0 / float64(hz)}
}

// Observe appends a frame at time t (seconds since start) if at least 1/hz has elapsed
// since the last kept frame. Caller's snapshot is copied, not retained.
func (r *Recorder) Observe(t float64, snapshot map[uint16][512]byte) {
	if r.hasLast && t-r.lastT < r.interval {
		return
	}
	cp := make(map[uint16][512]byte, len(snapshot))
	for k, v := range snapshot {
		cp[k] = v // [512]byte is a value array - assignment copies it
	}
	r.frames = append(r.frames, Frame{T: t, Universes: cp})
	r.lastT = t
	r.hasLast = true
}

// Recording finalizes the session; nil if no frames captured.
func (r *Recorder) Recording(name string) *Recording {
	if len(r.frames) == 0 {
		return nil
	}
	base, count := universeSpan(r.frames)
	return &Recording{
		Name:          name,
		Hz:            r.hz,
		Duration:      r.frames[len(r.frames)-1].T,
		BaseUniverse:  base,
		UniverseCount: count,
		Frames:        r.frames,
	}
}

// Reset clears captured frames.
func (r *Recorder) Reset() {
	r.frames = nil
	r.lastT = 0
	r.hasLast = false
}

// universeSpan returns the lowest universe seen + the contiguous count covering min..max.
func universeSpan(frames []Frame) (base uint16, count int) {
	var minU, maxU uint16
	seen := false
	for _, f := range frames {
		for u := range f.Universes {
			if !seen {
				minU, maxU, seen = u, u, true
				continue
			}
			if u < minU {
				minU = u
			}
			if u > maxU {
				maxU = u
			}
		}
	}
	if !seen {
		return 0, 0
	}
	return minU, int(maxU-minU) + 1
}

// Player samples full universe state from a recording using step/hold (DMX is discrete -
// no interpolation; the value between frames is the last frame's value).
type Player struct {
	rec *Recording
}

// NewPlayer wraps a recording for sampling.
func NewPlayer(rec *Recording) *Player {
	return &Player{rec: rec}
}

// Duration returns the recording length in seconds (0 if empty).
func (p *Player) Duration() float64 {
	if p.rec == nil {
		return 0
	}
	return p.rec.Duration
}

// Sample returns a copy of the full universe state of the last frame with T<=t (step/hold).
// Clamps to the first frame before it. Nil if the recording is empty.
func (p *Player) Sample(t float64) map[uint16][512]byte {
	if p.rec == nil || len(p.rec.Frames) == 0 {
		return nil
	}
	fr := p.rec.Frames
	// first frame strictly after t; the active frame is the one before it (step/hold).
	i := sort.Search(len(fr), func(j int) bool { return fr[j].T > t })
	if i == 0 {
		return cloneUniverses(fr[0].Universes) // t before first frame
	}
	return cloneUniverses(fr[i-1].Universes)
}

func cloneUniverses(m map[uint16][512]byte) map[uint16][512]byte {
	cp := make(map[uint16][512]byte, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// contract is the published/persisted JSON shape (the frozen cross-repo contract).
type contract struct {
	V             int             `json:"v"`
	Name          string          `json:"name"`
	Hz            int             `json:"hz"`
	Duration      float64         `json:"duration"`
	BaseUniverse  uint16          `json:"baseUniverse"`
	UniverseCount int             `json:"universeCount"`
	Frames        []contractFrame `json:"frames"`
}

// contractFrame carries the per-frame delta: flatIndex(decimal string) → byte 0..255.
type contractFrame struct {
	T float64        `json:"t"`
	D map[string]int `json:"d"`
}

// toContract delta-encodes a recording: frame 0 D = every non-zero channel; each later
// frame D = only channels whose byte changed vs the previous frame (value 0 = off).
func toContract(rec *Recording) *contract {
	base := rec.BaseUniverse
	count := rec.UniverseCount
	flatWidth := count * universeSize
	prev := make([]byte, flatWidth)
	havePrev := false
	c := &contract{
		V:             contractVersion,
		Name:          rec.Name,
		Hz:            rec.Hz,
		Duration:      rec.Duration,
		BaseUniverse:  base,
		UniverseCount: count,
		Frames:        make([]contractFrame, 0, len(rec.Frames)),
	}
	for _, f := range rec.Frames {
		cur := make([]byte, flatWidth)
		for u, arr := range f.Universes {
			if u < base {
				continue
			}
			off := int(u-base) * universeSize
			if off < 0 || off+universeSize > flatWidth {
				continue // universe outside the recorded span - skip
			}
			copy(cur[off:off+universeSize], arr[:])
		}
		d := map[string]int{}
		for i := 0; i < flatWidth; i++ {
			if !havePrev {
				if cur[i] != 0 {
					d[strconv.Itoa(i)] = int(cur[i])
				}
			} else if cur[i] != prev[i] {
				d[strconv.Itoa(i)] = int(cur[i])
			}
		}
		c.Frames = append(c.Frames, contractFrame{T: f.T, D: d})
		prev = cur
		havePrev = true
	}
	return c
}

// fromContract reconstructs full in-memory frames by applying cumulative deltas onto a
// running flat state, then splitting into per-universe [512]byte snapshots. Values clamp
// 0..255; flatIndex outside [0, UniverseCount*512) is skipped; malformed keys skipped.
func fromContract(c *contract) *Recording {
	count := c.UniverseCount
	if count < 0 {
		count = 0
	}
	flatWidth := count * universeSize
	flat := make([]byte, flatWidth)
	rec := &Recording{
		Name:          c.Name,
		Hz:            c.Hz,
		Duration:      c.Duration,
		BaseUniverse:  c.BaseUniverse,
		UniverseCount: count,
		Frames:        make([]Frame, 0, len(c.Frames)),
	}
	for _, cf := range c.Frames {
		for k, v := range cf.D {
			idx, err := strconv.Atoi(k)
			if err != nil || idx < 0 || idx >= flatWidth {
				continue // malformed / out-of-range - skip this channel
			}
			b := v
			if b < 0 {
				b = 0
			}
			if b > 255 {
				b = 255
			}
			flat[idx] = byte(b)
		}
		rec.Frames = append(rec.Frames, splitFlat(cf.T, flat, c.BaseUniverse, count))
	}
	return rec
}

// splitFlat copies the running flat state into a fresh full-span frame at time t.
func splitFlat(t float64, flat []byte, base uint16, count int) Frame {
	unis := make(map[uint16][512]byte, count)
	for i := 0; i < count; i++ {
		var arr [512]byte
		copy(arr[:], flat[i*universeSize:(i+1)*universeSize])
		unis[base+uint16(i)] = arr
	}
	return Frame{T: t, Universes: unis}
}

// Marshal serializes a recording to the contract JSON (indented). Shared by Save + publish.
func Marshal(rec *Recording) ([]byte, error) {
	return json.MarshalIndent(toContract(rec), "", "  ")
}

// Save writes rec as the contract JSON, atomically (tmp file + rename).
func Save(path string, rec *Recording) error {
	data, err := Marshal(rec)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lightcue-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Load reads a contract JSON file and reconstructs full in-memory frames.
func Load(path string) (*Recording, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c contract
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return fromContract(&c), nil
}
