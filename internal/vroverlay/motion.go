package vroverlay

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/osc"
	"rave.page/mate/internal/vmc"
	"rave.page/mate/internal/vrmotion"
)

// motion records SteamVR tracker/HMD poses and replays them to VRChat over OSC (P3). Capture +
// playback are driven from the Manager's VR goroutine (single-threaded OpenVR access); OSC sends are
// best-effort UDP. Recordings persist as JSON under the data dir.
type motion struct {
	log     *logbus.Bus
	poses   func() map[int]vrmotion.Pose // capture source (runtime.TrackerPoses)
	addr    func() string                // OSC target addr from config
	vmcAddr func() string                // VMC receiver addr from config
	vmcLive func() bool                  // stream live/played poses to a VMC receiver (VTuber)

	mu        sync.Mutex
	dir       string
	rec       *vrmotion.Recorder
	recStart  time.Time
	recording bool

	player    *vrmotion.Player
	playStart time.Time
	playName  string
	playing   bool
	loop      bool

	osc     *osc.Client
	oscAddr string

	vmc      *vmc.Sender
	vmcTgt   string
	vmcStart time.Time
}

func newMotion(log *logbus.Bus, poses func() map[int]vrmotion.Pose, addr, vmcAddr func() string, vmcLive func() bool) *motion {
	dir := ""
	if p, err := config.DataPath("vr_recordings.x"); err == nil {
		dir = filepath.Join(filepath.Dir(p), "vr_recordings")
		_ = os.MkdirAll(dir, 0o755)
	}
	return &motion{log: log, poses: poses, addr: addr, vmcAddr: vmcAddr, vmcLive: vmcLive, dir: dir}
}

// StartRecord begins capturing poses at 30 Hz (stops any playback first).
func (m *motion) StartRecord() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.playing = false
	m.rec = vrmotion.NewRecorder(30)
	m.recStart = time.Now()
	m.recording = true
	m.log.Info(logTag, "motion record start", nil)
}

// StopRecord finalizes + saves the capture; returns the saved name ("" if nothing captured).
func (m *motion) StopRecord() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.recording || m.rec == nil {
		return ""
	}
	m.recording = false
	name := "take-" + time.Now().Format("0102-150405")
	r := m.rec.Recording(name)
	m.rec = nil
	if r == nil {
		m.log.Warn(logTag, "motion record empty (no poses captured)", nil)
		return ""
	}
	if m.dir != "" {
		if err := vrmotion.Save(filepath.Join(m.dir, name+".json"), r); err != nil {
			m.log.Warn(logTag, "motion save failed", map[string]any{"error": err.Error()})
			return ""
		}
	}
	m.log.Info(logTag, "motion saved", map[string]any{"name": name, "frames": len(r.Frames), "sec": r.Duration})
	return name
}

// Play loads a saved recording and starts streaming it to VRChat over OSC.
func (m *motion) Play(name string) {
	rec, err := vrmotion.Load(filepath.Join(m.dir, name+".json"))
	if err != nil {
		m.log.Warn(logTag, "motion load failed", map[string]any{"name": name, "error": err.Error()})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recording = false
	m.player = vrmotion.NewPlayer(rec)
	m.playStart = time.Now()
	m.playName = name
	m.playing = true
	m.log.Info(logTag, "motion play", map[string]any{"name": name, "sec": rec.Duration})
}

// Stop halts playback.
func (m *motion) Stop() {
	m.mu.Lock()
	m.playing = false
	m.mu.Unlock()
}

// ToggleLoop flips looping playback.
func (m *motion) ToggleLoop() {
	m.mu.Lock()
	m.loop = !m.loop
	m.mu.Unlock()
}

// tick samples a pose (while recording) or emits the next playback frame over OSC. Called from the
// Manager VR goroutine at the motion cadence.
func (m *motion) tick() {
	m.mu.Lock()
	rec, recording, recStart := m.rec, m.recording, m.recStart
	player, playing, playStart, loop := m.player, m.playing, m.playStart, m.loop
	m.mu.Unlock()

	live := m.vmcLive != nil && m.vmcLive()

	if recording && rec != nil {
		if poses := m.poses(); len(poses) > 0 {
			rec.Observe(time.Since(recStart).Seconds(), poses)
			if live {
				m.sendVMC(poses) // see yourself in the VTuber renderer while recording
			}
		}
		return
	}
	if !playing || player == nil {
		if live { // live VTubing: stream current poses straight to the VMC renderer
			if poses := m.poses(); len(poses) > 0 {
				m.sendVMC(poses)
			}
		}
		return
	}
	t := time.Since(playStart).Seconds()
	if t > player.Duration() {
		if !loop {
			m.Stop()
			return
		}
		m.mu.Lock()
		m.playStart = time.Now()
		m.mu.Unlock()
		t = 0
	}
	sample := player.Sample(t)
	m.emit(sample)
	if live { // also drive the VTuber avatar with the replayed take
		m.sendVMC(sample)
	}
}

// sendVMC streams one frame to the VMC receiver (lazy-dials; reconnects on addr change).
func (m *motion) sendVMC(sample map[int]vrmotion.Pose) {
	if len(sample) == 0 || m.vmcAddr == nil {
		return
	}
	want := m.vmcAddr()
	m.mu.Lock()
	if m.vmc == nil || m.vmcTgt != want {
		if m.vmc != nil {
			_ = m.vmc.Close()
			m.vmc = nil
		}
		s, err := vmc.New(want)
		if err != nil {
			m.mu.Unlock()
			m.log.Warn(logTag, "VMC dial failed", map[string]any{"addr": want, "error": err.Error()})
			return
		}
		m.vmc, m.vmcTgt, m.vmcStart = s, want, time.Now()
	}
	s, t0 := m.vmc, m.vmcStart
	m.mu.Unlock()
	s.SendFrame(time.Since(t0).Seconds(), sample)
}

// emit streams one sampled frame to VRChat: head (key 0) + trackers (1..8), pos + ZXY-euler rotation.
func (m *motion) emit(sample map[int]vrmotion.Pose) {
	if len(sample) == 0 {
		return
	}
	c := m.client()
	if c == nil {
		return
	}
	for key, p := range sample {
		rx, ry, rz := osc.QuatToEulerZXY(p.Rot[0], p.Rot[1], p.Rot[2], p.Rot[3])
		var pa, ra string
		var pargs, rargs []any
		if key == 0 {
			pa, pargs = osc.HeadPosition(p.Pos[0], p.Pos[1], p.Pos[2])
			ra, rargs = osc.HeadRotation(rx, ry, rz)
		} else {
			pa, pargs = osc.TrackerPosition(key, p.Pos[0], p.Pos[1], p.Pos[2])
			ra, rargs = osc.TrackerRotation(key, rx, ry, rz)
		}
		_ = c.Send(pa, pargs...)
		_ = c.Send(ra, rargs...)
	}
}

// client lazily (re)dials the OSC socket; reconnects if the configured addr changed.
func (m *motion) client() *osc.Client {
	want := m.addr()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.osc != nil && m.oscAddr == want {
		return m.osc
	}
	if m.osc != nil {
		_ = m.osc.Close()
		m.osc = nil
	}
	c, err := osc.New(want)
	if err != nil {
		m.log.Warn(logTag, "OSC dial failed", map[string]any{"addr": want, "error": err.Error()})
		return nil
	}
	m.osc, m.oscAddr = c, want
	return c
}

// list returns saved recording names (newest first), without the .json extension.
func (m *motion) list() []string {
	if m.dir == "" {
		return nil
	}
	ents, err := os.ReadDir(m.dir)
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

// status is a one-line motion state summary for the menu.
func (m *motion) status() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.recording:
		return fmt.Sprintf("RECORDING %.0fs", time.Since(m.recStart).Seconds())
	case m.playing:
		return "PLAYING " + m.playName
	default:
		return "idle"
	}
}

func (m *motion) isRecording() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.recording }
func (m *motion) isPlaying() bool   { m.mu.Lock(); defer m.mu.Unlock(); return m.playing }
func (m *motion) looping() bool     { m.mu.Lock(); defer m.mu.Unlock(); return m.loop }

// close releases the OSC socket.
func (m *motion) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.osc != nil {
		_ = m.osc.Close()
		m.osc = nil
	}
	if m.vmc != nil {
		_ = m.vmc.Close()
		m.vmc = nil
	}
}
