// Package vrmotion is the runtime-agnostic motion-capture recording + playback
// engine. No cgo, no OpenVR: the VR layer feeds poses in via PoseSource and the
// recorder/player operate purely on plain Go values.
package vrmotion

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// Pose is a single tracker transform: position + quaternion rotation (x,y,z,w).
type Pose struct {
	Pos [3]float32 `json:"pos"`
	Rot [4]float32 `json:"rot"`
}

// Frame is a snapshot of all tracked poses at time T (seconds since start).
// Map key = tracker id (1..8, or 0 = head).
type Frame struct {
	T     float64      `json:"t"`
	Poses map[int]Pose `json:"poses"`
}

// Recording is a finalized capture session.
type Recording struct {
	Name     string  `json:"name"`
	Hz       int     `json:"hz"`
	Frames   []Frame `json:"frames"`
	Duration float64 `json:"duration"`
}

// PoseSource is implemented by the VR layer to feed live tracker poses.
type PoseSource interface {
	// Trackers returns current poses keyed by tracker id.
	Trackers() map[int]Pose
}

// Recorder accumulates frames at a target sample rate.
type Recorder struct {
	hz       int
	interval float64
	frames   []Frame
	lastT    float64
	hasLast  bool
}

// NewRecorder builds a recorder at hz samples/sec (hz<=0 → 30).
func NewRecorder(hz int) *Recorder {
	if hz <= 0 {
		hz = 30
	}
	return &Recorder{hz: hz, interval: 1.0 / float64(hz)}
}

// Observe appends a frame at time t (seconds since start) if at least 1/hz has
// elapsed since the last kept frame. Caller's map is copied, not retained.
func (r *Recorder) Observe(t float64, poses map[int]Pose) {
	if r.hasLast && t-r.lastT < r.interval {
		return
	}
	cp := make(map[int]Pose, len(poses))
	for k, v := range poses {
		cp[k] = v
	}
	r.frames = append(r.frames, Frame{T: t, Poses: cp})
	r.lastT = t
	r.hasLast = true
}

// Recording finalizes the session; nil if no frames captured.
func (r *Recorder) Recording(name string) *Recording {
	if len(r.frames) == 0 {
		return nil
	}
	return &Recording{
		Name:     name,
		Hz:       r.hz,
		Frames:   r.frames,
		Duration: r.frames[len(r.frames)-1].T,
	}
}

// Reset clears captured frames.
func (r *Recorder) Reset() {
	r.frames = nil
	r.lastT = 0
	r.hasLast = false
}

// Player samples poses from a recording with interpolation.
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

// Sample returns interpolated poses at time t: linear on position, nlerp on
// rotation. Clamps before first / after last frame. Nil if recording empty.
func (p *Player) Sample(t float64) map[int]Pose {
	if p.rec == nil || len(p.rec.Frames) == 0 {
		return nil
	}
	fr := p.rec.Frames
	if t <= fr[0].T {
		return clonePoses(fr[0].Poses)
	}
	last := fr[len(fr)-1]
	if t >= last.T {
		return clonePoses(last.Poses)
	}
	// first frame with T > t
	i := sort.Search(len(fr), func(j int) bool { return fr[j].T > t })
	a, b := fr[i-1], fr[i]
	span := b.T - a.T
	var u float64
	if span > 0 {
		u = (t - a.T) / span
	}
	out := make(map[int]Pose, len(a.Poses))
	for id, pa := range a.Poses {
		pb, ok := b.Poses[id]
		if !ok {
			out[id] = pa
			continue
		}
		out[id] = lerpPose(pa, pb, float32(u))
	}
	// trackers present only in b
	for id, pb := range b.Poses {
		if _, ok := a.Poses[id]; !ok {
			out[id] = pb
		}
	}
	return out
}

func clonePoses(m map[int]Pose) map[int]Pose {
	cp := make(map[int]Pose, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

func lerpPose(a, b Pose, u float32) Pose {
	var p Pose
	for i := 0; i < 3; i++ {
		p.Pos[i] = a.Pos[i] + (b.Pos[i]-a.Pos[i])*u
	}
	p.Rot = nlerp(a.Rot, b.Rot, u)
	return p
}

// nlerp normalized-lerps two quaternions; picks the shorter arc via dot sign.
func nlerp(a, b [4]float32, u float32) [4]float32 {
	dot := a[0]*b[0] + a[1]*b[1] + a[2]*b[2] + a[3]*b[3]
	var s float32 = 1
	if dot < 0 {
		s = -1
	}
	var q [4]float32
	for i := 0; i < 4; i++ {
		q[i] = a[i] + (s*b[i]-a[i])*u
	}
	n := float32(math.Sqrt(float64(q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3])))
	if n == 0 {
		return [4]float32{0, 0, 0, 1}
	}
	for i := 0; i < 4; i++ {
		q[i] /= n
	}
	return q
}

// Save writes rec as indented JSON, atomically (tmp file + rename).
func Save(path string, rec *Recording) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vrmotion-*.tmp")
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

// Load reads a recording from a JSON file.
func Load(path string) (*Recording, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec Recording
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}
