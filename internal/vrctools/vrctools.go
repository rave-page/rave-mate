// Package vrctools coordinates the VRChat companion features: it tails the VRChat log into a
// location timeline (vrcloc), auto-organizes screenshots (vrcphotos) and camera paths
// (vrccampaths) per world/instance, and publishes the current location + organize counts on the
// event bus so a paired instance (e.g. this VR PC observed from a desk PC) can watch what it does.
package vrctools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rave.page/mate/internal/cameraosc"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/obscontrol"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/vrccampaths"
	"rave.page/mate/internal/vrcloc"
	"rave.page/mate/internal/vrclog"
	"rave.page/mate/internal/vrcphotos"
)

// restoreDelay lets VRChat's OSC endpoint come up after a world join before we push a dolly restore.
const restoreDelay = 5 * time.Second

const logTag = "vrctools"

// Bus topics: a paired instance can subscribe to follow this node's VRChat activity.
const (
	TopicLocation = "vrc.location" // payload: vrcloc.Location (current world/instance)
	TopicOrganize = "vrc.organize" // payload: organizeCount
)

type organizeCount struct {
	Photos int `json:"photos"`
	Paths  int `json:"paths"`
}

// Service is the VRChat-tools coordinator (lifecycle via Start).
type Service struct {
	log     *logbus.Bus
	bus     *eventbus.Bus
	cfg     func() config.VRCToolsFeature
	tl      *vrcloc.Timeline
	dataDir string                // app data dir (default cam-path backup root)
	evSrc   vrcphotos.EventSource // rave.page event lookup (photo organize key); may be nil

	ctx          context.Context // set in Start; guards deferred auto-restore
	lastInstance string          // last published instance id (telemetry de-dup)

	// Live-set state cached from the bus (drives auto-restore). Guarded by mu.
	mu         sync.Mutex
	obsStream  map[string]bool // OBS source id → streaming
	obsRec     map[string]bool // OBS source id → recording
	twitchLive bool
}

// New builds the coordinator. dataDir holds the persisted location timeline + cam-path backups. bus may be nil.
func New(log *logbus.Bus, bus *eventbus.Bus, cfg func() config.VRCToolsFeature, dataDir string) *Service {
	return &Service{
		log: log, bus: bus, cfg: cfg, dataDir: dataDir,
		tl:        vrcloc.NewTimeline(vrcloc.DefaultTimelinePath(dataDir)),
		obsStream: map[string]bool{},
		obsRec:    map[string]bool{},
	}
}

// SetEventSource wires the rave.page event lookup used to file photos by event (primary key).
// nil-safe: without it, organizing falls back to the world/instance timeline. Set once at startup
// (before Start), so no locking.
func (s *Service) SetEventSource(ev vrcphotos.EventSource) { s.evSrc = ev }

// photoEventSource returns the event source when event-organizing is enabled, else nil.
func (s *Service) photoEventSource() vrcphotos.EventSource {
	if s.cfg().OrganizeByEvent {
		return s.evSrc
	}
	return nil
}

// Photos lists every screenshot (organized + un-organizable), labeled by event/world, newest first.
func (s *Service) Photos() []vrcphotos.Photo {
	return vrcphotos.ScanAll(s.PhotosDir(), s.tl, s.photoEventSource())
}

// Timeline exposes the location history (for the photo/cam-path consumers + UI).
func (s *Service) Timeline() *vrcloc.Timeline { return s.tl }

// CurrentWorld returns the world/instance the user is in now (false if unknown).
func (s *Service) CurrentWorld() (vrcloc.Location, bool) { return s.tl.Current() }

// PhotosDir / CamPathsDir resolve the configured dirs or VRChat defaults.
func (s *Service) PhotosDir() string {
	if d := s.cfg().PhotosDir; d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Pictures", "VRChat")
	}
	return ""
}

func (s *Service) CamPathsDir() string {
	if d := s.cfg().CamPathsDir; d != "" {
		return d
	}
	return vrccampaths.DefaultDir()
}

// CamPaths lists camera paths (summarized + world-tagged), newest first.
func (s *Service) CamPaths() []vrccampaths.Path {
	return vrccampaths.Scan(s.CamPathsDir(), s.tl)
}

// PhotosOrganizedRoot is the folder holding organized screenshots (Organized/<event-or-world>/).
func (s *Service) PhotosOrganizedRoot() string {
	return vrcphotos.New(s.PhotosDir(), nil, vrcphotos.Copy, nil, nil).OrganizedRoot()
}

// CamPathBackupDir resolves the configured cam-path backup dir or <dataDir>/campath_backups.
// Kept outside CamPathsDir so backup copies don't pollute the organized cam-path listing (Scan).
func (s *Service) CamPathBackupDir() string {
	if d := s.cfg().CamPathBackupDir; d != "" {
		return d
	}
	return filepath.Join(s.dataDir, "campath_backups")
}

// LoadCamPath loads a camera path into VRChat over OSC (/dolly/Import + /dolly/Play), applies the
// default camera preset (if any), then backs it up for the current world (when AutoBackup is on).
func (s *Service) LoadCamPath(file string) error { return s.loadCamPath(file, true) }

// loadCamPath imports+plays a path, applies the default preset, and (when backup && the toggle is on)
// copies it into the current world's backup slot. Auto-restore calls with backup=false so restoring
// a backup can't overwrite that backup with itself - the feedback-loop guard.
func (s *Service) loadCamPath(file string, backup bool) error {
	f := s.cfg()
	if err := vrccampaths.Load(f.OSCAddr, file); err != nil {
		return err
	}
	s.applyDefaultCamPreset(f)
	if backup && f.AutoBackupCamPaths {
		s.backupCamPath(file)
	}
	return nil
}

// backupCamPath copies the just-played path into the current world's backup slot (best-effort;
// needs a known world to key on).
func (s *Service) backupCamPath(file string) {
	loc, ok := s.tl.Current()
	if !ok || loc.WorldID == "" {
		return
	}
	if _, err := vrccampaths.Backup(s.CamPathBackupDir(), file, loc.WorldID, loc.WorldName); err != nil {
		s.logMsg("campath", fmt.Sprintf("backup failed: %v", err))
	} else {
		s.logMsg("campath", "backed up path for "+loc.WorldName)
	}
}

// applyDefaultCamPreset pushes the configured default look preset over /usercamera OSC (best-effort).
func (s *Service) applyDefaultCamPreset(f config.VRCToolsFeature) {
	if f.DefaultCamPreset == "" {
		return
	}
	if p, ok := cameraosc.PresetByName(f.AllCamPresets(), f.DefaultCamPreset); ok {
		if err := cameraosc.Apply(f.OSCAddr, p); err != nil {
			s.logf("campath")(fmt.Sprintf("preset apply failed: %v", err))
		}
	}
}

// InstallBuiltinPaths writes the shipped DJ-event dolly paths into the camera-paths dir.
func (s *Service) InstallBuiltinPaths() (int, string, error) {
	return vrccampaths.InstallBuiltins(s.CamPathsDir())
}

// ApplyCamPreset pushes a named preset to VRChat's camera now (manual apply).
func (s *Service) ApplyCamPreset(name string) error {
	f := s.cfg()
	p, ok := cameraosc.PresetByName(f.AllCamPresets(), name)
	if !ok {
		return fmt.Errorf("preset not found: %s", name)
	}
	return cameraosc.Apply(f.OSCAddr, p)
}

// OrganizeNow runs both organizers once and returns the counts.
func (s *Service) OrganizeNow() (photos, paths int) {
	f := s.cfg()
	if f.OrganizePhotos {
		mode := vrcphotos.Copy
		if f.PhotoMove {
			mode = vrcphotos.Move
		}
		photos = vrcphotos.New(s.PhotosDir(), s.tl, mode, s.photoEventSource(), s.logf("photo")).Scan()
	}
	if f.OrganizeCamPaths {
		paths = vrccampaths.New(s.CamPathsDir(), s.tl, f.CamPathMove, s.logf("campath")).Organize()
	}
	if (photos > 0 || paths > 0) && s.bus != nil {
		if raw, err := json.Marshal(organizeCount{photos, paths}); err == nil {
			s.bus.Publish(TopicOrganize, raw)
		}
	}
	return
}

// Start back-fills the timeline from the current log, then tails the live log + periodically
// organizes, until ctx is cancelled. Publishes location changes to the bus.
func (s *Service) Start(ctx context.Context) error {
	s.ctx = ctx
	if s.bus != nil { // cache OBS/Twitch live state → gates cam-path auto-restore
		unsub := s.subscribeLiveState()
		defer unsub()
	}
	// Back-fill the timeline from the current session's log so photos taken before launch map.
	if latest := vrclog.LatestLog(vrclog.DefaultLogDir()); latest != "" {
		for _, loc := range vrclog.ScanFile(latest) {
			s.tl.Record(loc)
		}
	}
	if cur, ok := s.tl.Current(); ok {
		s.onLocation(cur)
	}

	tailer := vrclog.NewTailer("", func(loc vrcloc.Location) {
		s.tl.Record(loc)
		s.onLocation(loc)
	})

	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	organize := time.NewTicker(30 * time.Second)
	defer organize.Stop()

	s.OrganizeNow() // catch up on launch
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
			tailer.Poll()
		case <-organize.C:
			s.OrganizeNow()
		}
	}
}

// onLocation logs + publishes a location change (deduped by instance id).
func (s *Service) onLocation(loc vrcloc.Location) {
	if loc.InstanceID == s.lastInstance {
		return
	}
	s.lastInstance = loc.InstanceID
	if s.log != nil {
		kind := "world"
		if loc.IsGroup() {
			kind = "group"
		}
		s.log.Info(logTag, "now in "+loc.WorldName, map[string]any{"world": loc.WorldName, "instance": loc.InstanceID, "kind": kind})
	}
	if s.bus != nil {
		if raw, err := json.Marshal(loc); err == nil {
			s.bus.Publish(TopicLocation, raw)
		}
	}
	s.maybeAutoRestore(loc)
}

// maybeAutoRestore reloads this world's backed-up camera path a few seconds after joining - but only
// while a set is live (OBS streaming/recording or Twitch live) and a backup exists. Crash-recovery
// for DJ sets. Debounced by onLocation's per-instance dedup; re-validates world + live state at fire
// time (in case the world/set changed during the delay).
func (s *Service) maybeAutoRestore(loc vrcloc.Location) {
	f := s.cfg()
	if !f.AutoRestoreCamPaths || loc.WorldID == "" || !s.isLive() {
		return
	}
	entry, ok := vrccampaths.LatestBackup(s.CamPathBackupDir(), loc.WorldID)
	if !ok {
		return
	}
	inst := loc.InstanceID
	time.AfterFunc(restoreDelay, func() {
		if s.ctx != nil && s.ctx.Err() != nil {
			return // service stopped
		}
		if cur, ok := s.tl.Current(); !ok || cur.InstanceID != inst || !s.isLive() {
			return // left the world or the set ended
		}
		if err := s.loadCamPath(entry.File, false); err != nil { // false: don't re-backup a restore
			s.logMsg("campath", fmt.Sprintf("auto-restore failed: %v", err))
		} else {
			s.logMsg("campath", "auto-restored path for "+loc.WorldName)
		}
	})
}

// isLive reports whether any monitored output is live (OBS streaming/recording or Twitch live).
func (s *Service) isLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, on := range s.obsStream {
		if on {
			return true
		}
	}
	for _, on := range s.obsRec {
		if on {
			return true
		}
	}
	return s.twitchLive
}

// subscribeLiveState caches OBS streaming/recording + Twitch live from the bus. Returns an unsub.
func (s *Service) subscribeLiveState() func() {
	u1 := s.bus.Subscribe(obscontrol.TopicStatus, func(e eventbus.Event) {
		var st obscontrol.Status
		if json.Unmarshal(e.Data, &st) != nil {
			return
		}
		s.mu.Lock()
		s.obsStream[st.ID] = st.Streaming
		s.obsRec[st.ID] = st.Recording
		s.mu.Unlock()
	})
	u2 := s.bus.Subscribe(twitch.TopicViewers, func(e eventbus.Event) {
		var vi twitch.ViewerInfo
		if json.Unmarshal(e.Data, &vi) != nil {
			return
		}
		s.mu.Lock()
		s.twitchLive = vi.Live
		s.mu.Unlock()
	})
	return func() { u1(); u2() }
}

// logMsg logs one sub-tagged message (nil-safe when no log bus is wired).
func (s *Service) logMsg(sub, msg string) {
	if fn := s.logf(sub); fn != nil {
		fn(msg)
	}
}

func (s *Service) logf(sub string) func(string) {
	if s.log == nil {
		return nil
	}
	return func(msg string) { debuglog.Go(s.log, logTag, func() { s.log.Info(logTag, sub+": "+msg, nil) }) }
}
