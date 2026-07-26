// Package app is the lifecycle orchestrator and the one daemon. It wires config + the
// feature-module manager + the worker-subprocess supervisor + UI, handles the browser
// deeplink auth callback (single-instance forwarded), and graceful shutdown. Only enabled
// features start; heavy jobs run as worker subprocesses. Two modes: windowed (tray +
// window) and --service (headless).
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/appgroups"
	"rave.page/mate/internal/assetsync"
	"rave.page/mate/internal/audiorec"
	"rave.page/mate/internal/authz"
	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/bridge"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/crewlink"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/discovery"
	"rave.page/mate/internal/dmx"
	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/filexfer"
	"rave.page/mate/internal/giokit"
	"rave.page/mate/internal/gistseq"
	ghlink "rave.page/mate/internal/github"
	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/gpuwatch"
	"rave.page/mate/internal/guardian"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/icecast"
	"rave.page/mate/internal/identity"
	"rave.page/mate/internal/idmark"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediapipe"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/mediasync"
	"rave.page/mate/internal/mfenc"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/midiemit"
	"rave.page/mate/internal/mocap"
	"rave.page/mate/internal/module"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/netstats"
	"rave.page/mate/internal/obscontrol"
	"rave.page/mate/internal/overlayart"
	"rave.page/mate/internal/overlayobs"
	"rave.page/mate/internal/overlayserver"
	"rave.page/mate/internal/peerbridge"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/peers"
	"rave.page/mate/internal/perfmon"
	"rave.page/mate/internal/playsync"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/rtspserve"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/session/aggregator"
	"rave.page/mate/internal/session/sinks/filesink"
	"rave.page/mate/internal/session/sinks/pngsink"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/session/sources/midifbsrc"
	"rave.page/mate/internal/session/sources/nmlsrc"
	"rave.page/mate/internal/session/sources/nowplayingsrc"
	"rave.page/mate/internal/session/sources/prodjlinksrc"
	"rave.page/mate/internal/session/sources/qmlsrc"
	"rave.page/mate/internal/session/sources/rekordboxsrc"
	"rave.page/mate/internal/session/sources/seratolivesrc"
	"rave.page/mate/internal/session/sources/seratoremotesrc"
	"rave.page/mate/internal/session/sources/seratosrc"
	"rave.page/mate/internal/session/sources/virtualdjsrc"
	"rave.page/mate/internal/setfp"
	"rave.page/mate/internal/shared/auth"
	"rave.page/mate/internal/shared/selfupdate"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/stt"
	"rave.page/mate/internal/studio"
	"rave.page/mate/internal/sysactivity"
	"rave.page/mate/internal/timecode"
	"rave.page/mate/internal/traktormap"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/ui"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/videoshare"
	"rave.page/mate/internal/vrbind"
	"rave.page/mate/internal/vrccampaths"
	"rave.page/mate/internal/vrchat"
	"rave.page/mate/internal/vrcmidi"
	"rave.page/mate/internal/vrcperm"
	"rave.page/mate/internal/vrctools"
	"rave.page/mate/internal/vroverlay"
	"rave.page/mate/internal/vrslstream"
	"rave.page/mate/internal/vrstats"
	"rave.page/mate/internal/waveform"
	"rave.page/mate/internal/webcam"
	"rave.page/mate/internal/webui"
	"rave.page/mate/internal/winshot"
	"rave.page/mate/internal/worker"
)

// Run boots the app from signals only. serviceMode runs headless (no tray/window).
func Run(serviceMode bool) error { return run(context.Background(), serviceMode) }

// RunCtx boots the app bound to an external context (cancel to stop) in addition to OS
// signals - used by the Windows Service handler, which stops via the SCM, not SIGTERM.
func RunCtx(ctx context.Context, serviceMode bool) error { return run(ctx, serviceMode) }

// setMemoryLimitGuard sets a soft Go memory limit (GOMEMLIMIT) so the runtime collects aggressively as
// the heap grows instead of ballooning - a raw-video frame runaway in the in-process media plane once
// OOM'd a machine (killed Parsec). Heavy one-shot work (ffmpeg/import) runs in worker SUBPROCESSES with
// their own memory, so this only bounds the resident daemon. Honours an explicit GOMEMLIMIT env override.
func setMemoryLimitGuard() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return // operator set it - respect that
	}
	const softLimit = 3 << 30 // 3 GiB: generous for normal use, hard backstop against a runaway heap
	debug.SetMemoryLimit(softLimit)
}

func run(parent context.Context, serviceMode bool) error {
	setMemoryLimitGuard() // cap Go heap so a media/frame runaway can't balloon RAM + starve the host
	deepLink := extractDeepLink(os.Args[1:])

	inst, primary := acquire()
	if !primary && os.Getenv(selfupdate.AwaitRestartEnv) == "1" {
		// Relaunched by the self-updater: the prior instance is still exiting. Wait for it to
		// release the lock instead of deferring to it.
		inst, primary = acquireWithRetry(15 * time.Second)
	}
	os.Unsetenv(selfupdate.AwaitRestartEnv) // don't leak the one-shot signal to feature subprocesses
	if !primary {
		if deepLink != "" {
			if err := forward(deepLink); err != nil {
				return fmt.Errorf("forward deeplink to running instance: %w", err)
			}
			return nil
		}
		// Headless SERVICE holds the slot: it has no tray icon and Show is a no-op, so a
		// desktop launch looked dead (users Task-Manager-killed it). Take over instead:
		// ask the service instance to quit, wait out the lock, run windowed. A WINDOWED
		// holder is never stolen from - it shows itself below.
		if !serviceMode {
			if st, err := Send("STATUS"); err == nil && strings.HasPrefix(st, "rave-mate service") {
				_, _ = Send("QUIT")
				inst, primary = acquireWithRetry(20 * time.Second)
			}
		}
		if !primary {
			_ = forward(showMsg)
			return fmt.Errorf("another rave-mate instance is already running")
		}
	}
	defer inst.close()

	cfg, cfgErr := config.Load()
	i18n.SetLocale(cfg.Features.UI.Language) // webui i18n: persisted pref → OS locale → en
	log := logbus.New(5000)
	debuglog.Init(log) // persistent debug log + panic capture in the cwd (GUI build has no console)
	if cfgErr != nil { // corrupt/unreadable config - recovered from .bak or reset; never silent
		log.Error("config", "load", map[string]any{"error": cfgErr.Error()})
	}
	defer debuglog.Recover(log, "main", true)
	if serviceMode {
		debuglog.Go(log, "app", func() { stderrSink(log) })
	}
	log.Info("app", "starting", map[string]any{"mode": modeLabel(serviceMode), "api": cfg.APIBaseURL, "version": version.String()})

	// Crash guardian: detached child that relaunches us if the process dies WITHOUT a clean
	// shutdown (cgo/OpenVR hard fault - the in-process watchdog can't survive those). Disarmed
	// on every clean exit path via defer. Service mode relies on SCM recovery instead; opt out
	// via Settings (cfg.DisableCrashGuardian, next launch) or RAVE_MATE_NO_GUARDIAN=1.
	// Crash-loop brake lives in the guardian (≤4 per 10 min).
	guardDisarm := func() {} // also called by appControl.Quit's hard-exit backstop (defers skip on os.Exit)
	if !serviceMode && !cfg.DisableCrashGuardian && os.Getenv("RAVE_MATE_NO_GUARDIAN") == "" {
		if gdir, gerr := config.Dir(); gerr == nil {
			if disarm, gerr2 := guardian.Spawn(gdir); gerr2 == nil {
				guardDisarm = disarm
				defer disarm()
				log.Info("app", "crash guardian armed", nil)
			} else {
				log.Warn("app", "crash guardian unavailable", map[string]any{"error": gerr2.Error()})
			}
		}
	}

	// Install/location marker: write our exe path so a co-located rave-app can detect "installed
	// but not running" and launch us for the Local Studio link. Owner-only; best-effort.
	if exe, eerr := os.Executable(); eerr == nil {
		if p, perr := config.DataPath("exe.path"); perr == nil {
			_ = os.WriteFile(p, []byte(exe), 0o600)
		}
	}
	// Handoff secret: authenticates the co-located token handoff (rave-app encrypts the token
	// bundle under this before pushing it, so a cross-user port-squatter can't harvest it).
	// Generate once; owner-only at rest. Best-effort - handoff just degrades to refused.
	ensureHandoffSecret(log)

	// Local persistence (analysis cache + automations/jobs/runs). Non-fatal if it fails.
	var st *store.Store
	if dbPath, derr := config.DataPath("rave-mate.db"); derr == nil {
		if st, derr = store.Open(dbPath); derr != nil {
			log.Warn("app", "persistence unavailable", map[string]any{"error": derr.Error()})
			st = nil
		}
	}

	// Relational DJ-library store - persists the imported collection + history across launches
	// so the UI loads instantly and "Refresh" only syncs the delta. Non-fatal if it fails.
	var lib *libdb.DB
	if dbPath, derr := config.DataPath("rave-mate-library.db"); derr == nil {
		if lib, derr = libdb.Open(dbPath); derr != nil {
			log.Warn("app", "library db unavailable", map[string]any{"error": derr.Error()})
			lib = nil
		}
	}

	// Stable long-term node identity (Ed25519) for the LAN peer link - persisted in the
	// store, sealed where the OS has a secret API. Ephemeral (non-persisted) if no store.
	ident, ierr := identity.LoadOrCreate(st)
	if ierr != nil {
		log.Warn("app", "node identity unavailable", map[string]any{"error": ierr.Error()})
		ident, _ = identity.LoadOrCreate(nil) // ephemeral fallback so the app still runs
	}
	if st == nil {
		log.Warn("app", "node identity is ephemeral (no store) - remembered peers won't persist", nil)
	}
	lib.SetNodeID(ident.NodeID) // stamp change_log rows with this node (nil-safe)

	apiC := api.New(cfg.APIBaseURL, log)
	authMgr := auth.NewManager(apiC, log, cfg.APIBaseURL, config.DataPath)
	if err := authMgr.RegisterScheme(); err != nil {
		log.Warn("app", "scheme registration failed", map[string]any{"error": err.Error()})
	}
	authMgr.Restore()

	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Worker subprocess supervisor (lazy - spawns nothing until a job runs).
	workers, werr := worker.New(log)
	if werr != nil {
		log.Warn("app", "worker supervisor unavailable", map[string]any{"error": werr.Error()})
	} else {
		if cfg.Features.Transcode.Enabled {
			workers.Configure("transcode", cfg.Features.Transcode.MaxConcurrent)
		}
		// Opt-in external probe worker (Zig rave-probe, ZIG_MIGRATION P4). Missing exe →
		// keep the builtin so a stale config can't break analysis.
		if exe := cfg.Features.Workers.ProbeExe; exe != "" {
			if _, serr := os.Stat(exe); serr != nil {
				log.Warn("app", "probe worker exe missing - using builtin", map[string]any{"exe": exe, "error": serr.Error()})
			} else {
				workers.SetExternal("probe", exe)
				log.Info("app", "probe worker: external exe", map[string]any{"exe": exe})
			}
		}
	}

	// Per-interface monitor buses: live ring buffers the Logs-area monitor tabs subscribe to.
	// Separate from the app log so a raw MIDI/HTTP firehose doesn't drown the structured log.
	midiMon := logbus.New(2000)
	traktorMon := logbus.New(2000)
	sessionMon := logbus.New(2000)

	// Feature subsystems (built unconditionally; the manager starts only the enabled ones).
	// Traktor HTTP ingest runs in a supervised child process (featurehost) - a fault in the
	// listener/parser kills only the child; the host logs + restarts it.
	trk, terr := featurehost.NewTraktorProxy(log, traktorMon, func() featurehost.TraktorConfig {
		return featurehost.TraktorConfig{
			Addr:        fmt.Sprintf("127.0.0.1:%d", cfg.Features.Traktor.ResolvedPort()),
			LogPath:     traktorLogPath(),
			LogPayloads: cfg.Features.Traktor.LogPayloads,
		}
	})
	if terr != nil {
		return terr
	}

	// DJ-data aggregation hub: sources feed the merger, the stream publisher + sinks read it.
	merger := session.NewMerger()
	// ID-mark redaction (unreleased-track leak prevention): applied at the merger's output
	// boundaries so every consumer - overlays, stream publisher, now-playing file, recorder
	// tracklist, Publish UI, VR overlays - inherits it. Raw data never leaves the merger.
	idmarksPath, _ := config.DataPath("idmarks.json")
	idMarks := idmark.Load(idmarksPath)
	merger.SetRedactor(func(p string) (session.Mark, bool) {
		m, ok := idMarks.Match(p)
		return session.Mark{ShowArtist: m.ShowArtist, ShowLabel: m.ShowLabel}, ok
	})
	agg := aggregator.New(log, merger)
	agg.SetMonitor(sessionMon)
	agg.AddSource(trk, func() bool { return cfg.Features.Traktor.Enabled })
	// Sources built via AddSourceFn are reconstructed from live config on every (re)start,
	// so settings edits apply through agg.RestartSource (webui settings_apply.go) - no app
	// restart, no manual feature off/on.
	agg.AddSourceFn(func() session.Source {
		return nmlsrc.New(log, merger, cfg.Features.NML.CollectionPath, cfg.Features.NML.HistoryDir)
	}, func() bool { return cfg.Features.NML.Enabled })
	// MIDI driver runs in a child process (winmm callback faults can't touch the daemon);
	// its host lifecycle rides the source slot, so the enable-gate + Reconcile drive it.
	midiSrc, merr := featurehost.NewMidiProxy(log, midiMon, func() featurehost.MidiConfig {
		// driver sync BEFORE the child (re)opens ports: covers boot + driver-update
		// reboots, where no config edit would otherwise trigger the webui sync
		syncMIDIDriver(cfg.Features.MIDI.Controllers, log)
		return featurehost.MidiConfig{
			DenonPort:   cfg.Features.MIDI.DenonPort,
			CustomPort:  cfg.Features.MIDI.CustomPort,
			Controllers: midiControllerInits(cfg.Features.MIDI.Controllers),
			Bridge: featurehost.MidiBridgeInit{
				Enabled:    cfg.Features.MIDI.Bridge.Enabled,
				ToDJPort:   cfg.Features.MIDI.Bridge.ToDJPort,
				FromDJPort: cfg.Features.MIDI.Bridge.FromDJPort,
			},
		}
	})
	if merr != nil {
		return merr
	}
	agg.AddSource(midiSrc, func() bool { return cfg.Features.MIDI.Enabled })
	// Pro DJ Link: passive UDP listener for Pioneer CDJ/XDJ status broadcasts (live BPM/play
	// state per deck). In-proc (pure stdlib net); the enable-gate drives its lifecycle. CDJ
	// status packets carry only a rekordbox track id - best-effort resolve it to title/artist/key
	// from the local master.db so networked players show track text (BPM/play-state work regardless).
	pdl := prodjlinksrc.New(log)
	if cfg.Features.ProDJLink.Enabled || cfg.Features.Rekordbox.Enabled {
		if resolve, rerr := rekordboxsrc.NewResolver(cfg.Features.Rekordbox.DBPath, cfg.Features.Rekordbox.DBKey); rerr != nil {
			log.Info("prodjlink", "rekordbox metadata resolver unavailable", map[string]any{"err": rerr.Error()})
		} else {
			pdl.SetResolver(resolve)
		}
	}
	agg.AddSource(pdl, func() bool { return cfg.Features.ProDJLink.Enabled })
	// Serato: collection + live now-playing from the active History session file (local files,
	// no account/internet). In-proc (stdlib file watch); the enable-gate drives its lifecycle.
	agg.AddSourceFn(func() session.Source {
		return seratosrc.New(log, cfg.Features.Serato.SeratoDir, cfg.Features.Serato.NowPlaying)
	}, func() bool { return cfg.Features.Serato.Enabled })
	// Serato Remote: real-time OSC-over-TCP - we advertise _SeratoIOSRemote._tcp and Serato DJ
	// Pro connects in and streams per-deck state. In-proc + bounded (tiny control messages, no
	// media frames); enable-gate drives lifecycle. Debug flag logs every frame for the handshake RE.
	agg.AddSourceFn(func() session.Source {
		return seratoremotesrc.New(log, seratoremotesrc.Config{Debug: cfg.Features.Serato.RemoteDebug})
	}, func() bool { return cfg.Features.Serato.Remote })
	// Serato Live Playlist: remote scrape of serato.com/playlists/<user>/live (master now-playing).
	// Independent opt-in (works with no local Serato install - controllers/all decks). In-proc +
	// bounded (a ~10s HTTP poll, capped body, no media); enable-gate drives lifecycle. Emits only
	// on track CHANGE so a stale past-session page isn't re-asserted as fresh now-playing.
	agg.AddSourceFn(func() session.Source {
		return seratolivesrc.New(log, seratolivesrc.Config{
			URL:      cfg.Features.Serato.LivePlaylistURL,
			Interval: time.Duration(cfg.Features.Serato.LivePlaylistInterval) * time.Second,
		})
	}, func() bool { return cfg.Features.Serato.LivePlaylist })
	// MIDI LED-feedback play/pause: the ravemidi driver captures DJ-software LED writes; a
	// paused deck flashes its play LED, a playing one holds it solid. Real-time per-deck
	// play-state for a MIDI-only Serato rig (History lags + never records pause, Serato Remote
	// is discontinued). In-proc + bounded (a ~1s read of the driver trace ring, no media);
	// gated on the MIDI feature being ON *and* the kernel driver installed - driver presence
	// alone must not spin a permanent ioctl poll when the user never enabled MIDI.
	agg.AddSourceFn(func() session.Source {
		return midifbsrc.New(log)
	}, func() bool { return cfg.Features.MIDI.Enabled && midi.DriverInstalled() })
	// VirtualDJ: collection (database.xml) + live now-playing via Network Control (full metadata),
	// our OS2L server (BPM/beat), and/or the tracklist file. In-proc; enable-gate drives lifecycle.
	agg.AddSourceFn(func() session.Source {
		vdj := cfg.Features.VirtualDJ
		return virtualdjsrc.New(log, virtualdjsrc.Config{
			NetCtl: vdj.NetCtl, NetCtlURL: vdj.ResolvedNetCtlURL(), NetCtlAuth: vdj.NetCtlAuth,
			OS2L: vdj.OS2L, OS2LPort: vdj.ResolvedOS2LPort(),
			Tracklist: vdj.Tracklist, DatabaseDir: vdj.DatabaseDir,
		})
	}, func() bool { return cfg.Features.VirtualDJ.Enabled })
	// Rekordbox live now-playing: master.db history poll (recently-played, ~60s lag) + process
	// memory read (real-time, Windows-only, offset-seeded). In-proc; enable-gate drives lifecycle.
	agg.AddSourceFn(func() session.Source {
		rb := cfg.Features.Rekordbox
		return rekordboxsrc.New(log, rekordboxsrc.Config{
			DBPath: rb.DBPath, DBKey: rb.DBKey, DBPoll: rb.DBPoll, MemoryRead: rb.MemoryRead,
		})
	}, func() bool { return cfg.Features.Rekordbox.Enabled })
	// Icecast set-capture receiver: Traktor broadcasts a live set to this local Icecast
	// source endpoint. Runs in a child process - the TCP listener, source-protocol parsing,
	// and capture-file writing are isolated; the daemon gets metadata observations, capture
	// events (for libdb linking), and a status mirror.
	icecastRcv, ierr2 := featurehost.NewIcecastProxy(log, func() icecast.Config { return setCaptureConfig(cfg) })
	if ierr2 != nil {
		return ierr2
	}
	agg.AddSource(icecastRcv, func() bool { return cfg.Features.SetCapture.Enabled })
	// OBS bridge: watches obs-websocket for finished recordings (video) so they land in the
	// library linked to the tracklist, like Icecast audio captures. Child process - ws
	// parsing faults can't touch the daemon.
	obsW, oerr := featurehost.NewObsProxy(log, func() featurehost.ObsConfig {
		o := cfg.Features.OBS
		ow := cfg.Features.OverlayWeb
		src := ow.OBSSource
		manage := ow.Enabled && src.Enabled
		overlayURL := ""
		if manage {
			overlayURL = fmt.Sprintf("http://127.0.0.1:%d/", ow.ResolvedPort())
		}
		return featurehost.ObsConfig{Host: o.ResolvedHost(), Port: o.ResolvedPort(), Password: o.Password,
			Overlay: featurehost.ObsOverlayCfg{Enabled: manage, URL: overlayURL,
				Scene: src.ResolvedScene(), Source: src.ResolvedSourceName(),
				Width: src.Width, Height: src.Height, NestInProgram: src.NestInProgram}}
	})
	if oerr != nil {
		return oerr
	}
	// Ableton Link bridge: publishes the fused DJ master tempo/phrase onto a Link session so
	// Link-aware visuals (Resolume/Live) follow the DJ. Child process - the real Link backend is
	// cgo (abl_link) and only present in `-tags abletonlink` builds; otherwise the child reports
	// unavailable and this stays inert. Init closure re-reads config per (re)spawn.
	linkW, lerr := featurehost.NewAbletonLinkProxy(log, func() featurehost.AbletonLinkConfig {
		a := cfg.Features.AbletonLink
		return featurehost.AbletonLinkConfig{
			Enabled: a.Enabled, Quantum: a.ResolvedQuantum(), StartStopSync: a.StartStopSync,
		}
	})
	if lerr != nil {
		return lerr
	}
	// VRChat: account manager stays in-proc (sealed session, DPAPI); the pipeline WS runs
	// in a child process. Login/logout pushes the session token to the running child;
	// a (re)spawn re-reads it from the manager.
	vrcMgr := vrchat.NewManager(log, func() bool { return cfg.Features.VRChat.RememberSession })
	vrcW, vcerr := featurehost.NewVrchatProxy(log, func() string {
		a, _ := vrcMgr.Client().Cookies()
		return a
	})
	if vcerr != nil {
		return vcerr
	}
	vrcMgr.OnChange(func(s vrchat.State) {
		tok := ""
		if s.LoggedIn && s.Via == "" { // pipeline runs ONLY on the instance holding the session
			tok, _ = vrcMgr.LocalClient().Cookies()
		}
		vrcW.SetAuth(tok)
	})
	// Opt-in uplink: vault the VRChat session on rave.page (server-side group/event
	// features). Store on login while Uplink is on; delete on explicit unlink or
	// toggle-off. Strictly gated - nothing is pushed unless the user opted in.
	vrcUplinkStore := func() {
		s := vrcMgr.State()
		rtok := authMgr.Token()
		// s.Via != "" = federated read-through session: NEVER vault (this instance
		// has no cookies; pushing would overwrite the real vaulted session with "").
		if !cfg.Features.VRChat.Uplink || !s.LoggedIn || s.Via != "" || rtok == "" {
			return
		}
		a, t := vrcMgr.LocalClient().Cookies()
		uctx, ucancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ucancel()
		if err := apiC.StoreVrchatToken(uctx, rtok, api.VrchatToken{
			AuthToken: a, TwoFactor: t, UserID: s.UserID, DisplayName: s.DisplayName,
		}); err != nil {
			log.Warn("vrchat", "uplink store failed", map[string]any{"error": err.Error()})
		} else {
			log.Info("vrchat", "session vaulted on rave.page", nil)
		}
	}
	vrcUplinkDelete := func() {
		rtok := authMgr.Token()
		if rtok == "" {
			return
		}
		uctx, ucancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ucancel()
		if err := apiC.DeleteVrchatCredentials(uctx, rtok); err != nil {
			log.Warn("vrchat", "uplink delete failed", map[string]any{"error": err.Error()})
		} else {
			log.Info("vrchat", "vaulted session removed from rave.page", nil)
		}
	}
	vrcMgr.OnChange(func(s vrchat.State) {
		if s.LoggedIn && s.Via == "" {
			debuglog.Go(log, "vrchat-uplink", vrcUplinkStore)
		}
	})
	vrcMgr.OnUnlink(func() {
		if cfg.Features.VRChat.Uplink {
			debuglog.Go(log, "vrchat-uplink", vrcUplinkDelete)
		}
	})
	// Planned sources: registered (always-disabled) so the Session tab advertises the full
	// connection roadmap + what each would provide. Implementing one = flip its gate.
	disabled := func() bool { return false }
	agg.AddSource(nowplayingsrc.New(log), disabled)
	agg.AddSource(qmlsrc.New(log), disabled)
	agg.AddSink(filesink.New(log, func() string { return fileSinkDir(cfg) }), func() bool { return cfg.Features.NowPlayingFile.Enabled })
	// Live browser overlay (OBS Browser source): every loaded deck + faders/EQ + cover art,
	// served over loopback with SSE so faders animate. Shared cover-art resolver (local
	// embedded art → cache) also feeds the PNG + obs-websocket renderers.
	overlayArt := overlayart.New(overlayCacheDir(), log)
	if lib != nil {
		overlayArt.SetStore(libArtStore{db: lib}) // persist extracted covers in the library DB
	}
	// Per-track waveform peak overviews for the overlay panels (generated on first play, cached).
	overlayWave := waveform.New(overlayWaveCacheDir(), log)
	if lib != nil {
		// Resolve the file path by artist+title before Traktor reports it (~90s), so peaks appear
		// promptly - mirrors the cover-art name-based resolution.
		overlayWave.SetPathResolver(lib.TrackPathByMeta)
	}
	waveFn := func() config.OverlayWaveformFeature { return cfg.Features.OverlayWaveform }
	overlayWebSink := overlayserver.New(log, func() int { return cfg.Features.OverlayWeb.ResolvedPort() }, overlayArt, overlayWave, waveFn, overlayLayoutPath())
	agg.AddSink(overlayWebSink, func() bool { return cfg.Features.OverlayWeb.Enabled })
	// Native per-deck PNG cards (OBS Image source per deck) - same data + cued-not-played gate
	// + shared cover-art resolver, but flat files instead of a live page (no browser, no cgo).
	agg.AddSink(
		pngsink.New(log, func() string { return overlayPngDir(cfg) }, overlayArt, overlayWave, waveFn, overlayStylePath()),
		func() bool { return cfg.Features.OverlayPNG.Enabled },
	)
	// obs-websocket renderer: drives OBS inputs directly via the subprocess OBS bridge (obsW
	// satisfies overlayobs.OBSClient, forwarding requests to the live client in the obs child).
	// No-op while OBS is down; needs the OBS connection feature enabled too.
	agg.AddSink(
		overlayobs.New(log, obsW, overlayArt),
		func() bool { return cfg.Features.OverlayOBS.Enabled },
	)
	// GPU/IPC video share: publishes each deck's card over the OS-native sharing API (Spout/
	// Syphon/PipeWire) for any compatible receiver. Transport chosen at build time; no-op in the
	// default build (see internal/videoshare).
	agg.AddSink(
		videoshare.New(log, overlayArt, overlayWave, waveFn, func() int { return cfg.Features.VideoShare.ResolvedRenderScale() }, overlayStylePath()),
		func() bool { return cfg.Features.VideoShare.Enabled },
	)
	rec := recorder.New(log, st, lib, cfg.Features.Recorder.ResolvedConfirmSeconds())
	agg.AddSink(rec, func() bool { return cfg.Features.Recorder.Enabled })
	// Native audio recorder: captures a chosen audio device to FLAC (default), following OBS
	// record start/stop + manual. Finalized files register as set recordings linked to the
	// tracklist. OBS state comes from the subprocess bridge mirror.
	audioRec := audiorec.New(log, func() config.AudioRecordFeature { return cfg.Features.AudioRecord },
		rec, lib, func() bool { return obsW.Status().Recording })
	// Play-layer backend sync (PLAY_LAYER_INTEGRATION_BRIEF): identifies captured played
	// tracks against the canonical corpus + publishes recorded sets. Owner-scoped - uses the
	// current access token (rave-mate's own session or one adopted from a co-located rave-app).
	syncer := playsync.New(apiC, lib, log, authMgr.Token)
	if workers != nil {
		syncer.SetProbe(workers) // media sync (waveform peaks + artwork) probes via the worker pool
	}
	// Per-track fingerprinter for captured set audio (needs the worker pool for fpcalc).
	var setFp *setfp.Fingerprinter
	if workers != nil {
		setFp = setfp.New(workers, lib)
	}
	fpEnabled := func() bool { return cfg.Features.Fingerprint.Enabled }
	// Persist + time-link captured set files (Icecast audio + finished OBS recordings) to
	// the tracklist recorded over the same span; re-links orphans when recordings finalize.
	// On set finish: fingerprint linked audio + sync the set-log to the play layer.
	debuglog.Go(log, "setcapture-link", func() {
		linkCaptures(ctx, icecastRcv, obsW, rec, lib, setFp, syncer, fpEnabled, log)
	})
	// Activity governor: makes rave-mate a good neighbour by default. Detect a live OBS stream on
	// this machine and pause non-essential heavy work (fingerprinting/indexing) while it runs -
	// stream-critical paths (Spout, peerlink media, MIDI/now-playing, overlays) keep feeding it.
	governor.SetLog(log)
	// Live-stream publisher runs in a child process (spawned lazily on go-live); merged
	// updates stream over IPC, the publish token never enters the daemon.
	pub, perr := featurehost.NewStreamProxy(log, merger, func() featurehost.StreamConfig {
		return featurehost.StreamConfig{APIBaseURL: cfg.APIBaseURL}
	})
	if perr != nil {
		return perr
	}
	pub.Bind(ctx)
	// Auto-live: the OBS stream signal (streamLive) drives the now-playing broadcast - publish while a
	// stream is live here (signed in + not paused), end when it stops. No manual go-live. The same 3s
	// sampler also feeds the activity governor (good-neighbour: pause heavy work while streaming).
	autoLive := &autoLiveDriver{pub: pub, log: log}
	autoLiveIn := autoLiveInputs{
		signedIn: authMgr.SignedIn,
		paused:   func() bool { return cfg.Features.StreamBridge.PauseLiveSignal },
		token:    authMgr.Token,
		title:    func() string { return nowPlayingTitle(merger) },
	}
	debuglog.Go(log, "governor-stream", func() { watchStreaming(ctx, obsW, log, autoLive, autoLiveIn) })
	// Subprocessed audio player: the native internal/audio engine runs in the `player` child (a
	// decode/codec/oto fault kills only it), ffmpeg fallback for AAC/M4A. Shared by every player
	// panel; pre-warmed by Bind for instant play.
	player, plerr := featurehost.NewPlayerProxy(log)
	if plerr != nil {
		return plerr
	}
	player.SetVolume(cfg.Features.Player.VolumeOr()) // persisted global gain, re-pushed per spawn
	player.Bind(ctx)
	// Auto-record across a live stream's span (start with go-live, finalize on end) so the
	// tracklist + precise recording window are captured without a manual step.
	debuglog.Go(log, "auto-record", func() { autoRecord(ctx, pub, rec, func() bool { return cfg.Features.Recorder.Enabled }) })
	// One transcode hub shared by the desktop UI and the studio WS channel, so a job
	// started on either is attachable from the other.
	var studioRunner jobs.Runner
	var transcodeHub *jobs.Hub
	if workers != nil {
		studioRunner = workers
		transcodeHub = jobs.New(workers)
	}

	// LAN peer link: discovery + secure paired connections to other rave-mate instances.
	peerStore := peers.New(st)
	peerNick := cfg.Features.Peers.Nickname
	if peerNick == "" {
		peerNick = defaultNodeNickname()
	}
	peerMgr := peerlink.New(ident, peerStore, log)
	disc := discovery.New(discovery.Service{
		NodeID: ident.NodeID, Name: peerNick, ProtoVersion: 1, IdentityFP: ident.NodeID,
	}, log)
	// Bridges live DJ data (now-playing, MIDI, remote control) across the peerlink data
	// channel by tapping the same session merger the stream publisher + file sink use.
	peerBridge := peerbridge.New(log, peerMgr)
	// Persisted mesh mode: mirror local MIDI to every connected peer (UI toggle live-applies).
	peerBridge.SetMIDIMesh(cfg.Features.MIDI.MeshForward)
	// MIDI bridge: tap local MIDI out to the link (gated by the control context), and apply
	// inbound peer MIDI to the local decoders so a linked controller drives this session. The
	// forwarder also fans MIDI to the keybind dispatcher (set once the managers below exist).
	var dispatchMIDIBind func(port string, m midi.Message)
	midiSrc.SetForwarder(func(port string, m midi.Message) {
		peerBridge.ForwardMIDI(m.Status, m.Data1, m.Data2)
		if dispatchMIDIBind != nil {
			dispatchMIDIBind(port, m)
		}
	})
	peerBridge.SetMIDISink(func(_ string, payload []byte) {
		var mm peerbridge.MIDIMsg
		if json.Unmarshal(payload, &mm) == nil {
			midiSrc.Inject(midi.Message{Status: mm.S, Data1: mm.D1, Data2: mm.D2})
		}
	})

	// Cross-instance event bus (twitch/vr/obs.mic + capability routing) over the peerlink ChanBus.
	// Local-only until peers connect; modules publish/subscribe by topic, consuming a capability
	// owned by another instance transparently.
	bus := eventbus.New(log, ident.NodeID)
	bus.SetTransport(
		func(payload []byte) { peerMgr.Broadcast(peerlink.ChanBus, payload) },
		func(nodeID string, payload []byte) { _ = peerMgr.SendTo(nodeID, peerlink.ChanBus, payload) },
	)
	peerBridge.SetBusSink(bus.Inbound)
	peerMgr.AddListener(nil, bus.Advertise) // re-advertise capabilities on peer connect/disconnect

	// medialink P1: the LAN media plane (audio/video/timecode) negotiates media.advert|offer|answer
	// on the eventbus and dials its own AEAD TCP socket (keyed off peerMgr.MediaSecret). P4 adds
	// the ffmpeg encode/decode children (mediapipe) + the async §3.2 codec probe feeding
	// capability negotiation. See MEDIALINK_DESIGN.md.
	mediaClock := medialink.NewSoftwareClock() // §2.3 tier-2 media clock (disciplined by sync probes)
	encFac, decFac := mediapipe.Factories(log)
	mediaLinkCfg := func() config.MediaLinkFeature { return cfg.Features.MediaLink }
	mediaRouter := medialink.New(medialink.Options{
		Self: ident.NodeID, Bus: mediaBus{bus}, Secrets: peerMgr, Log: log, Clock: mediaClock,
		Encoder: encFac, Decoder: decFac,
		EncodeMaxHeight: cfg.Features.MediaLink.MaxHeight,
		EncodePolicy:    mediaEncodePolicy(mediaLinkCfg),
		EncodeDevice:    mediaEncodeDevice(log, mediaLinkCfg),
	})
	// #44: the media plane (medialink+mediaroute+webcam) runs isolated in a memory-capped featurehost
	// child by DEFAULT (MediaLink.Subprocess tri-state; explicit false = legacy in-proc). TCPlane +
	// mediaClock stay daemon-side; the child mirrors the clock + bridges the negotiation bus.
	// mediaCtl/mediaRoutesCtl/webcamCtl point at the child proxies or the in-proc managers, so the
	// rest of app + UI is agnostic.
	useMediaChild := cfg.Features.MediaLink.MediaSubprocess()
	mediaChildFailed := false // config wanted the isolated child but it wouldn't spawn -> fail CLOSED
	var mediaCapsMu sync.Mutex
	var mediaEnc, mediaDec []string
	var mediaSyncPeer string
	var mediaChild *featurehost.MediaHost
	var mediaCtl medialink.MediaControl = mediaRouter
	var mediaRoutesCtl mediaroute.ReceiveControl
	var webcamCtl webcam.CamControl
	if useMediaChild {
		label, _ := os.Hostname()
		if label == "" {
			label = "rave-mate"
		}
		mh, err := featurehost.NewMediaHost(log, bus, mediaClock, featurehost.MediaHostDeps{
			Self: ident.NodeID, Label: label,
			Cfg: func() (config.MediaLinkFeature, config.WebcamFeature) {
				return cfg.Features.MediaLink, cfg.Features.Webcam
			},
			Secrets:    func() map[string][]byte { return connectedMediaSecrets(peerMgr) },
			Codecs:     func() ([]string, []string) { mediaCapsMu.Lock(); defer mediaCapsMu.Unlock(); return mediaEnc, mediaDec },
			SyncPeer:   func() string { mediaCapsMu.Lock(); defer mediaCapsMu.Unlock(); return mediaSyncPeer },
			SameHost:   func(peer string) bool { return peerIsLocalhost(peerMgr, peer) },
			MemLimitMB: mediaChildMemMB,
		})
		if err != nil {
			// FAIL CLOSED. The memory-capped child is the ONLY sanctioned home for the frame-churning
			// media route/webcam plane (a raw 720p30 in-proc route once ate 75% RAM + killed Parsec).
			// If it won't spawn we DISABLE the plane - never silently run it ungoverned in the daemon.
			log.Error("medialink", "media isolation child failed to spawn - media routes + webcam DISABLED (they will not run in-proc)", map[string]any{"error": err.Error()})
			useMediaChild = false
			mediaChildFailed = true
		} else {
			mediaChild = mh
			mediaCtl = mh.Media()
			mediaRoutesCtl = mh.MediaRoutes()
			webcamCtl = mh.Webcam()
			peerMgr.AddListener(nil, func() { mediaChild.PushSecrets(connectedMediaSecrets(peerMgr)) }) // per-peer media keys on connect/disconnect
		}
	}
	// mediaInProc: run the media route/webcam plane IN this process. True only when isolation was
	// never requested (config default). When the requested child failed to spawn it stays FALSE, so
	// the frame plane is left off (fail-closed) rather than falling back to the ungoverned in-proc path.
	mediaInProc := !useMediaChild && !mediaChildFailed
	peerMgr.AddListener(nil, func() { mediaCtl.Advertise() }) // re-advertise media on peer connect/disconnect
	// §3.2 codec probe (test encodes take seconds - off the startup path). SWOnly keeps only the
	// software tiers advertised (diagnostic: forces tier 4 + the CPU warning on routes we source).
	// encFilter maps a raw probe result to the set we advertise.
	encFilter := func(caps mediapipe.Caps) []string {
		enc := caps.Encoders
		if cfg.Features.MediaLink.SWOnly {
			enc = nil
			for _, e := range caps.Encoders {
				if e == "libx264" || e == "mjpeg" {
					enc = append(enc, e)
				}
			}
		} else if mfenc.Available() && !slices.Contains(enc, medialink.EncoderMFNative) {
			// The native pipe-free engine gets its OWN capability name (never ffmpeg's h264_mf), so
			// the negotiation can preempt the pipe-fed tiers with it and the engine keying in
			// mediapipe.Factories is unambiguous.
			enc = append(enc, medialink.EncoderMFNative)
		}
		return enc
	}
	var mfOnly []string // ffmpeg-less fallback: the native MF engine can still SOURCE h264
	if mfenc.Available() && !cfg.Features.MediaLink.SWOnly {
		mfOnly = []string{medialink.EncoderMFNative}
	}
	// mediaCaps gates the probe on the governor (no encode session stolen mid-stream) and runs the
	// advertised list through the encode-headroom planner on every streaming edge. See mediacaps.go.
	mediaCapsPlan := newMediaCaps(log, func(enc, dec []string) {
		mediaCapsMu.Lock()
		mediaEnc, mediaDec = enc, dec
		mediaCapsMu.Unlock()
		mediaCtl.SetCodecCaps(enc, dec) // in-proc router OR pushed to the media child
	})
	governor.OnChange(func(governor.Signals) { debuglog.Go(log, "mediapipe", mediaCapsPlan.onGovernorChange) })
	debuglog.Go(log, "mediapipe", func() { mediaCapsPlan.probeAndAdvertise(ctx, encFilter, mfOnly) })
	// P4 route glue: Spout-sender sharing + receive routes (Peers tab). Same-PC routes are
	// refused (§3 - never encode locally); detected via the peer's remote address.
	mediaRoutes := mediaroute.New(mediaroute.Options{
		Log: log, Router: mediaRouter,
		Cfg:      func() config.MediaLinkFeature { return cfg.Features.MediaLink },
		SameHost: func(peer string) bool { return peerIsLocalhost(peerMgr, peer) },
	})

	// medialink P3: timecode plane. Elects one TC master across paired instances (media.tc
	// announces); the elected master also becomes the clock-sync discipline peer, so slave TC
	// derives from a media clock slewed into the master's domain. See MEDIALINK_DESIGN.md §4.
	tcPlane := medialink.NewTCPlane(medialink.TCPlaneOptions{
		Self: ident.NodeID, Bus: mediaBus{bus}, Clock: mediaClock, Log: log,
		OnMaster: func(node string) {
			if node == ident.NodeID {
				node = "" // we are authoritative - stop disciplining to anyone
			}
			mediaCapsMu.Lock()
			mediaSyncPeer = node
			mediaCapsMu.Unlock()
			mediaCtl.SetSyncPeer(node) // in-proc router OR pushed to the media child
		},
	})

	// File transfer between paired instances (filexfer): negotiates file.offer|answer on the
	// eventbus, bulk data on its own AEAD TCP listener (47651-47655) keyed off peerMgr.FileSecret.
	// Zero footprint while the feature is off (module below); stalled sends retry on reconnect.
	fileXfer := filexfer.New(filexfer.Options{
		Self: ident.NodeID, Bus: fileBus{bus}, Secrets: peerMgr, Log: log,
		Policy: func() filexfer.Policy {
			f := cfg.Features.FileXfer
			return filexfer.Policy{Enabled: f.Enabled, Dir: f.ResolvedDownloadDir(), AutoAccept: f.AutoAccept()}
		},
	})
	peerMgr.AddListener(nil, fileXfer.PeerStateChanged) // retry stalled sends on peer reconnect

	// 1 Hz network sampler (peer bytes/RTT + API bytes) feeding the dashboard graphs.
	netSampler := netstats.NewSampler(120)
	debuglog.Go(log, "netstats", func() {
		netSampler.Run(ctx, time.Second, func() netstats.Totals {
			t := netstats.Totals{RTT: map[string]netstats.RTTSample{}}
			for _, p := range peerMgr.NetStats() {
				t.PeerIn += p.BytesIn
				t.PeerOut += p.BytesOut
				ms := math.NaN()
				if p.HasRTT {
					ms = p.RTTMs
				}
				label := p.Nickname
				if label == "" {
					if label = p.NodeID; len(label) > 8 {
						label = label[:8]
					}
				}
				t.RTT[p.NodeID] = netstats.RTTSample{Label: label, Ms: ms}
			}
			t.APIIn, t.APIOut = apiC.NetTotals()
			return t
		})
	})

	// Twitch integration (chat/alerts/title-control/moderation) - supervised feature child.
	// The child owns auth (sealed twitch.bin single-writer) + EventSub + polling; the proxy
	// republishes onto the bus, serves peer commands, and appends every bus chat/alert (local
	// AND peer-origin) to the persistent chat log so a set is readable after the fact.
	var twitchLog *twitch.ChatLog
	if dir, derr := config.DataPath("twitch-chat"); derr == nil {
		if cl, cerr := twitch.OpenChatLog(dir, log); cerr == nil {
			twitchLog = cl // never closed: lives for the process; appends are single writes
		} else {
			log.Warn("twitch", "chat log unavailable", map[string]any{"error": cerr.Error()})
		}
	}
	twitchW, twerr := featurehost.NewTwitchProxy(log, bus, twitchLog, func() string { return cfg.Features.Twitch.ClientID })
	if twerr != nil {
		return twerr
	}

	// World Sync: GitHub-gist feeds for VRChat worlds (permission lists + posters/events/
	// now-playing). Publishes only while enabled + GitHub linked; group-role expansion needs
	// the VRChat link. Now-playing consumes the session layer's derived output - MUST switch
	// to the redacted unified surface when the ID-redaction feature lands.
	ghAuth := ghlink.NewAuth(func() string { return cfg.Features.WorldSync.GitHubClientID }, log)
	ghAuth.Load()
	ghGists := ghlink.NewGists(ghAuth)
	// Persisted monotonic per-module gist seq (the world's SEQ-GATE) - shared by the enveloped
	// gist writer AND the editor-bridge preset store so a preset republished to a gist keeps a valid
	// runtime seq. Survives restarts; a lost ledger risks at most a one-time reset (gistseq doc).
	gistSeqPath, _ := config.DataPath("worldsync_seq.json")
	gistSeq := gistseq.Open(gistSeqPath)
	// vrchat federation fallback for group-role expansion: serves through a paired peer's
	// link when this instance is unlinked. The remotectl endpoint doesn't exist yet - it's
	// late-bound below (nil endpoint = "peer control unavailable" until wired).
	var remoteCtlRef *remotectl.Endpoint
	vrcFed := &peerVrcMembers{endpoint: func() *remotectl.Endpoint { return remoteCtlRef }, peers: peerMgr}
	worldSync := vrcperm.New(vrcperm.Deps{
		Log:  log,
		Cfg:  func() *config.WorldSyncFeature { return &cfg.Features.WorldSync },
		Save: func() { _ = cfg.Save() },
		Seq:  gistSeq,
		// Hosted publish path (rave.page worldlive API under its own service account) - self-gates
		// on a live rave.page session + a configured world id; unused unless PublishMode="hosted".
		Hosted: &worldliveClient{
			api:   apiC,
			token: authMgr.Token,
			cfg:   func() *config.WorldSyncFeature { return &cfg.Features.WorldSync },
		},
		Gists: func() vrcperm.GistStore {
			if !ghAuth.SignedIn() {
				return nil
			}
			return ghGists
		},
		Owner: ghAuth.Login,
		Members: func() vrcperm.MemberSource {
			if vrcMgr.State().LoggedIn {
				return vrcMgr.Client()
			}
			// vrchat federation: expansion keeps working when a PAIRED instance holds
			// the link (endpoint late-bound; nil until remotectl is wired below).
			return vrcFed
		},
		Events: func(ectx context.Context) []vrcperm.Event {
			evs, err := apiC.ListEvents(ectx, authMgr.Token(), "", "", 50)
			if err != nil {
				return nil
			}
			now := time.Now()
			var out []vrcperm.Event
			for _, e := range evs {
				if e.Start.Before(now) {
					continue
				}
				out = append(out, vrcperm.Event{Title: e.Title, Date: e.Start.Format("Mon Jan 2 15:04")})
			}
			return out
		},
		NowPlay: func() vrcperm.NowPlaying {
			if agg == nil {
				return vrcperm.NowPlaying{}
			}
			np, ok := agg.Snapshot().DeriveNowPlaying()
			if !ok {
				return vrcperm.NowPlaying{}
			}
			return vrcperm.NowPlaying{
				Live:   true,
				Artist: session.StringField(np.Fields, session.FieldArtist),
				Track:  session.StringField(np.Fields, session.FieldTitle),
			}
		},
	})

	// OBS control across instances: polls the local OBS bridge → broadcasts stream/record status on
	// the bus, and serves directed start/stop commands. A VR/overlay instance renders peers' status
	// + sends commands; an OBS PC executes them. Always on (cheap when no local OBS).
	obsLabel, _ := os.Hostname()
	if obsLabel == "" {
		obsLabel = "rave-mate"
	}
	obsControl := obscontrol.New(log, bus, obsW, obsLabel, ident.NodeID, func() []obscontrol.Remote {
		var out []obscontrol.Remote
		for _, r := range cfg.Features.OBS.Remotes {
			if !r.Enabled || r.Host == "" {
				continue
			}
			out = append(out, obscontrol.Remote{ID: r.ID(), Label: r.ResolvedName(), Host: r.Host, Port: r.ResolvedPort(), Password: r.Password})
		}
		return out
	})
	// Media-sync tier: chase chosen OBS media sources to the house clock (config re-read live).
	obsControl.SetSyncConfig(func() obscontrol.SyncConfig {
		s := cfg.Features.OBS.Sync
		srcs := make([]obscontrol.SyncSource, 0, len(s.Sources))
		for _, src := range s.Sources {
			srcs = append(srcs, obscontrol.SyncSource{
				Endpoint: src.Endpoint, InputName: src.InputName, InputKind: src.InputKind,
				StaticOffsetMs: src.StaticOffsetMs, Enabled: src.Enabled,
			})
		}
		return obscontrol.SyncConfig{
			Enabled: s.Enabled, DeadBandFrames: s.DeadBandFrames, Fps: s.Fps,
			RestartThresholdMs: s.RestartThresholdMs, Sources: srcs,
		}
	})

	// Webcam/UVC source (medialink P5): supervised ffmpeg dshow capture → local Spout sender +
	// UVC PTZ/exposure control, driveable from a paired instance over media.cam.* on the bus.
	webcamMgr := webcam.New(log, bus, ident.NodeID, obsLabel, func() config.WebcamFeature { return cfg.Features.Webcam })
	webcamMgr.SetRouter(mediaRouter) // P4: a running camera advertises as routable source "webcam"
	if mediaInProc {                 // in-proc: the UI drives the managers directly (child path set them above)
		mediaRoutesCtl = mediaRoutes
		webcamCtl = webcamMgr
	} // else: child mode (proxies set above) OR child-spawn failed -> ctls stay nil = plane disabled

	// VR perf/debug telemetry collector - receives vr.perf samples from any instance (incl. this one),
	// for the UI monitor + `rave-mate ctl vrperf`. Always on (cheap; works even with no local VR), so a
	// non-VR machine can monitor a headset PC's frame timing remotely.
	vrPerf := vrstats.New(bus)

	// VR overlays (OpenVR/SteamVR). Renders bus-sourced content (Twitch chat/alerts - local OR a
	// peer's) into the headset. Real OpenVR backend only in a `vr` build; else a no-op stub.
	vrOverlay := vroverlay.New(log, bus, vroverlay.NewRuntime(),
		func() config.VROverlayFeature { return cfg.Features.VROverlay },
		func(fn func(*config.VROverlayFeature)) { fn(&cfg.Features.VROverlay); _ = cfg.Save() })
	// Task #4: on vr builds OpenVR runs in the supervised `feature vr` child by default - a cgo/GPU
	// fault kills only the child. vrSurf routes every daemon-side call (UI, keybinds, ctl vrinput) to
	// the child's proxy or the in-proc manager (fallback: non-vr build / InProc opt-out / proxy init
	// failure). vrProc is constructed below once its deps (vrcTools, keyBinds) exist.
	var vrProc *featurehost.VrOverlayProxy
	vrSurf := &vrSurface{mgr: vrOverlay, useProxy: func() bool {
		return vrProc != nil && cfg.Features.VROverlay.SubprocessEnabled(vroverlay.BuiltWithVR())
	}}

	// Always-on perf collector (1 Hz rings, ~10 min) + probe registry - `ctl perf` locally,
	// remotectl app.perf from a paired peer. Probes: subsystem counters without perfmon imports.
	perfMon := perfmon.New()
	debuglog.Go(log, "perfmon", func() { perfMon.Run(ctx) })
	perfMon.SetChildren(func() []perfmon.ChildProc {
		kids := featurehost.Children()
		out := make([]perfmon.ChildProc, len(kids))
		for i, k := range kids {
			out[i] = perfmon.ChildProc{Name: k.Name, PID: k.PID, Ready: k.Ready, Restarts: k.Restarts, LastErr: k.LastErr}
		}
		return out
	})
	// Live-stats VR overlays (perf/network/timing kinds) read the always-on perf ring + net sampler.
	vrOverlay.SetStatsProviders(perfMon.Snapshot, netSampler.Snapshot)
	// Late-bind the encode planner's CPU-load input (perfmon is built after the media plane).
	mediaCapsPlan.SetCPUSource(func() float64 {
		if ss := perfMon.Snapshot(); len(ss) > 0 {
			return ss[len(ss)-1].CPUPct
		}
		return 0
	})
	perfmon.RegisterProbe("vroverlay", vrSurf.PerfProbe)
	perfmon.RegisterProbe("eventbus", bus.Stats)
	perfmon.RegisterProbe("session.merger", merger.Stats)
	perfmon.RegisterProbe("featurehost.frames", featurehost.FrameStats)
	perfmon.RegisterProbe("logbus", func() string { return perfmon.LogCounts(log, 10*time.Minute) })

	// Keybind dispatcher: maps app actions (OBS rec/stream per instance, overlay show/hide) to
	// handlers. Fed by MIDI here (forwarder above) and by VR action slots in the overlay editor.
	keyBinds := vrbind.NewDispatcher()
	// editor.toggle + overlays.toggle are in the bindable catalog (vrbind.Actions) but were previously
	// unregistered - so binding them granularly silently no-op'd (only the hardwired summon tap/hold
	// worked). Register them so distinct inputs can drive menu-toggle and show/hide-all independently.
	keyBinds.Register(vrbind.ActEditorToggle, func(string) { vrSurf.RequestEditorToggle() })
	keyBinds.Register(vrbind.ActOverlaysToggle, func(string) { vrSurf.ToggleAllOverlays() })
	keyBinds.Register(vrbind.ActOverlayToggle, func(t string) { vrSurf.ToggleHidden(t) })
	keyBinds.Register(vrbind.ActOverlayShow, func(t string) { vrSurf.SetHidden(t, false) })
	keyBinds.Register(vrbind.ActOverlayHide, func(t string) { vrSurf.SetHidden(t, true) })
	keyBinds.Register(vrbind.ActOBSRecord, func(t string) {
		_ = obsControl.Command(context.Background(), obscontrol.Cmd{Target: t, Action: obscontrol.ActRecordToggle})
	})
	keyBinds.Register(vrbind.ActOBSStream, func(t string) {
		_ = obsControl.Command(context.Background(), obscontrol.Cmd{Target: t, Action: obscontrol.ActStreamToggle})
	})
	keyBinds.Register(vrbind.ActOBSMic, func(t string) { // t = OBS input/source name, local OBS
		_ = obsControl.Command(context.Background(), obscontrol.Cmd{Action: obscontrol.ActMicToggle, Arg: t})
	})
	vrOverlay.SetBindDispatcher(keyBinds, func() []vrbind.Bind { return cfg.Features.VROverlay.Binds })

	// Speech-to-text (Whisper) → Twitch chat. Dictation is driven by keybinds (record/send/discard)
	// and/or auto-submit on silence; finished transcripts post to chat via the Twitch manager.
	sttCtl := stt.NewController(parent,
		func() stt.Options {
			f := cfg.Features.STT
			o := stt.Options{Device: f.InputDevice, Model: f.Model, Threshold: f.Threshold}
			if f.AutoSubmit {
				o.AutoSilence = time.Duration(f.ResolvedSilenceMs()) * time.Millisecond
			}
			return o
		},
		func(text string) { _ = twitchW.SendChat(context.Background(), text, "") },
		func(s string) { log.Info("stt", s, nil) },
	)
	keyBinds.Register(vrbind.ActSTTRecord, func(string) { sttCtl.Toggle() })
	keyBinds.Register(vrbind.ActSTTSend, func(string) { sttCtl.Send() })
	keyBinds.Register(vrbind.ActSTTDiscard, func(string) { sttCtl.Discard() })
	keyBinds.Register(vrbind.ActSTTClipboard, func(string) { sttCtl.CopyToClipboard() })

	// DMX plane: Art-Net ingest → universe store → VRSL video grid (Spout/PNG) + optional
	// re-emit. Config re-read on module (re)start.
	dmxRouter := dmx.New(log, func() config.DMXFeature { return cfg.Features.DMX },
		func() config.LightCueFeature { return cfg.Features.LightCue }, dmxGridPNGPath())

	// DMX→MIDI VRChat bridge: universe store → rate-limited MIDI CC on a virtual port for
	// VRChat --midi worlds (change-detected + hard-capped under the ~128 events/frame crash bug).
	vrcMidiBridge := vrcmidi.New(log, dmxRouter.Store(), func() config.DMXMIDIFeature { return cfg.Features.DMXMIDI })

	// Software MIDI test controller: a pad/CC surface (webui "MIDI Controller" tab) emitting MIDI
	// to a loopback port (LoopBe1) so a DJ app can MIDI-learn custom mappings. Lazily opens the port
	// on first send; the panel persists a port change to cfg + calls SetPort.
	midiEmit := midiemit.New(log, cfg.Features.MIDIController.Device)

	// Local RTSP performer chain: supervised ffmpeg encode of a configured source →
	// RTSP/TCP for VRChat AVPro (rtspt://) - no OBS/MediaMTX relay.
	rtspSrv := rtspserve.New(log, func() config.RTSPServeFeature { return cfg.Features.RTSPServe })

	// Mocap capture master: capture node (desktop/Spout/dshow reads the in-world mocap panel) →
	// pose store → composite mocap region overlaid on the VRSL stream (extended mode).
	mocapSvc := mocap.New(log, func() config.MocapFeature { return cfg.Features.Mocap })

	// Capture-crew relay: uplink this rig's decoded mocap packets to the event's master
	// (role=node) or ingest remote crew packets into the local master (role=master), over the
	// rave.page mocap relay rooms. Bearer = the signed-in account; base = the API root.
	crewSvc := crewlink.New(log, func() config.CrewFeature { return cfg.Features.Crew },
		cfg.APIBaseURL, authMgr, mocapSvc)

	// VRSL DMX-over-video stream: render the shared DMX store → VRSL grid → ffmpeg → RTMP/WHIP push
	// for VRChat playback. Reuses dmxRouter's store so one Art-Net listener serves both (it owns its
	// own listener only when the DMX plane is off). In-proc rtspserve-style ffmpeg supervisor.
	// mocapSvc.Overlay is the per-frame overlay provider (nil while the mocap module is off).
	vrslStream := vrslstream.New(log, dmxRouter.Store(),
		func() config.StreamFeature { return cfg.Features.Stream },
		func() bool { return cfg.Features.DMX.Enabled },
		func() config.DMXFeature { return cfg.Features.DMX },
		mocapSvc.Overlay)

	// Application groups: relaunch a DJ-rig app set (crash recovery). LaunchGroup may sleep
	// (per-app DelayMs), so fire it off-thread so a MIDI/VR dispatch isn't blocked.
	appGroups := appgroups.New(log, func() []config.AppGroup { return cfg.Features.AppGroups.Groups })
	keyBinds.Register(vrbind.ActAppGroupLaunch, func(t string) {
		debuglog.Go(log, "appgroups", func() { _, _, _ = appGroups.LaunchGroup(t) })
	})

	// House SMPTE timecode outputs (LTC audio / MTC / Art-Net) - one master clock external
	// gear/software chases. In-proc, no cgo; the clock is started from the UI or ctl.
	tcSvc := timecode.New(log, func() config.TimecodeFeature { return cfg.Features.Timecode })
	tcSvc.FollowClock(mediaClock.Now) // TC egress advances on the medialink-disciplined domain
	tcSvc.AttachPlane(tcPlane)        // master announces / slave chase (MEDIALINK_DESIGN.md §4)

	// VRChat tools: tail the VRChat log → location timeline, organize screenshots + camera paths
	// per world, publish location/organize telemetry on the bus (observable from a paired instance).
	vrcDir, _ := config.Dir()
	vrcTools := vrctools.New(log, bus, func() config.VRCToolsFeature { return cfg.Features.VRCTools }, vrcDir)
	// Primary photo organize key: file screenshots under the rave.page event whose window covers the
	// capture time (best-effort; falls back to the world timeline). Uses the current access token.
	vrcTools.SetEventSource(vrctools.NewAPIEventSource(apiC, authMgr.Token, log))
	// In-VR camera-path picker: list paths (current world first) + load over OSC. Named closures -
	// shared by the in-proc manager and the vr subprocess proxy.
	vrCamList := func() []vroverlay.CamPathItem {
		cur, _ := vrcTools.CurrentWorld()
		paths := vrcTools.CamPaths()
		sort.SliceStable(paths, func(i, j int) bool {
			ci := paths[i].WorldID == cur.WorldID && cur.WorldID != ""
			cj := paths[j].WorldID == cur.WorldID && cur.WorldID != ""
			if ci != cj {
				return ci // current-world paths first
			}
			return paths[i].SavedAt.After(paths[j].SavedAt)
		})
		items := make([]vroverlay.CamPathItem, 0, len(paths))
		for _, p := range paths {
			label := p.Name
			if p.Local {
				label = "[player] " + p.Name
			} else if p.WorldName != "" {
				label = p.WorldName + " · " + p.Name
			}
			items = append(items, vroverlay.CamPathItem{Label: label, File: p.File})
		}
		return items
	}
	vrCamGeom := func(file string) vroverlay.CamPathGeom {
		pts, err := vrccampaths.LoadPoints(file)
		if err != nil {
			return vroverlay.CamPathGeom{}
		}
		g := vroverlay.CamPathGeom{Pts: make([][3]float32, len(pts)), Spd: make([]float32, len(pts)), Dur: make([]float32, len(pts))}
		for i, p := range pts {
			g.Pts[i] = [3]float32{float32(p.Position.X), float32(p.Position.Y), float32(p.Position.Z)}
			g.Spd[i] = float32(p.Speed)
			g.Dur[i] = float32(p.Duration)
		}
		return g
	}
	// Per-world overlay layouts: current world from the VRChat location timeline (auto-apply/notify).
	vrWorld := func() (string, string, bool) {
		loc, ok := vrcTools.CurrentWorld()
		return loc.WorldID, loc.WorldName, ok
	}
	vrOverlay.SetCamPathProvider(vrCamList, vrcTools.LoadCamPath, vrCamGeom)
	vrOverlay.SetWorldSource(vrWorld)
	// The vr subprocess proxy: same deps, pushed over the pipe (full-state, idempotent). A nil proxy
	// (exe-path resolution failed) falls back to the in-proc manager via vrSurf.
	var vperr error
	vrProc, vperr = featurehost.NewVrOverlayProxy(log, featurehost.VROverlayDeps{
		Cfg:         func() config.VROverlayFeature { return cfg.Features.VROverlay },
		Mutate:      func(fn func(*config.VROverlayFeature)) { fn(&cfg.Features.VROverlay); _ = cfg.Save() },
		Bus:         bus,
		FireBind:    func(b vrbind.Bind) { keyBinds.Fire(b) },
		LoadCamPath: vrcTools.LoadCamPath,
		CamPaths:    vrCamList,
		CamPathGeom: vrCamGeom,
		World:       vrWorld,
		StatsPerf:   perfMon.Snapshot,
		StatsNet:    netSampler.Snapshot,
	})
	if vperr != nil {
		log.Warn("vroverlay", "vr subprocess proxy init failed - falling back to in-process overlays", map[string]any{"error": vperr.Error()})
	}
	vrSurf.proxy = vrProc
	// MIDI learn: a one-shot capture of the next press, for the keybind editor's "Learn MIDI".
	var midiLearnMu sync.Mutex
	var midiLearnCB func(port string, status, data1 byte)
	// Desktop-UI bind groups honor the MIDI-tab master toggle (VR binds stay always-on).
	keyBinds.SetGroupFilter(func(group string) bool {
		return group == vrbind.GroupVR || !cfg.Features.MIDI.DisableUIBinds
	})
	// Per-device mapping profiles: a desktop-UI bind is inert while its device's profile is
	// paused (VR-group binds are unaffected, same carve-out as the master toggle).
	keyBinds.SetProfileFilter(func(group, bindPort string) bool {
		return group == vrbind.GroupVR || !cfg.Features.MIDI.BindProfileDisabled(bindPort)
	})
	// MIDI → binds. A pending learn capture wins on the press edge (the message must not also
	// fire an already-bound action mid-learn); otherwise the dispatcher applies the full mode
	// semantics (hold press/release, toggle, encoders) - releases DO flow through.
	dispatchMIDIBind = func(port string, m midi.Message) {
		k := m.Status & 0xF0
		press := k != 0x80 && !((k == 0x90 || k == 0xB0) && m.Data2 == 0)
		if press {
			midiLearnMu.Lock()
			cb := midiLearnCB
			midiLearnMu.Unlock()
			if cb != nil { // learning: capture this press, don't trigger a bind
				cb(port, m.Status, m.Data1)
				return
			}
		}
		keyBinds.FireMIDIMsg(cfg.Features.VROverlay.Binds, port, m.Status, m.Data1, m.Data2)
	}
	midiLearn := func(onCapture func(port string, status, data1 byte)) func() {
		midiLearnMu.Lock()
		midiLearnCB = func(p string, s, d byte) {
			midiLearnMu.Lock()
			midiLearnCB = nil // one-shot
			midiLearnMu.Unlock()
			onCapture(p, s, d)
		}
		midiLearnMu.Unlock()
		return func() { midiLearnMu.Lock(); midiLearnCB = nil; midiLearnMu.Unlock() }
	}

	// Media-automation engine (file watchers + schedules; runs in the daemon, headless too).
	var autoMgr *automation.Service
	var autoIface automation.Manager
	// Hoisted out of the store-gated block: the studio wire validates loudness overrides against
	// the same presets the engine resolves, whether or not the engine itself came up.
	presetResolver := automation.PresetResolver(func(id string) (transcode.Preset, bool) {
		for _, p := range transcode.AllPresets(cfg.Features.Transcode.Presets) {
			if p.ID == id {
				return p, true
			}
		}
		return transcode.Preset{}, false
	})
	if st != nil {
		var autoWorker automation.Worker
		if workers != nil {
			autoWorker = workers
		}
		autoMgr = automation.NewManager(st, autoWorker, presetResolver, log)
		autoIface = autoMgr
	}

	// LAN peer-control RPC: a paired instance drives this one's automations + library and
	// browses this machine's filesystem (streamed, never a native dialog) - same method system
	// as the Local Studio browser-control channel, carried over the peerlink control channel.
	remoteCtl := remotectl.New(log, func(nodeID string, payload []byte) error {
		return peerMgr.SendTo(nodeID, peerlink.ChanControl, payload)
	})
	peerBridge.SetControlSink(remoteCtl.OnControl)
	remoteCtlRef = remoteCtl // arms the vrchat-federation fallback (vrcFed above)
	remotectl.RegisterBrowse(remoteCtl)
	remotectl.RegisterAutomations(remoteCtl, autoIface)
	// vrchat federation: serve THIS instance's VRChat link to paired peers (the session
	// never crosses the link - only calls do), and ARM the consuming side: with no local
	// session, a linked peer serves every VRChat feature as if logged in locally.
	remotectl.RegisterVrchat(remoteCtl, vrcMgr)
	debuglog.Go(log, "vrchat-federation", func() {
		runVrcFederationWatcher(ctx, log, vrcMgr, peerMgr, func() *remotectl.Endpoint { return remoteCtl })
	})
	remotectl.RegisterLibrary(remoteCtl, lib)
	// Remote cue/beatgrid/drop editing: a paired controller pulls a track's audio + edits
	// locally, then writes back here. Writes publish library.trackchanged so every open UI
	// (and the controller, via the bus's peer broadcast) refreshes.
	cueBackupRoot, _ := config.DataPath("library-backups")
	remotectl.RegisterLibraryCueEdit(remoteCtl, lib, bus.Publish,
		func() string { return strings.TrimSpace(cfg.Features.NML.CollectionPath) }, cueBackupRoot)
	remotectl.RegisterMedia(remoteCtl, transcodeHub)
	remotectl.RegisterRecorder(remoteCtl, rec, lib) // peer-driven Publish cockpit (list/tracklist/captures/export/rename/delete)
	if rec != nil {
		// peer-driven Traktor-history reconciliation - the match runs on THIS box (its history dir +
		// library metadata resolver), triggered by a paired controller's remote Publish tab.
		remotectl.RegisterRecorderMatch(remoteCtl, func(id string) (recorder.Recording, error) {
			histDir := cfg.Features.NML.HistoryDir
			if histDir == "" {
				if ins, e := musiclib.DiscoverTraktor(); e == nil && len(ins) > 0 {
					histDir = ins[0].HistoryDir
				}
			}
			if histDir == "" {
				return recorder.Recording{}, fmt.Errorf("no Traktor history folder")
			}
			resolve := func(path string) (recorder.HistoryMeta, bool) {
				if lib == nil {
					return recorder.HistoryMeta{}, false
				}
				t, ok, _ := lib.TrackByPath(path)
				if !ok {
					return recorder.HistoryMeta{}, false
				}
				return recorder.HistoryMeta{Title: t.Title, Artist: t.Artist, Album: t.Album, Key: t.Key, BPM: t.BPM}, true
			}
			rr, err := rec.ReconcileWithHistory(id, histDir, resolve)
			if err != nil {
				return recorder.Recording{}, err
			}
			return *rr, nil
		})
	}
	// Motion Studio recordings replicate across paired peers over the same control channel.
	motionDir := motionRecordingsDir()
	remotectl.RegisterMotionSync(remoteCtl, motionDir)
	// Pull peers' recordings on every (re)connect + an initial pass, off the main thread.
	reconcileMotion := func() {
		if motionDir == "" {
			return
		}
		debuglog.Go(log, "assetsync", func() {
			rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			results := assetsync.ReconcileAllPeers(rctx, peerMgr,
				func(nodeID string) assetsync.MotionClient { return remotectl.NewClient(remoteCtl, nodeID) },
				motionDir)
			for node, r := range results {
				if len(r.Pulled) > 0 || len(r.Errors) > 0 {
					log.Info("assetsync", "motion reconcile",
						map[string]any{"peer": node, "pulled": len(r.Pulled), "skipped": r.Skipped, "errors": len(r.Errors)})
				}
			}
		})
	}
	// VRM/GLB avatar models replicate the same way (chunked - avatars are large). Served from the
	// managed avatars dir; pulled into it on (re)connect.
	vrmDir := config.VRMAvatarsDir()
	remotectl.RegisterVRMSync(remoteCtl, vrmDir)
	// reconcileVRMNow: synchronous all-peer avatar reconcile with aggregate counts - also the UI's
	// "Sync now" action (Services.SyncVRMAvatars).
	reconcileVRMNow := func() (pulled, skipped, errored int) {
		if vrmDir == "" {
			return 0, 0, 0
		}
		rctx, cancel := context.WithTimeout(ctx, 5*time.Minute) // large files → generous window
		defer cancel()
		results := assetsync.ReconcileVRMAllPeers(rctx, peerMgr,
			func(nodeID string) assetsync.VRMClient { return remotectl.NewClient(remoteCtl, nodeID) },
			vrmDir)
		for node, r := range results {
			pulled += len(r.Pulled)
			skipped += r.Skipped
			errored += len(r.Errors)
			if len(r.Pulled) > 0 || len(r.Errors) > 0 {
				log.Info("assetsync", "vrm reconcile",
					map[string]any{"peer": node, "pulled": len(r.Pulled), "skipped": r.Skipped, "errors": len(r.Errors)})
			}
		}
		return pulled, skipped, errored
	}
	reconcileVRM := func() {
		debuglog.Go(log, "assetsync", func() { reconcileVRMNow() })
	}
	peerMgr.AddListener(nil, func() { reconcileMotion(); reconcileVRM() }) // fires on any connect/disconnect
	reconcileMotion()                                                      // initial pass (peers already connected)
	reconcileVRM()

	// Local Studio WS control channel - browser drives this desktop's media/transcode/
	// automations. Created here so it gets the store (favorites/presets) + automation engine.
	studioSrv := studio.New(log, apiC, authMgr, studioRunner, transcodeHub, st, autoIface, presetResolver)
	authMgr.OnChange(func(auth.State) { studioSrv.OnDesktopTokenChanged() })
	// Peer gateway: lets the web Local Studio manage + drive paired rave-mate instances on
	// other machines (peers.* + remote-context routing over the existing remotectl).
	studioSrv.SetPeerGateway(newPeerGateway(peerMgr, disc, remoteCtl))
	// VRChat gateway: web Local Studio queries/updates THIS desktop's VRChat session via
	// rave-mate (no server-side token vault). Advertised only when the feature is on + signed in.
	studioSrv.SetVrchatGateway(newVrchatGateway(vrcMgr, func() bool { return cfg.Features.VRChat.Enabled }))
	// OBS + app-group gateways: web Local Studio drives THIS desktop's OBS (status, settings,
	// presets, stream/record) + app groups (launch/readiness); quickAction.streamReady composes
	// them into one-tap go-live. Advertised per-connection when the features are on.
	studioSrv.SetObsGateway(newObsGateway(obsW, obsControl, func() config.OBSFeature { return cfg.Features.OBS }))
	studioSrv.SetAppGroupGateway(newAppGroupGateway(appGroups,
		func() bool { return cfg.Features.AppGroups.Enabled },
		func() []config.AppGroup { return cfg.Features.AppGroups.Groups }))

	// Editor bridge - edit-time loopback RPC for the Unity world toolkit (page.rave.mate). Runs with
	// the WorldSync feature (its edit-time half); gateways self-gate live (vrchat needs a signed-in
	// session, worldsync needs GitHub linked). The instance pointer is fed from the live VRChat
	// session + location timeline.
	worldSync.SetPointerProvider(pointerProvider(vrcMgr, vrcTools.CurrentWorld))
	editorBridge := newEditorBridge(editorBridgeDeps{
		VRChat:     vrcMgr,
		WorldSync:  worldSync,
		Cfg:        func() *config.WorldSyncFeature { return &cfg.Features.WorldSync },
		Owner:      ghAuth.Login,
		VRCEnabled: func() bool { return cfg.Features.VRChat.Enabled },
		Seq:        gistSeq,
		Version:    version.Version,
	})

	// ── rave.page account bridge ────────────────────────────────────────────────
	// Reaches THIS instance from off-LAN through the account's blind relay. The relay is a
	// transport only: peerlink authenticates + encrypts + gates every tunnel, so rave.page
	// never sees a payload, a code or a token.
	authGate := authz.New(st, log, "rave-mate", peerNick)
	authGate.SetSelfID(ident.NodeID) // the challenge advertises the Ed25519 node id
	bridgePrompt := &codePrompt{}
	peerMgr.SetAuthorizer(authGate, instanceLabel(cfg), bridgePrompt.ask)

	bridgeCfg := func() config.AccountBridgeFeature { return cfg.Features.AccountBridge }
	bridgeCl := bridge.NewClient(cfg.APIBaseURL, authMgr, log)
	bridgeMgr := bridge.NewManager(bridgeCl, log, ident.NodeID, peerNick, bridgeCaps(bridgeCfg),
		&bridgeTunnel{peers: peerMgr, studio: studioSrv, log: log, cfg: bridgeCfg})
	// Sign-in/out flips the bridge: the relay is account-scoped, so a signed-out instance has
	// nothing to register against.
	authMgr.OnChange(func(s auth.State) {
		if !cfg.Features.AccountBridge.Enabled {
			return
		}
		if s.SignedIn {
			_ = bridgeMgr.Start(ctx)
		} else {
			bridgeMgr.Stop()
		}
	})

	mods := module.NewManager(log, ctx)
	mods.Add(&module.Service{
		Name:    "accountbridge",
		Enabled: func() bool { return cfg.Features.AccountBridge.Enabled },
		Start:   func(c context.Context) error { return bridgeMgr.Start(c) },
		Stop:    bridgeMgr.Stop,
	})
	mods.Add(&module.Service{
		Name:    "automation",
		Enabled: func() bool { return autoMgr != nil },
		Start:   func(c context.Context) error { return autoMgr.Start(c) },
		Stop:    func() { autoMgr.Stop() },
	})
	mods.Add(&module.Service{
		Name:    "traktor",
		Enabled: func() bool { return cfg.Features.Traktor.Enabled },
		// Runs as a child process - bind clashes/crashes surface as host restarts, never
		// daemon failures. Stop reaps the child.
		Start: func(c context.Context) error { return trk.Host().Start(c) },
		Stop:  trk.Host().Stop,
	})
	mods.Add(&module.Service{
		Name:    "studio",
		Enabled: func() bool { return cfg.Features.StudioChannel.Enabled },
		Start:   func(context.Context) error { return studioSrv.Start() },
		Stop:    studioSrv.Stop,
	})
	// Editor bridge - loopback HTTP server + editor-bridge.json handshake file. Bound to WorldSync
	// enablement (edit-time half of the same feature). Start binds the first free 47623-47627 port +
	// writes the 0600 discovery file; Stop tears down the listener + removes the file.
	mods.Add(&module.Service{
		Name:    "editorbridge",
		Enabled: func() bool { return editorBridge != nil && cfg.Features.WorldSync.Enabled },
		Start: func(context.Context) error {
			if editorBridge == nil {
				return nil
			}
			return editorBridge.Start()
		},
		Stop: func() {
			if editorBridge != nil {
				editorBridge.Stop()
			}
		},
	})
	// Icecast set-capture receiver - the local endpoint Traktor's Broadcasting streams to.
	// Child process; the init closure re-reads config on every (re)spawn. Settings edits
	// auto-restart the module (webui settings_apply.go; deferred while capturing).
	mods.Add(&module.Service{
		Name:    "setcapture",
		Enabled: func() bool { return cfg.Features.SetCapture.Enabled },
		Start:   func(c context.Context) error { return icecastRcv.Host().Start(c) },
		Stop:    icecastRcv.Host().Stop,
	})
	// OBS bridge - child process maintaining the obs-websocket connection. Init closure
	// re-reads config per (re)spawn; settings edits auto-restart the module (webui
	// settings_apply.go; deferred while recording).
	mods.Add(&module.Service{
		Name:    "obs",
		Enabled: func() bool { return cfg.Features.OBS.Enabled },
		Start:   func(c context.Context) error { return obsW.Host().Start(c) },
		Stop:    obsW.Host().Stop,
	})
	// Ableton Link - child process publishing the fused DJ tempo/phrase onto a Link session.
	// Start spawns the child then runs the DJ→Link bridge loop (reads the session Merger master
	// BPM/phase and drives Link when this node owns the tempo). Quantum/start-stop-sync edits
	// auto-restart the module (webui settings_apply.go); owner/Resolume fields are read live.
	mods.Add(&module.Service{
		Name:    "abletonlink",
		Enabled: func() bool { return cfg.Features.AbletonLink.Enabled },
		Start: func(c context.Context) error {
			if err := linkW.Host().Start(c); err != nil {
				return err
			}
			debuglog.Go(log, "abletonlink", func() {
				runLinkBridge(c, agg, linkW, func() config.AbletonLinkFeature { return cfg.Features.AbletonLink })
			})
			return nil
		},
		Stop: linkW.Host().Stop,
	})
	// Twitch - supervised feature child. Always listening while enabled + signed in
	// (EventSub + stats polling run tab-open or not; events persist via the chat log).
	mods.Add(&module.Service{
		Name:    "twitch",
		Enabled: func() bool { return cfg.Features.Twitch.Enabled },
		Start:   func(c context.Context) error { return twitchW.Host().Start(c) },
		Stop:    twitchW.Host().Stop,
	})
	mods.Add(&module.Service{
		Name:    "worldsync",
		Enabled: func() bool { return cfg.Features.WorldSync.Enabled },
		Start: func(c context.Context) error {
			debuglog.Go(log, "worldsync", func() { worldSync.Run(c) })
			return nil
		},
	})
	mods.Add(&module.Service{
		Name:    "obscontrol",
		Enabled: func() bool { return true }, // cheap; renders peers + routes commands even w/o local OBS
		Start: func(c context.Context) error {
			debuglog.Go(log, "obscontrol", func() { _ = obsControl.Start(c) })
			return nil
		},
	})
	// VR overlays (task #4): one module, two backends. Default on vr builds = the supervised
	// `feature vr` child (a cgo/OpenVR fault kills only the child; the host restarts it and the proxy
	// re-pushes full state). Fallback = in-proc manager (non-vr build, InProc opt-out, or proxy init
	// failure). Mode is picked per module (re)start so only one backend ever owns SteamVR.
	var vrProcActive bool
	mods.Add(&module.Service{
		Name:    "vroverlay",
		Enabled: func() bool { return cfg.Features.VROverlay.Enabled },
		Start: func(c context.Context) error {
			if vrSurf.useProxy() {
				vrProcActive = true
				return vrProc.Start(c)
			}
			vrProcActive = false
			debuglog.Go(log, "vroverlay", func() { _ = vrOverlay.Start(c) })
			return nil
		},
		Stop: func() {
			if vrProcActive {
				vrProc.Stop()
			}
		},
	})
	mods.Add(&module.Service{
		Name:    "vrctools",
		Enabled: func() bool { return cfg.Features.VRCTools.Enabled },
		Start: func(c context.Context) error {
			debuglog.Go(log, "vrctools", func() { _ = vrcTools.Start(c) })
			return nil
		},
	})
	mods.Add(&module.Service{
		Name:    "vrmonitor",
		Enabled: func() bool { return true }, // cheap; collects VR perf telemetry from any instance
		Start: func(c context.Context) error {
			debuglog.Go(log, "vrmonitor", func() { _ = vrPerf.Start(c) })
			return nil
		},
	})
	mods.Add(&module.Service{
		Name:    "vrchat",
		Enabled: func() bool { return cfg.Features.VRChat.Enabled },
		Start: func(c context.Context) error {
			debuglog.Go(log, "vrchat", func() {
				rctx, rcancel := context.WithTimeout(c, 30*time.Second)
				defer rcancel()
				vrcMgr.Resume(rctx)
			})
			return vrcW.Host().Start(c)
		},
		Stop: vrcW.Host().Stop,
	})
	// The aggregation hub is always up; it internally starts only the enabled sources/sinks
	// (live-toggled via agg.Reconcile after a settings change). Cheap when idle.
	mods.Add(&module.Service{
		Name:    "session",
		Enabled: func() bool { return true },
		Start:   func(c context.Context) error { return agg.Start(c) },
		Stop:    agg.Stop,
	})
	// DMX plane (Art-Net listener + VRSL grid + re-emit). Settings edits auto-restart the
	// module (webui settings_apply.go).
	mods.Add(&module.Service{
		Name:    "dmx",
		Enabled: func() bool { return cfg.Features.DMX.Enabled },
		Start:   dmxRouter.Start,
	})
	// DMX→MIDI VRChat bridge (needs the DMX plane feeding the store to have data to send).
	mods.Add(&module.Service{
		Name:    "dmxmidi",
		Enabled: func() bool { return cfg.Features.DMXMIDI.Enabled },
		Start:   vrcMidiBridge.Start,
	})
	// Local RTSP performer chain (ffmpeg encode + RTSP/TCP server).
	mods.Add(&module.Service{
		Name:    "rtspserve",
		Enabled: func() bool { return cfg.Features.RTSPServe.Enabled },
		Start:   rtspSrv.Start,
	})
	// VRSL DMX-over-video stream (ffmpeg encode → RTMP/WHIP push). Settings edits auto-restart it.
	mods.Add(&module.Service{
		Name:    "vrslstream",
		Enabled: func() bool { return cfg.Features.Stream.Enabled },
		Start:   vrslStream.Start,
	})
	// Mocap capture master (panel capture → composite region on the VRSL stream). Settings edits
	// auto-restart it; the overlay joins/leaves the running stream live (per-frame provider).
	mods.Add(&module.Service{
		Name:    "mocap",
		Enabled: func() bool { return cfg.Features.Mocap.Enabled },
		Start:   mocapSvc.Start,
	})
	// Capture-crew relay (node uplink / master ingest over the event's relay room). Settings
	// edits auto-restart it; the mocap sink seam is restored on stop.
	mods.Add(&module.Service{
		Name:    "crew",
		Enabled: func() bool { return cfg.Features.Crew.Enabled },
		Start:   crewSvc.Start,
	})
	// Webcam/UVC source (medialink P5). Disabled = zero footprint (no ffmpeg child, no COM).
	mods.Add(&module.Service{
		Name:    "webcam",
		Enabled: func() bool { return mediaInProc && cfg.Features.Webcam.Enabled }, // child hosts the cam when isolated; off if isolation was requested but failed
		Start:   webcamMgr.Start,
		Stop:    webcamMgr.Stop,
	})
	// Media plane child (#44): one memory-capped subprocess hosts medialink + mediaroute + webcam
	// (the default; explicit MediaLink.Subprocess=false opts out). Runs while EITHER peers or webcam is enabled (both live inside
	// it). tcPlane + mediaClock stay daemon-side and mirror the child's clock.
	if useMediaChild {
		mods.Add(&module.Service{
			Name:    "mediaplane",
			Enabled: func() bool { return cfg.Features.Peers.Enabled || cfg.Features.Webcam.Enabled },
			Start:   func(c context.Context) error { return mediaCtl.Start(c) },
			Stop:    mediaCtl.Stop,
		})
	}
	// Timecode outputs: the module arms the feature (nothing runs until the user starts the
	// clock); disabling stops any running clock + closes every sink.
	mods.Add(&module.Service{
		Name:    "timecode",
		Enabled: func() bool { return cfg.Features.Timecode.Enabled },
		Start:   func(context.Context) error { return nil },
		Stop:    tcSvc.StopClock,
	})
	// Native audio recorder: the Run loop (OBS follow + ctx-bound) must not block Start.
	mods.Add(&module.Service{
		Name:    "audiorecord",
		Enabled: func() bool { return cfg.Features.AudioRecord.Enabled },
		Start: func(c context.Context) error {
			debuglog.Go(log, "audiorecord", func() { _ = audioRec.Run(c) })
			return nil
		},
	})
	// LAN peer link: starts the peer listener, then mDNS discovery advertising that port,
	// and auto-dials trusted rediscovered peers. SetEnabled("peers", …) is the toggle.
	var peerUnsub func()
	mods.Add(&module.Service{
		Name:    "peers",
		Enabled: func() bool { return cfg.Features.Peers.Enabled },
		Start: func(c context.Context) error {
			if err := peerMgr.Start(c); err != nil {
				return err
			}
			disc.SetPort(peerMgr.Port())
			// Loopback-bound rigs (RAVE_MATE_PEER_BIND) skip mDNS: the listener isn't
			// LAN-reachable and discovery only advertises LAN IPs. RAVE_MATE_PEER_SEED
			// dials literal addresses instead (also useful on multicast-less networks).
			if peerlink.BindIsLoopback() {
				log.Info("peers", "loopback bind - discovery skipped", map[string]any{"port": peerMgr.Port()})
			} else {
				if err := disc.Start(c); err != nil {
					peerMgr.Stop()
					return err
				}
				ch, unsub := disc.Subscribe()
				peerUnsub = unsub
				debuglog.Go(log, "peers", func() {
					for found := range ch {
						peerMgr.OnDiscovered(found)
					}
				})
			}
			if seeds := peerlink.SeedAddrs(); len(seeds) > 0 {
				debuglog.Go(log, "peers", func() {
					t := time.NewTicker(5 * time.Second)
					defer t.Stop()
					for {
						for _, a := range seeds {
							peerMgr.ConnectAddr(a) // no-op while connected to a
						}
						select {
						case <-c.Done():
							return
						case <-t.C:
						}
					}
				})
			}
			// Bridge now-playing/MIDI/control over the link while peers are enabled.
			debuglog.Go(log, "peerbridge", func() { peerBridge.Start(c, merger) })
			// LAN media plane (medialink): bind the media listener + negotiation. Non-fatal -
			// a busy port range only disables the media plane, never the peer link. When the plane runs
			// in the isolated child (#44) the "mediaplane" module owns its lifecycle instead.
			if mediaInProc {
				if err := mediaRouter.Start(c); err != nil {
					log.Warn("medialink", "media plane disabled", map[string]any{"error": err.Error()})
				} else {
					mediaRoutes.Start(c) // P4: Spout share scanner + receive-route cleanup
				}
			}
			tcPlane.Start(c) // timecode master election + media.tc announces (daemon-side either way)
			return nil
		},
		Stop: func() {
			if peerUnsub != nil {
				peerUnsub()
				peerUnsub = nil
			}
			tcPlane.Stop()
			if mediaInProc {
				mediaRouter.Stop()
			}
			disc.Stop()
			peerMgr.Stop()
		},
	})
	// File transfer between paired instances. Own module so the Peers-tab toggle
	// starts/stops it live; disabled = no listener, zero footprint.
	mods.Add(&module.Service{
		Name:    "filexfer",
		Enabled: func() bool { return cfg.Features.FileXfer.Enabled },
		Start:   func(c context.Context) error { return fileXfer.Start(c) },
		Stop:    fileXfer.Stop,
	})
	mods.StartEnabled()

	shutdown := func() {
		mods.StopAll()
		midiEmit.Close()  // release the MIDI-out loopback port if the test controller opened it
		tcSvc.StopClock() // module Stop only fires if it ran; belt-and-braces for a ctl-started clock
		if workers != nil {
			workers.Stop()
		}
		_ = st.Close()
		_ = lib.Close()
		if s := pub.Status(); s.IsLive {
			endCtx, c := context.WithTimeout(context.Background(), 8*time.Second)
			defer c()
			_, _ = pub.End(endCtx)
		}
	}

	if serviceMode {
		ctl := &appControl{log: log, auth: authMgr, cfg: &cfg, mods: mods, vrStats: vrPerf, vrOverlay: vrSurf, perfMon: perfMon, peerMgr: peerMgr, remoteCtl: remoteCtl, rec: rec, lib: lib, syncer: syncer, appGroups: appGroups, dmxR: dmxRouter, vrslStream: vrslStream, mocap: mocapSvc, crew: crewSvc, tc: tcSvc, obsControl: obsControl, media: mediaRouter, mediaCaps: mediaCapsPlan, obs: obsW, ableLink: linkW, guardDisarm: guardDisarm, quit: cancel}
		remotectl.RegisterScreenshot(remoteCtl, ctl)  // peer-driven app/VR-View screenshot (VR works headless)
		remotectl.RegisterVRDiag(remoteCtl, ctl)      // peer-driven VR input/binding diagnostics
		remotectl.RegisterPerf(remoteCtl, ctl)        // peer-driven perf diagnosis (remote-perf)
		remotectl.RegisterLogs(remoteCtl, ctl)        // peer-driven log tail (remote-logs)
		remotectl.RegisterEncoderScan(remoteCtl, ctl) // peer-driven encoder-utilization scan (remote-encoder-scan)
		remotectl.RegisterPprof(remoteCtl, ctl)       // peer-driven pprof capture (remote-pprof-cpu/-heap, remote-goroutines)
		remotectl.RegisterUpdater(remoteCtl, ctl)     // peer-driven self-update+relaunch (remote VR-PC update)
		ctl.startDJSyncScheduler()
		debuglog.Go(log, "ctl", func() { inst.serve(ctl, log) })
		if deepLink != "" {
			authMgr.HandleDeepLink(deepLink)
		}
		<-ctx.Done()
		log.Info("app", "shutting down", nil)
		shutdown()
		return nil
	}

	tmap := traktormap.New(log)
	tmap.SetVersion(cfg.Features.Traktor.MappingVersion) // "" = auto (newest)
	var ctl *appControl                                  // forward-declared so Services callbacks can reach the control
	svc := ui.Services{
		Log: log, Cfg: &cfg, API: apiC, Auth: authMgr,
		ReconcileLibSync: func() {
			if ctl != nil {
				ctl.ReconcileLibSync()
			}
		},
		SyncVRMAvatars: reconcileVRMNow,
		Traktor:        trk, Stream: pub, Player: player, Modules: mods, Workers: workers, Hub: transcodeHub, Store: st,
		Lib: lib, OverlayArt: overlayArt, OverlayWeb: overlayWebSink, Syncer: syncer, Automations: autoIface, Session: agg, Recorder: rec, SetCapture: icecastRcv, OBS: obsW,
		AbleLink:   linkW,
		AudioRec:   audioRec,
		TraktorMap: tmap, Identity: ident, Peers: peerMgr, Discovery: disc, PeerBridge: peerBridge, NetStats: netSampler, Perf: perfMon,
		Bridge: bridgeMgr, AuthGate: authGate,
		EventBus:  bus,
		RemoteCtl: remoteCtl, Vrchat: vrcMgr, VrchatPipe: vrcW, Twitch: twitchW, TwitchLog: twitchLog, VROverlay: vrSurf, OBSControl: obsControl, VRStats: vrPerf, VRCTools: vrcTools,
		GitHub: ghAuth, WorldSync: worldSync,
		STT:         sttCtl,
		AppGroups:   appGroups,
		DMX:         dmxRouter,
		DMXMIDI:     vrcMidiBridge,
		MIDIEmit:    midiEmit,
		MIDISource:  midiSrc,
		RTSP:        rtspSrv,
		VRSLStream:  vrslStream,
		Mocap:       mocapSvc,
		Crew:        crewSvc,
		Timecode:    tcSvc,
		Media:       mediaCtl,
		MediaRoutes: mediaRoutesCtl,
		TCPlane:     tcPlane,
		Webcam:      webcamCtl,
		FileXfer:    uiFileXfer{Manager: fileXfer, peers: peerMgr},
		VrchatUplink: func(on bool) {
			if on {
				vrcUplinkStore()
			} else {
				vrcUplinkDelete()
			}
		},
		MIDIMon: midiMon, TraktorMon: traktorMon, SessionMon: sessionMon,
		MIDILearn:      midiLearn,
		BindDispatcher: keyBinds,
		IDMarks:        idMarks,
	}
	// Renderer seam: Fyne (default) or the Go-driven HTML/CSS webview (features.ui.renderer).
	// Both satisfy `frontend`; only ONE is constructed (each builds a window). Coexist until parity.
	var u frontend
	// webview is the default; Fyne only on an explicit renderer="fyne" or when the webview host is
	// unavailable (non-cgo/non-Windows build) or the WebView2 runtime is missing at runtime
	// (probeWebview) - so a box without WebView2 degrades to Fyne, never a blank window.
	if cfg.Features.UI.UseWebview() && webui.Available() && webui.ProbeWebview() {
		u = webui.New(svc)
		log.Info("ui", "renderer=webview (HTML/CSS)", nil)
	} else {
		if cfg.Features.UI.UseWebview() && webui.Available() {
			log.Warn("ui", "webview default selected but WebView2 runtime not detected - using Fyne", nil)
		} else if cfg.Features.UI.UseWebview() {
			log.Warn("ui", "webview default selected but host unavailable (nocgo/non-windows) - using Fyne", nil)
		}
		u = ui.New(svc)
	}
	studioSrv.SetPicker(u)     // native file dialogs for the web client (windowed mode only)
	mods.SetNotifier(u.Notify) // surface live module failures as desktop toasts
	fileXfer.SetNotify(u.Notify)
	// Feature-subprocess crash toasts.
	trk.Host().SetNotifier(u.Notify)
	midiSrc.Host().SetNotifier(u.Notify)
	icecastRcv.Host().SetNotifier(u.Notify)
	pub.Host().SetNotifier(u.Notify)
	obsW.Host().SetNotifier(u.Notify)
	linkW.Host().SetNotifier(u.Notify)
	vrcW.Host().SetNotifier(u.Notify)
	if vrProc != nil {
		vrProc.Host().SetNotifier(u.Notify)
	}
	if mediaChild != nil {
		mediaChild.Host().SetNotifier(u.Notify)
	}

	// Auto-reconcile finished recordings against Traktor's post-session history the instant
	// Traktor writes it (on close) - no manual step. History dir = config override or newest
	// install; metadata resolver = the library DB.
	if rec != nil {
		histDir := func() string {
			if cfg.Features.NML.HistoryDir != "" {
				return cfg.Features.NML.HistoryDir
			}
			if ins, e := musiclib.DiscoverTraktor(); e == nil && len(ins) > 0 {
				return ins[0].HistoryDir
			}
			return ""
		}
		resolveMeta := func(path string) (recorder.HistoryMeta, bool) {
			if lib == nil {
				return recorder.HistoryMeta{}, false
			}
			t, ok, _ := lib.TrackByPath(path)
			if !ok {
				return recorder.HistoryMeta{}, false
			}
			return recorder.HistoryMeta{Title: t.Title, Artist: t.Artist, Album: t.Album, Key: t.Key, BPM: t.BPM}, true
		}
		ar := recorder.NewAutoReconciler(rec, histDir, resolveMeta, log)
		ar.SetOnChange(u.RefreshRecordings)
		perfmon.RegisterProbe("recorder.reconcile", ar.Stats)
		debuglog.Go(log, "auto-reconcile", func() { ar.Start(ctx) })
	}
	ctl = &appControl{log: log, auth: authMgr, cfg: &cfg, mods: mods, ui: u, vrStats: vrPerf, vrOverlay: vrSurf, perfMon: perfMon, peerMgr: peerMgr, remoteCtl: remoteCtl, rec: rec, lib: lib, syncer: syncer, appGroups: appGroups, dmxR: dmxRouter, vrslStream: vrslStream, mocap: mocapSvc, crew: crewSvc, tc: tcSvc, obsControl: obsControl, media: mediaRouter, mediaCaps: mediaCapsPlan, obs: obsW, ableLink: linkW, guardDisarm: guardDisarm, quit: cancel}
	remotectl.RegisterScreenshot(remoteCtl, ctl)  // peer-driven app-window + VR-View screenshot
	remotectl.RegisterVRDiag(remoteCtl, ctl)      // peer-driven VR input/binding diagnostics
	remotectl.RegisterPerf(remoteCtl, ctl)        // peer-driven perf diagnosis (remote-perf)
	remotectl.RegisterLogs(remoteCtl, ctl)        // peer-driven log tail (remote-logs)
	remotectl.RegisterEncoderScan(remoteCtl, ctl) // peer-driven encoder-utilization scan (remote-encoder-scan)
	remotectl.RegisterPprof(remoteCtl, ctl)       // peer-driven pprof capture (remote-pprof-cpu/-heap, remote-goroutines)
	remotectl.RegisterUpdater(remoteCtl, ctl)     // peer-driven self-update+relaunch (remote VR-PC update)
	ctl.startDJSyncScheduler()
	debuglog.Go(log, "ctl", func() { inst.serve(ctl, log) })
	if deepLink != "" {
		authMgr.HandleDeepLink(deepLink)
	}
	// GPU-fault watchdog: a display-driver TDR or a hung UI window auto-recovers via a clean
	// relaunch instead of leaving the app wedged (the in-daemon GL context can't recover in-proc).
	// Windowed only - needs a window to assess responsiveness, and headless has no GL surface.
	gpuRec := &gpuRecovery{log: log, notify: u.Notify, quit: cancel}
	ctl.gpuRec = gpuRec
	gpuwatch.Start(ctx, gpuwatch.Options{Log: log, OnFault: gpuRec.onFault})
	log.Info("gpuwatch", "GPU-fault watchdog armed (TDR + hung-window auto-recovery)", nil)

	debuglog.Go(log, "app", func() {
		<-ctx.Done()
		u.Stop()
	})
	u.Run(cfg.StartHidden)
	cancel()
	shutdown()
	return nil
}

func traktorLogPath() string {
	p, _ := config.DataPath("traktor-payloads.jsonl")
	return p
}

// syncMIDIDriver mirrors driver-managed controllers into the ravemidi driver. Runs from
// the midi child's config provider (boot + every reconfigure); the webui re-syncs on
// config edits with error surfacing. An empty set is skipped here (never wipe the
// driver's persisted config from a mere restart - clearing is an explicit webui apply).
func syncMIDIDriver(cs []config.MIDIControllerMap, log *logbus.Bus) {
	if !midi.DriverInstalled() {
		return
	}
	var ins []midi.ManagedInput
	for _, c := range cs {
		if !c.Enabled || c.Port == "" || c.ThruPort != midi.DriverSentinel {
			continue
		}
		ins = append(ins, midi.ManagedInput{Name: c.Name, SourceMatch: c.Port, Filter: c.DriverFilter, Distinct: c.ThruDistinctName})
	}
	if len(ins) == 0 {
		return
	}
	if err := midi.SetDriverConfig(midi.ManagedCfgs(ins)); err != nil && log != nil {
		log.Warn("midi", "ravemidi driver sync failed (driver update needed?)",
			map[string]any{"err": err.Error()})
	}
}

// midiControllerInits maps enabled native-learn controllers to the midi child's init wire.
// Disabled controllers are dropped (their ports stay closed); an enabled controller with no
// bindings still opens its port so it can be learned.
func midiControllerInits(cs []config.MIDIControllerMap) []featurehost.MidiControllerInit {
	out := make([]featurehost.MidiControllerInit, 0, len(cs))
	for _, c := range cs {
		if !c.Enabled {
			continue
		}
		bs := make([]featurehost.MidiBindingInit, 0, len(c.Bindings))
		for _, b := range c.Bindings {
			bs = append(bs, featurehost.MidiBindingInit{Control: b.Control, Channel: b.Channel, Status: b.Status, Data1: b.Data1, Invert: b.Invert})
		}
		port, thru := c.Port, c.ThruPort
		if thru == midi.DriverSentinel {
			// Driver-managed: the ravemidi driver taps the hardware itself. Read the
			// reserved per-input port and NEVER open the device - releasing our hold is
			// what lets the driver (re)bind it. No app-side THRU (the driver fans out).
			thru = ""
			if midi.DriverInstalled() {
				port = midi.ReservedPortName(c.Name)
			}
		}
		out = append(out, featurehost.MidiControllerInit{Name: c.Name, Port: port, ThruPort: thru, Bindings: bs})
	}
	return out
}

// linkCaptures persists captured set files and time-links each to the recorder tracklist
// recorded over the same span (recording_id), so history playback can seek to a track's
// offset into the media (track.StartedAt − capture.StartedAt). Two capture feeds: Icecast
// audio (live, start+end events) and finished OBS recordings (end only). A recording that
// finalizes re-links orphaned captures (e.g. capture outlived the set, or app closed
// mid-link). Runs until ctx done; idle-cheap. lib may be nil. Both proxies are subprocess
// hosts - events arrive over IPC; linking stays daemon-side (libdb is single-writer).
func linkCaptures(ctx context.Context, rcv *featurehost.IcecastProxy, obsW *featurehost.ObsProxy, rec *recorder.Recorder, lib *libdb.DB, setFp *setfp.Fingerprinter, syncer *playsync.Syncer, fpEnabled func() bool, log *logbus.Bus) {
	iceCh, iceUnsub := rcv.SubscribeCapture()
	defer iceUnsub()
	obsCh, obsUnsub := obsW.SubscribeRecordings()
	defer obsUnsub()
	recCh, recUnsub := rec.Subscribe()
	defer recUnsub()

	link := func(start, end time.Time) string {
		if end.IsZero() || rec == nil {
			return ""
		}
		if r, ok := rec.FindByWindow(start, end); ok {
			return r.ID
		}
		return ""
	}
	save := func(sr libdb.SetRecording) {
		if err := lib.SaveSetRecording(sr); err != nil {
			log.Warn("setcapture", "persist set recording failed", map[string]any{"error": err.Error()})
		}
	}
	relink := func() {
		orphans, _ := lib.UnlinkedSetRecordings()
		for _, o := range orphans {
			if id := link(o.StartedAt, o.EndedAt); id != "" {
				if err := lib.RelinkSetRecording(o.ID, id); err == nil {
					log.Info("setcapture", "re-linked capture", map[string]any{"capture": o.ID, "recording": id})
				}
			}
		}
	}
	relink() // orphans from a previous run

	active := rec.Active()
	wasActive := active != nil
	lastActiveID := ""
	if active != nil {
		lastActiveID = active.ID
	}
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-iceCh:
			if !ok {
				return
			}
			save(libdb.SetRecording{
				ID: c.ID, Path: c.Path, Format: c.Format, Mount: c.Mount, Kind: libdb.SetKindIcecast,
				StartedAt: c.StartedAt, EndedAt: c.EndedAt, Bytes: c.Bytes,
				RecordingID: link(c.StartedAt, c.EndedAt),
			})
		case o, ok := <-obsCh:
			if !ok {
				return
			}
			save(libdb.SetRecording{
				ID:   fmt.Sprintf("obs_%d", o.StartedAt.UnixNano()),
				Path: o.Path, Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(o.Path)), "."),
				Mount: "obs", Kind: libdb.SetKindOBS,
				StartedAt: o.StartedAt, EndedAt: o.EndedAt, Bytes: o.Bytes,
				RecordingID: link(o.StartedAt, o.EndedAt),
			})
		case r, ok := <-recCh:
			if !ok {
				return
			}
			if r != nil {
				lastActiveID = r.ID
			} else if wasActive && lastActiveID != "" {
				// A recording just finalized: re-link orphan captures, then fingerprint linked
				// audio + sync the set-log to the play layer (off the event loop - slow work).
				id := lastActiveID
				relink()
				// Defer the CPU-heavy fingerprint+identify while an OBS stream is live - it would
				// starve the encoder. The Icecast/OBS capture itself already finished; the recording
				// is kept and processed when the stream ends (governor releases parked work then).
				debuglog.Go(log, "playsync", func() {
					governor.WhenBackgroundAllowed("playsync-fp:"+id, func() {
						syncFinishedSet(ctx, id, rec, lib, setFp, syncer, fpEnabled, log)
					})
				})
			}
			wasActive = r != nil
		}
	}
}

// watchStreaming polls for a live OBS stream and feeds the activity governor (governor.SetStreaming).
// Detection is two-tier, preferring real streaming-state over mere process presence:
//  1. If obs-websocket is connected, GetStreamStatus().Active is authoritative (OBS open but idle
//     → not streaming → nothing suspended).
//  2. Otherwise fall back to OBS process presence (obs64/obs running) - conservative: while OBS is
//     up we assume a stream may be live and defer non-essential heavy work (it's only deferred, not
//     dropped, and resumes when OBS closes).
//
// Cheap + event-driven: a 3s poll, no busy loop. Only flips the governor on real transitions. The
// same sample also drives the auto-live broadcast (drv): OBS stream ⇒ publish now-playing (no manual
// go-live). drv/in may be nil (governor-only).
func watchStreaming(ctx context.Context, obsW *featurehost.ObsProxy, log *logbus.Bus, drv *autoLiveDriver, in autoLiveInputs) {
	defer debuglog.Recover(log, "governor", false)
	act := sysactivity.New()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			live := streamLive(ctx, obsW, act)
			governor.SetStreaming(live)
			if drv != nil {
				// Start spawns the stream child + hits the API; cap it so a stuck call can't wedge
				// the sampler (governor keeps flipping next tick).
				sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				drv.tick(sctx, live, in.signedIn(), in.paused(), in.token(), in.title())
				cancel()
			}
		}
	}
}

// streamLive reports whether a stream is live now (see watchStreaming for the two-tier logic).
func streamLive(ctx context.Context, obsW *featurehost.ObsProxy, act sysactivity.Activity) bool {
	if obsW != nil {
		sctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		st, err := obsW.GetStreamStatus(sctx)
		cancel()
		if err == nil {
			return st.Active // authoritative when obs-websocket is reachable
		}
	}
	// Fallback: OBS process presence.
	set, ok := act.RunningProcesses()
	if !ok {
		return false
	}
	return sysactivity.Running(set, "obs64") || sysactivity.Running(set, "obs")
}

// syncFinishedSet fingerprints a finished recording's linked capture audio (per-track spans,
// offset = track.StartedAt − capture.StartedAt) so identification can match acoustically, then
// drains the recording through the play-layer sync (identify/link/provisional). Both steps are
// nil-safe + auth-gated downstream - no-ops when fingerprinting is off or the user is signed out.
func syncFinishedSet(ctx context.Context, recordingID string, rec *recorder.Recorder, lib *libdb.DB, setFp *setfp.Fingerprinter, syncer *playsync.Syncer, fpEnabled func() bool, log *logbus.Bus) {
	defer debuglog.Recover(log, "playsync", false)
	if recordingID == "" || lib == nil {
		return
	}
	if setFp != nil && fpEnabled != nil && fpEnabled() {
		fingerprintLinkedAudio(ctx, recordingID, rec, lib, setFp, log)
	}
	if syncer != nil {
		if _, err := syncer.DrainRecording(ctx, recordingID); err != nil {
			log.Warn("playsync", "drain recording failed", map[string]any{"recording": recordingID, "error": err.Error()})
		}
	}
}

// fingerprintLinkedAudio fingerprints each Icecast-captured audio file linked to a recording,
// using the recording's played tracks as the per-track spans within the capture.
func fingerprintLinkedAudio(ctx context.Context, recordingID string, rec *recorder.Recorder, lib *libdb.DB, setFp *setfp.Fingerprinter, log *logbus.Bus) {
	caps, err := lib.SetRecordingsFor(recordingID)
	if err != nil || len(caps) == 0 {
		return
	}
	r, ok := rec.Get(recordingID)
	if !ok || len(r.Tracks) == 0 {
		return
	}
	for _, c := range caps {
		if c.Kind != libdb.SetKindIcecast || c.Path == "" || c.StartedAt.IsZero() {
			continue // only audio captures are fingerprintable
		}
		spans := make([]setfp.TrackSpan, 0, len(r.Tracks))
		for _, t := range r.Tracks {
			if t.StartedAt.Before(c.StartedAt) {
				continue
			}
			length := 0.0
			if !t.EndedAt.IsZero() && t.EndedAt.After(t.StartedAt) {
				length = t.EndedAt.Sub(t.StartedAt).Seconds()
			}
			spans = append(spans, setfp.TrackSpan{
				Artist: t.Artist, Title: t.Title,
				OffsetSeconds: t.StartedAt.Sub(c.StartedAt).Seconds(), LengthSeconds: length,
			})
		}
		if len(spans) == 0 {
			continue
		}
		n, err := setFp.FingerprintSet(ctx, c.Path, spans, nil)
		if err != nil {
			log.Warn("playsync", "fingerprint set failed", map[string]any{"capture": c.ID, "error": err.Error()})
			continue
		}
		log.Info("playsync", "set fingerprinted", map[string]any{"capture": c.ID, "recording": recordingID, "tracks": n})
	}
}

// setCaptureConfig builds the Icecast receiver config from the SetCapture feature. Bound to
// loopback - Traktor broadcasts from the same host, so the receiver never faces the LAN.
func setCaptureConfig(cfg config.Config) icecast.Config {
	sc := cfg.Features.SetCapture
	return icecast.Config{
		Addr:           fmt.Sprintf("127.0.0.1:%d", sc.ResolvedPort()),
		Mount:          sc.Mount,
		Username:       sc.ResolvedUsername(),
		Password:       sc.Password,
		SetsDir:        sc.ResolvedSetsDir(),
		SingleFile:     sc.SingleFile,
		ReconnectGrace: sc.ResolvedReconnectGrace(),
		MetadataOnly:   sc.MetadataOnly,
	}
}

// defaultNodeNickname is the LAN-advertised name when the user hasn't set one: the hostname.
func defaultNodeNickname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "rave-mate"
}

// mediaChildMemMB caps the isolated media child's RAM (kill-on-close job) - generous for 4K decode
// buffers but bounded so a route runaway can't starve the host (#44).
const mediaChildMemMB = 2048

// connectedMediaSecrets snapshots every connected peer's media AEAD key, for the media child spawn +
// per-peer refresh (the child dials its own AEAD socket keyed off these).
func connectedMediaSecrets(pm *peerlink.Manager) map[string][]byte {
	out := map[string][]byte{}
	for _, c := range pm.Connections() {
		if c.Status != peerlink.StatusConnected {
			continue
		}
		if sec, ok := pm.MediaSecret(c.NodeID); ok {
			out[c.NodeID] = sec
		}
	}
	return out
}

// peerIsLocalhost reports whether a paired peer's link address resolves to THIS machine
// (loopback or a local interface IP) - the mediaroute same-PC guard (never encode locally, §3).
func peerIsLocalhost(pm *peerlink.Manager, peer string) bool {
	for _, c := range pm.Connections() {
		if c.NodeID != peer || c.Address == "" {
			continue
		}
		host := c.Address
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return false
		}
		if ip.IsLoopback() {
			return true
		}
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return false
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// mediaBus adapts the eventbus to medialink.Bus (media.advert|offer|answer negotiation).
type mediaBus struct{ b *eventbus.Bus }

func (m mediaBus) Publish(topic string, data json.RawMessage) { m.b.Publish(topic, data) }
func (m mediaBus) Subscribe(topic string, fn func(medialink.Event)) func() {
	return m.b.Subscribe(topic, func(ev eventbus.Event) {
		fn(medialink.Event{Origin: ev.Origin, Local: ev.Local, Data: ev.Data})
	})
}

// uiFileXfer adapts *filexfer.Manager to ui.FileXfer: adds Peers() (connected, trusted
// peerlink targets for the Library "Send to a paired instance…" submenu).
type uiFileXfer struct {
	*filexfer.Manager
	peers *peerlink.Manager
}

func (x uiFileXfer) Peers() []ui.PeerInfo {
	if x.peers == nil {
		return nil
	}
	var out []ui.PeerInfo
	for _, c := range x.peers.Connections() {
		if c.Status != peerlink.StatusConnected {
			continue
		}
		name := c.Nickname
		if name == "" {
			if name = c.NodeID; len(name) > 8 {
				name = name[:8]
			}
		}
		out = append(out, ui.PeerInfo{NodeID: c.NodeID, Name: name})
	}
	return out
}

// fileBus adapts the eventbus to filexfer.Bus (file.offer|answer negotiation).
type fileBus struct{ b *eventbus.Bus }

func (f fileBus) Publish(topic string, data json.RawMessage) { f.b.Publish(topic, data) }
func (f fileBus) Subscribe(topic string, fn func(filexfer.Event)) func() {
	return f.b.Subscribe(topic, func(ev eventbus.Event) {
		fn(filexfer.Event{Origin: ev.Origin, Local: ev.Local, Data: ev.Data})
	})
}

// fileSinkDir resolves the now-playing output dir: the configured path, else the app
// config dir (where OBS can read now_playing.{json,txt}).
func fileSinkDir(cfg config.Config) string {
	if d := cfg.Features.NowPlayingFile.Dir; d != "" {
		return d
	}
	d, _ := config.Dir()
	return d
}

// overlayCacheDir is where the overlay cover-art resolver caches normalized thumbnails.
func overlayCacheDir() string {
	d, _ := config.DataPath("overlay-art")
	return d
}

// overlayWaveCacheDir holds the cached per-track waveform peak overviews for the overlays.
func overlayWaveCacheDir() string {
	d, _ := config.DataPath("overlay-waveform")
	return d
}

// libArtStore adapts the library DB as the overlayart persistent store (covers survive restarts
// + each file is probed at most once). Get/Put are best-effort; a DB error degrades to "unknown".
type libArtStore struct{ db *libdb.DB }

func (s libArtStore) Get(path string) ([]byte, string, bool) {
	a, ok, err := s.db.GetTrackArt(path)
	if err != nil || !ok {
		return nil, "", false
	}
	return a.Data, a.MIME, true
}

func (s libArtStore) GetByMeta(artist, title string) ([]byte, bool) {
	return s.db.GetTrackArtByMeta(artist, title)
}

func (s libArtStore) Put(path, artist, title string, data []byte, mime, src string) {
	_ = s.db.PutTrackArt(libdb.TrackArt{Path: path, Artist: artist, Title: title, MIME: mime, Data: data, Source: src})
}

// overlayLayoutPath is where the browser overlay's drag-editor persists deck positions.
func overlayLayoutPath() string {
	d, _ := config.DataPath("overlay-layout.json")
	return d
}

// overlayStylePath is the shared overlay appearance file the browser editor writes and the native
// renderers read (so Spout/PNG honour the same colours/gradients/per-band EQ).
func overlayStylePath() string {
	d, _ := config.DataPath("overlay-style.json")
	return d
}

// dmxGridPNGPath is the VRSL grid's PNG-fallback output file (non-Spout builds / no GPU).
func dmxGridPNGPath() string {
	d, _ := config.DataPath("vrsl-grid.png")
	return d
}

// overlayPngDir is the output folder for the native per-deck PNG cards (deck_A.png …).
func overlayPngDir(cfg config.Config) string {
	if d := cfg.Features.OverlayPNG.Dir; d != "" {
		return d
	}
	d, _ := config.DataPath("overlay-png")
	return d
}

// autoRecord ties recording to the live-stream lifecycle: start on go-live, finalize on
// end (when the recorder feature is enabled). Manual record from the UI is independent and
// idempotent with this.
func autoRecord(ctx context.Context, pub *featurehost.StreamProxy, rec *recorder.Recorder, enabled func() bool) {
	ch, unsub := pub.SubscribeStatus()
	defer unsub()
	wasLive := false
	for {
		select {
		case <-ctx.Done():
			return
		case s, ok := <-ch:
			if !ok {
				return
			}
			switch {
			case s.IsLive && !wasLive && enabled():
				rec.StartRecording(s.Title, s.StreamID)
			case !s.IsLive && wasLive:
				rec.StopRecording()
			}
			wasLive = s.IsLive
		}
	}
}

// appControl implements the IPC Control surface for `rave-mate ctl …`. ui is nil in
// service mode (Show/SelectTab become no-ops; logs + status + quit still work).
// frontend is the renderer seam: the Fyne UI (*ui.UI) or the HTML/CSS webview (*webui.UI) both
// satisfy it structurally. app.go depends only on this surface so the two coexist behind the
// features.ui.renderer flag (and Fyne can be retired once the webview reaches parity).
type frontend interface {
	Run(startHidden bool)
	Stop()
	Show()
	Notify(title, body string)
	RefreshRecordings()
	// studio.Picker (native file dialogs for the web client, windowed mode)
	PickDirectory(ctx context.Context) (string, error)
	PickFile(ctx context.Context) (string, error)
	ChooseSavePath(ctx context.Context, defaultPath, container string) (string, error)
	// ctl control plane (verify-rave-mate-ui)
	SelectTab(name string) (bool, []string)
	Snapshot() string
	Resize(w, h float32)
	Click(query string) bool
	Tap(x, y float32) bool
	TapSecondary(x, y float32) bool
	Type(text string) bool
	Read(query string) (string, bool)
	Set(query, value string) bool
	Screenshot(path string) error
	ScreenshotAll(dir string) (string, error)
	ScreenshotRegion(path string, x, y, w, h float32) error
}

type appControl struct {
	log         *logbus.Bus
	auth        *auth.Manager
	cfg         *config.Config
	mods        *module.Manager
	ui          frontend
	rec         *recorder.Recorder            // local recordings (recorded-set upload); may be nil
	lib         *libdb.DB                     // sync ledgers (upload status); may be nil
	syncer      *playsync.Syncer              // play-layer backend sync; may be nil
	vrStats     *vrstats.Collector            // VR perf telemetry snapshot (ctl vrperf); may be nil
	vrOverlay   vroverlay.Surface             // VR overlay control plane (ctl vrinput binding diagnostics); may be nil
	perfMon     *perfmon.Monitor              // always-on perf collector (ctl perf / peer app.perf); may be nil
	peerMgr     *peerlink.Manager             // paired peers (ctl remote-screenshot / list-peers); may be nil
	remoteCtl   *remotectl.Endpoint           // peer RPC endpoint (drive a paired peer); may be nil
	appGroups   *appgroups.Service            // application-group launcher (ctl launch-group); may be nil
	obsControl  *obscontrol.Manager           // cross-instance OBS control + media-sync status (ctl obs-sync-status); may be nil
	dmxR        *dmx.Router                   // DMX plane (ctl dmx-status); may be nil
	vrslStream  *vrslstream.Streamer          // VRSL DMX-over-video stream (ctl stream-status); may be nil
	mocap       *mocap.Service                // mocap capture master (ctl mocap-status); may be nil
	crew        *crewlink.Service             // capture-crew relay (ctl crew-status); may be nil
	tc          *timecode.Service             // house timecode outputs (ctl tc-status/tc-start/tc-stop); may be nil
	gpuRec      *gpuRecovery                  // GPU-fault recovery (ctl gpu-selftest); nil in service mode
	media       *medialink.RouteManager       // media plane (ctl encoder-scan: probed encoders); may be nil
	mediaCaps   *mediaCaps                    // encode-capability advertisement planner (ctl encoder-scan); may be nil
	obs         *featurehost.ObsProxy         // OBS bridge (ctl encoder-scan: live stream/record active); may be nil
	ableLink    *featurehost.AbletonLinkProxy // Ableton Link bridge (ctl ablelink-status/resync); may be nil
	guardDisarm func()                        // guardian disarm for the hard-exit backstop (defers skip on os.Exit); may be nil
	quit        context.CancelFunc
	updating    atomic.Bool    // guards a self-update in flight (ctl SELF-UPDATE is fire-and-forget)
	libSync     libSyncState   // in-flight/last library metadata sync (ctl SYNC-LIBRARY is async)
	mediaSync   mediaSyncState // in-flight/last media sync (ctl SYNC-MEDIA is async)
	plSync      plSyncState    // in-flight/last playlist sync (ctl SYNC-PLAYLISTS is async)
	djSync      djSyncState    // in-flight/last cross-DJ-software sync (ctl SYNC-DJ is async)
	djSched     djSyncSched    // auto-sync scheduler (reuses the automation Scheduler)
}

// libSyncState is the SYNC-LIBRARY run guard + last-result snapshot for LIBRARY-SYNC-STATUS.
type libSyncState struct {
	mu         sync.Mutex
	running    bool
	startedAt  time.Time
	finishedAt time.Time
	last       *playsync.LibraryResult
	err        string
}

// mediaSyncState is the SYNC-MEDIA run guard + live-progress snapshot for MEDIA-SYNC-STATUS.
type mediaSyncState struct {
	mu         sync.Mutex
	running    bool
	startedAt  time.Time
	finishedAt time.Time
	last       *playsync.MediaResult // live progress while running, final result after
	err        string
}

// plSyncState is the SYNC-PLAYLISTS run guard + last-result snapshot for PLAYLIST-SYNC-STATUS.
type plSyncState struct {
	mu         sync.Mutex
	running    bool
	startedAt  time.Time
	finishedAt time.Time
	last       *playsync.PlaylistSyncResult
	err        string
}

func (c *appControl) Show() {
	if c.ui != nil {
		c.ui.Show()
	}
}

func (c *appControl) Quit() {
	// Hard-exit backstop: a wedged Fyne main loop (hidden-to-tray WaitForEvents stall) swallows
	// the queued app.Quit and holds the single-instance lock forever - seen live on a paired VR
	// instance where every self-update applied on disk but the old process never exited. Force
	// the exit after a grace period; disarm the guardian first so an INTENDED quit stays quit.
	go func() {
		time.Sleep(15 * time.Second)
		if c.log != nil {
			c.log.Warn("app", "graceful quit timed out - forcing exit", nil)
		}
		if c.guardDisarm != nil {
			c.guardDisarm()
		}
		os.Exit(0)
	}()
	if c.ui != nil {
		c.ui.Stop()
	} else {
		c.quit()
	}
}

// GPUSelfTest injects a synthetic hung-window fault to prove GPU auto-recovery end-to-end: the
// app relaunches a fresh instance and exits, no real driver crash needed. Fires after a short
// delay so this reply flushes first.
func (c *appControl) GPUSelfTest() string {
	if c.gpuRec == nil {
		return "unavailable (service mode / no UI)"
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		c.gpuRec.onFault(gpuwatch.Fault{Kind: gpuwatch.FaultHungWindow, Detail: "self-test (synthetic)"})
	}()
	return "ok - simulating GPU hang; rave-mate will relaunch itself now"
}

func (c *appControl) SelectTab(name string) (bool, []string) {
	if c.ui == nil {
		return false, nil
	}
	return c.ui.SelectTab(name)
}

func (c *appControl) Snapshot() string {
	if c.ui == nil {
		return "(no UI - running in service mode)\n"
	}
	return c.ui.Snapshot()
}

func (c *appControl) Resize(w, h float32) bool {
	if c.ui == nil || w < 1 || h < 1 {
		return false
	}
	c.ui.Resize(w, h)
	return true
}

func (c *appControl) Click(query string) bool {
	if c.ui == nil || query == "" {
		return false
	}
	return c.ui.Click(query)
}

// Act posts a raw UI action through the page act pipeline - a webview-renderer
// capability (interface assertion; Fyne/service mode = unsupported).
func (c *appControl) Act(act, val string) bool {
	type acter interface{ Act(act, val string) bool }
	if a, ok := c.ui.(acter); ok {
		return a.Act(act, val)
	}
	return false
}

func (c *appControl) Tap(x, y float32) bool {
	if c.ui == nil {
		return false
	}
	return c.ui.Tap(x, y)
}

func (c *appControl) TapSecondary(x, y float32) bool {
	if c.ui == nil {
		return false
	}
	return c.ui.TapSecondary(x, y)
}

func (c *appControl) Type(text string) bool {
	if c.ui == nil {
		return false
	}
	return c.ui.Type(text)
}

func (c *appControl) Read(query string) (string, bool) {
	if c.ui == nil {
		return "", false
	}
	return c.ui.Read(query)
}

func (c *appControl) Set(query, value string) bool {
	if c.ui == nil {
		return false
	}
	return c.ui.Set(query, value)
}

// Scroll scrolls the main content pane (ctl SCROLL). Webview renderer only - resolved by
// type assertion so the frontend seam (and the Fyne renderer) stays untouched.
func (c *appControl) Scroll(y float32) bool {
	s, ok := c.ui.(interface{ Scroll(y float32) bool })
	return ok && s.Scroll(y)
}

func (c *appControl) Screenshot(path string) bool {
	if c.ui == nil || path == "" {
		return false
	}
	return c.ui.Screenshot(path) == nil
}

// ScreenshotAll sweeps every tab (+scroll positions) to PNGs + report.txt in dir
// (ctl SCREENSHOT-ALL) - the visual-verification pass over the whole UI.
func (c *appControl) ScreenshotAll(dir string) string {
	if c.ui == nil {
		return "error: no UI (service mode)"
	}
	report, err := c.ui.ScreenshotAll(dir)
	if err != nil {
		return "error: " + err.Error()
	}
	// First line = totals; per-shot detail + overflow findings live in report.txt.
	head := strings.SplitN(report, "\n", 2)[0]
	return "ok " + head + " report=" + filepath.Join(dir, "report.txt")
}

func (c *appControl) ScreenshotRegion(path string, x, y, w, h float32) bool {
	if c.ui == nil || path == "" {
		return false
	}
	return c.ui.ScreenshotRegion(path, x, y, w, h) == nil
}

// ScreenshotVR captures the SteamVR VR-View mirror window to a PNG (ctl SCREENSHOT-VR). Gated by
// the VROverlay.VRViewCapture opt-in; Windows-only OS window capture (no OpenVR). false if the
// opt-in is off, no VR View window exists, or the platform is unsupported. Works in service mode
// too (it needs no Fyne window).
func (c *appControl) ScreenshotVR(path string) bool {
	if path == "" || c.cfg == nil || !c.cfg.Features.VROverlay.VRViewCapture {
		return false
	}
	return winshot.CaptureVRView(path) == nil
}

// GioSnapshot (ctl GIO-SNAPSHOT): labeled-control tree of one Gio window; "" lists open
// windows. Backed by the package-level giokit window registry - no Fyne UI needed.
func (c *appControl) GioSnapshot(windowID string) string {
	if windowID == "" {
		ids := giokit.WindowIDs()
		if len(ids) == 0 {
			return "(no gio windows open)\n"
		}
		return strings.Join(ids, "\n") + "\n"
	}
	if s, ok := giokit.SnapshotText(windowID); ok {
		return s
	}
	return "unknown gio window " + windowID + " - open:\n" + c.GioSnapshot("")
}

// GioTap (ctl GIO-TAP): queue controlID's activation in Gio window windowID.
func (c *appControl) GioTap(windowID, controlID string) string {
	if err := giokit.TapWindow(windowID, controlID); err != nil {
		return err.Error()
	}
	return "ok"
}

func (c *appControl) HandleDeepLink(url string) { c.auth.HandleDeepLink(url) }

// SelfUpdate runs the updater (ctl SELF-UPDATE - a co-located rave-app that just updated asks
// us to update too). Guarded + async (the ctl call is fire-and-forget). Sibling-triggered, so
// it does NOT notify rave-app back (single hop, no ping-pong). Returns a terse status.
func (c *appControl) SelfUpdate() string {
	if !c.updating.CompareAndSwap(false, true) {
		return "already updating"
	}
	u := selfupdate.New(version.FeedURL, version.BuildNum(), version.UpdatePubKey)
	if !u.Enabled() {
		c.updating.Store(false)
		return "disabled"
	}
	debuglog.Go(c.log, "update", func() {
		defer c.updating.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		rel, avail, err := u.Available(ctx)
		if err != nil {
			c.log.Warn("update", "check failed", map[string]any{"error": err.Error()})
			return
		}
		if !avail {
			return
		}
		if err := u.Apply(ctx, rel, nil); err != nil {
			c.log.Warn("update", "apply failed", map[string]any{"error": err.Error()})
			return
		}
		if err := selfupdate.Relaunch(); err != nil {
			c.log.Warn("update", "relaunch failed", map[string]any{"error": err.Error()})
		}
		c.Quit()
	})
	return "updating"
}

// AdoptToken adopts a co-located sibling app's token bundle (the rave-app handoff): the arg is
// the SealHandoff blob (AES-256-GCM over JSON {access,refresh,apiBase}, keyed by ctl.secret -
// so only a sender that could read our owner-only secret is honoured). Adopts only when signed
// out and the api base matches ours (no dev/prod mixing); an explicit rave-mate login is never
// clobbered. Returns a terse status.
func (c *appControl) AdoptToken(blob string) string {
	if c.auth == nil {
		return "unavailable"
	}
	secret, err := loadHandoffSecret()
	if err != nil {
		return "no secret"
	}
	raw, err := auth.OpenHandoff(secret, blob)
	if err != nil {
		return "bad payload"
	}
	var p struct {
		Access  string `json:"access"`
		Refresh string `json:"refresh"`
		APIBase string `json:"apiBase"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Access == "" {
		return "bad payload"
	}
	if strings.TrimRight(p.APIBase, "/") != strings.TrimRight(c.cfg.APIBaseURL, "/") {
		return "api mismatch"
	}
	if c.auth.SignedIn() {
		return "already signed in"
	}
	c.auth.SetTokens(p.Access, p.Refresh)
	return "ok"
}

// recordedSetFrom maps a local recorder Recording to a play-layer RecordedSet.
func recordedSetFrom(r recorder.Recording) playsync.RecordedSet {
	set := playsync.RecordedSet{RecordingID: r.ID, Title: r.Name, StartedAt: r.StartedAt, EndedAt: r.EndedAt}
	for _, t := range r.Tracks {
		set.Tracks = append(set.Tracks, playsync.Track{
			Artist: t.Artist, Title: t.Title, Album: t.Album, Key: t.Key, BPM: t.BPM,
			Deck: t.Deck, StartedAt: t.StartedAt, EndedAt: t.EndedAt,
		})
	}
	return set
}

// UploadRecordedSet publishes a local recording to the play layer as a backend stream (Gap 2 -
// the rave-app Captured-Sets "publish" bridge). Synchronous (a few API calls); the caller uses a
// generous read deadline. Returns "ok <streamId>" or a terse error token.
func (c *appControl) UploadRecordedSet(recordingID string) string {
	if c.syncer == nil || c.rec == nil {
		return "unavailable"
	}
	if !c.auth.SignedIn() {
		return "unauthenticated"
	}
	r, ok := c.rec.Get(recordingID)
	if !ok {
		return "not found"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	streamID, err := c.syncer.UploadRecordedSet(ctx, recordedSetFrom(r))
	if err != nil {
		return "error: " + err.Error()
	}
	return "ok " + streamID
}

// SyncRecordedSet identifies a local recording's played tracks against the catalog (Gap 1) on
// demand. Returns "ok linked=N provisional=M cached=K" or a terse error token.
func (c *appControl) SyncRecordedSet(recordingID string) string {
	if c.syncer == nil {
		return "unavailable"
	}
	if !c.auth.SignedIn() {
		return "unauthenticated"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := c.syncer.DrainRecording(ctx, recordingID)
	if err != nil {
		return "error: " + err.Error()
	}
	return fmt.Sprintf("ok linked=%d provisional=%d cached=%d failed=%d", res.Linked, res.Provisional, res.Cached, res.Failed)
}

// ListRecordedSets returns the local recordings as a compact JSON array with each set's upload
// status (drives the rave-app Captured-Sets surface). One line (no newlines) so it round-trips
// the single-line ctl reply protocol.
func (c *appControl) ListRecordedSets() string {
	if c.rec == nil {
		return "[]"
	}
	type item struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		StartedAt  string `json:"startedAt"`
		EndedAt    string `json:"endedAt,omitempty"`
		TrackCount int    `json:"trackCount"`
		Uploaded   bool   `json:"uploaded"`
		StreamID   string `json:"streamId,omitempty"`
	}
	recs := c.rec.List()
	out := make([]item, 0, len(recs))
	for _, r := range recs {
		it := item{ID: r.ID, Name: r.Name, TrackCount: len(r.Tracks), StartedAt: r.StartedAt.UTC().Format(time.RFC3339)}
		if !r.EndedAt.IsZero() {
			it.EndedAt = r.EndedAt.UTC().Format(time.RFC3339)
		}
		if c.lib != nil {
			if up, ok, _ := c.lib.GetSetUpload(r.ID); ok {
				it.Uploaded = true
				it.StreamID = up.StreamID
			}
		}
		out = append(out, it)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// SyncLibrary starts the full local-library metadata upload (ctl SYNC-LIBRARY). Async - a
// first run over ~23k tracks takes minutes; progress streams to the logbus. One run at a time.
func (c *appControl) SyncLibrary() string {
	if c.syncer == nil {
		return "unavailable"
	}
	if c.auth == nil || !c.auth.SignedIn() {
		return "unauthenticated"
	}
	c.libSync.mu.Lock()
	if c.libSync.running {
		c.libSync.mu.Unlock()
		return "already running"
	}
	c.libSync.running, c.libSync.startedAt, c.libSync.finishedAt = true, time.Now(), time.Time{}
	c.libSync.last, c.libSync.err = nil, ""
	c.libSync.mu.Unlock()
	debuglog.Go(c.log, "library-sync", func() {
		var res playsync.LibraryResult
		var err error
		defer func() { // always clears running, even on panic
			c.libSync.mu.Lock()
			defer c.libSync.mu.Unlock()
			c.libSync.running, c.libSync.finishedAt, c.libSync.last = false, time.Now(), &res
			if err != nil {
				c.libSync.err = err.Error()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		res, err = c.syncer.SyncLibrary(ctx)
	})
	return "started"
}

// LibrarySyncStatus returns the library sync state as one-line JSON (ctl LIBRARY-SYNC-STATUS).
func (c *appControl) LibrarySyncStatus() string {
	c.libSync.mu.Lock()
	defer c.libSync.mu.Unlock()
	out := map[string]any{"running": c.libSync.running}
	if !c.libSync.startedAt.IsZero() {
		out["started_at"] = c.libSync.startedAt.UTC().Format(time.RFC3339)
	}
	if !c.libSync.finishedAt.IsZero() {
		out["finished_at"] = c.libSync.finishedAt.UTC().Format(time.RFC3339)
	}
	if r := c.libSync.last; r != nil {
		out["total"], out["uploaded"], out["skipped"], out["linked"], out["failed"] =
			r.Total, r.Uploaded, r.Skipped, r.Linked, r.Failed
	}
	if c.libSync.err != "" {
		out["error"] = c.libSync.err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"running":false}`
	}
	return string(b)
}

// SyncMedia starts the waveform/artwork media upload (ctl SYNC-MEDIA). Async; budget caps
// ffmpeg waveform probes per run (<=0 = default 500). One run at a time.
func (c *appControl) SyncMedia(budget int) string {
	if c.syncer == nil {
		return "unavailable"
	}
	if c.auth == nil || !c.auth.SignedIn() {
		return "unauthenticated"
	}
	c.mediaSync.mu.Lock()
	if c.mediaSync.running {
		c.mediaSync.mu.Unlock()
		return "already running"
	}
	c.mediaSync.running, c.mediaSync.startedAt, c.mediaSync.finishedAt = true, time.Now(), time.Time{}
	c.mediaSync.last, c.mediaSync.err = nil, ""
	c.mediaSync.mu.Unlock()
	debuglog.Go(c.log, "media-sync", func() {
		var res playsync.MediaResult
		var err error
		defer func() { // always clears running, even on panic
			c.mediaSync.mu.Lock()
			defer c.mediaSync.mu.Unlock()
			c.mediaSync.running, c.mediaSync.finishedAt, c.mediaSync.last = false, time.Now(), &res
			if err != nil {
				c.mediaSync.err = err.Error()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		res, err = c.syncer.SyncMedia(ctx, budget, func(p playsync.MediaResult) {
			c.mediaSync.mu.Lock()
			snap := p
			c.mediaSync.last = &snap
			c.mediaSync.mu.Unlock()
		})
	})
	return "started"
}

// SyncPlaylists starts the bulk playlist sync (ctl SYNC-PLAYLISTS): pushes every local-ahead
// pair, pulls every remote-ahead pair, skips diverged. Async; one run at a time.
func (c *appControl) SyncPlaylists() string {
	if c.syncer == nil || c.lib == nil {
		return "unavailable"
	}
	if c.auth == nil || !c.auth.SignedIn() {
		return "unauthenticated"
	}
	c.plSync.mu.Lock()
	if c.plSync.running {
		c.plSync.mu.Unlock()
		return "already running"
	}
	c.plSync.running, c.plSync.startedAt, c.plSync.finishedAt = true, time.Now(), time.Time{}
	c.plSync.last, c.plSync.err = nil, ""
	c.plSync.mu.Unlock()
	debuglog.Go(c.log, "playlist-sync", func() {
		var res playsync.PlaylistSyncResult
		var err error
		defer func() { // always clears running, even on panic
			c.plSync.mu.Lock()
			defer c.plSync.mu.Unlock()
			c.plSync.running, c.plSync.finishedAt, c.plSync.last = false, time.Now(), &res
			if err != nil {
				c.plSync.err = err.Error()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		res, err = c.syncer.SyncAllPlaylists(ctx)
	})
	return "started"
}

// PlaylistSyncStatus returns the playlist sync state as one-line JSON (ctl
// PLAYLIST-SYNC-STATUS): running flag + the last run's counts and per-playlist statuses.
func (c *appControl) PlaylistSyncStatus() string {
	c.plSync.mu.Lock()
	defer c.plSync.mu.Unlock()
	out := map[string]any{"running": c.plSync.running}
	if !c.plSync.startedAt.IsZero() {
		out["started_at"] = c.plSync.startedAt.UTC().Format(time.RFC3339)
	}
	if !c.plSync.finishedAt.IsZero() {
		out["finished_at"] = c.plSync.finishedAt.UTC().Format(time.RFC3339)
	}
	if r := c.plSync.last; r != nil {
		out["total"], out["pushed"], out["pulled"], out["in_sync"] = r.Total, r.Pushed, r.Pulled, r.InSync
		out["diverged"], out["local_only"], out["remote_only"], out["failed"] = r.Diverged, r.LocalOnly, r.RemoteOnly, r.Failed
		out["playlists"] = r.Playlists
	}
	if c.plSync.err != "" {
		out["error"] = c.plSync.err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"running":false}`
	}
	return string(b)
}

// MediaSyncStatus returns the media sync state as one-line JSON (ctl MEDIA-SYNC-STATUS).
func (c *appControl) MediaSyncStatus() string {
	c.mediaSync.mu.Lock()
	defer c.mediaSync.mu.Unlock()
	out := map[string]any{"running": c.mediaSync.running}
	if !c.mediaSync.startedAt.IsZero() {
		out["started_at"] = c.mediaSync.startedAt.UTC().Format(time.RFC3339)
	}
	if !c.mediaSync.finishedAt.IsZero() {
		out["finished_at"] = c.mediaSync.finishedAt.UTC().Format(time.RFC3339)
	}
	if r := c.mediaSync.last; r != nil {
		out["total_candidates"], out["waveforms_uploaded"], out["artwork_uploaded"] = r.Candidates, r.Waveforms, r.Artwork
		out["skipped"], out["failed"], out["remaining"] = r.Skipped, r.Failed, r.Remaining
		if r.Unmatched > 0 {
			out["unmatched"] = r.Unmatched
		}
	}
	if c.mediaSync.err != "" {
		out["error"] = c.mediaSync.err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"running":false}`
	}
	return string(b)
}

func (c *appControl) Status() string {
	f := c.cfg.Features
	var enabled []string
	for _, e := range []struct {
		name string
		on   bool
	}{
		{"traktor", f.Traktor.Enabled}, {"stream", f.StreamBridge.Enabled},
		{"transcode", f.Transcode.Enabled}, {"studio", f.StudioChannel.Enabled},
		{"obs", f.OBS.Enabled}, {"library", f.Library.Enabled},
		{"editor", f.MediaEditor.Enabled}, {"fingerprint", f.Fingerprint.Enabled},
		{"vrchat", f.VRChat.Enabled}, {"vr", f.VR.Enabled}, {"notify", f.Notifications.Enabled},
	} {
		if e.on {
			enabled = append(enabled, e.name)
		}
	}
	mode := "windowed"
	if c.ui == nil {
		mode = "service"
	}
	return fmt.Sprintf("rave-mate %s | api=%s | signed_in=%v | features=[%s] | running=[traktor=%v studio=%v]",
		mode, c.cfg.APIBaseURL, c.auth != nil && c.auth.SignedIn(), strings.Join(enabled, ","),
		c.mods.IsRunning("traktor"), c.mods.IsRunning("studio"))
}

// LaunchAppGroup launches every not-running app in the group (crash recovery). Returns a status
// line: "ok <n> started, <m> already running" or an error token.
func (c *appControl) LaunchAppGroup(id string) string {
	if c.appGroups == nil {
		return "app groups unavailable"
	}
	started, skipped, err := c.appGroups.LaunchGroup(id)
	if err != nil {
		return "error: " + err.Error()
	}
	return fmt.Sprintf("ok %d started, %d already running", len(started), len(skipped))
}

// TCStatus returns the one-line timecode report: running, rate, current TC, sinks on/off.
func (c *appControl) TCStatus() string {
	if c.tc == nil {
		return "timecode unavailable"
	}
	return c.tc.StatusLine()
}

// TCStart starts the house timecode clock (all enabled sinks). Feature must be enabled.
func (c *appControl) TCStart() string {
	if c.tc == nil {
		return "timecode unavailable"
	}
	if !c.cfg.Features.Timecode.Enabled {
		return "timecode feature disabled - enable it in Settings first"
	}
	if err := c.tc.StartClock(); err != nil {
		return "error: " + err.Error()
	}
	return "ok " + c.tc.StatusLine()
}

// TCStop stops the house timecode clock.
func (c *appControl) TCStop() string {
	if c.tc == nil {
		return "timecode unavailable"
	}
	c.tc.StopClock()
	return "ok " + c.tc.StatusLine()
}

// VRPerf returns the VR perf telemetry snapshot as JSON (local + any peers' frame timing / drops).
func (c *appControl) VRPerf() string {
	if c.vrStats == nil {
		return "[]"
	}
	return c.vrStats.JSON()
}

// DMXStatus reports the DMX plane: universes seen (pps, source IP), grid sink state.
func (c *appControl) DMXStatus() string {
	if c.dmxR == nil {
		return "dmx plane unavailable"
	}
	return c.dmxR.StatusText()
}

// StreamStatus reports the VRSL DMX-over-video stream: push target, encoder state, restarts.
func (c *appControl) StreamStatus() string {
	if c.vrslStream == nil {
		return "vrsl stream unavailable"
	}
	return c.vrslStream.StatusText()
}

// MocapStatus reports the mocap capture master: source, packets, active dancers.
func (c *appControl) MocapStatus() string {
	if c.mocap == nil {
		return "mocap unavailable"
	}
	return c.mocap.StatusText()
}

// CrewStatus reports the capture-crew relay link: role, session, frames, drops.
func (c *appControl) CrewStatus() string {
	if c.crew == nil {
		return "crew unavailable"
	}
	return c.crew.StatusText()
}

// OBSSyncStatus reports the media-sync tier state (house clock + per-source chase status) as JSON.
func (c *appControl) OBSSyncStatus() string {
	if c.obsControl == nil {
		return `{"running":false,"sources":[]}`
	}
	payload := struct {
		Running bool               `json:"running"`
		Sources []mediasync.Status `json:"sources"`
	}{Running: c.obsControl.SyncRunning(), Sources: c.obsControl.SyncStatuses()}
	b, err := json.Marshal(payload)
	if err != nil {
		return `{"running":false,"sources":[]}`
	}
	return string(b)
}

// AbleLinkStatus reports the Ableton Link session state (tempo/beat/phase/peers) as JSON.
func (c *appControl) AbleLinkStatus() string {
	if c.ableLink == nil {
		return `{"available":false,"enabled":false}`
	}
	b, err := json.Marshal(c.ableLink.State())
	if err != nil {
		return `{"available":false,"enabled":false}`
	}
	return string(b)
}

// AbleLinkResync hard-realigns the Link phrase (maps beat 0 to now).
func (c *appControl) AbleLinkResync() string {
	if c.ableLink == nil {
		return "ableton link unavailable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.ableLink.Resync(ctx); err != nil {
		return "resync failed: " + err.Error()
	}
	return "resync ok"
}

// ListPeers reports connected paired peers (one per line: nodeID, nickname, status, trusted).
func (c *appControl) ListPeers() string {
	if c.peerMgr == nil {
		return "peer link unavailable"
	}
	conns := c.peerMgr.Connections()
	if len(conns) == 0 {
		return "no peers connected"
	}
	var b strings.Builder
	for _, p := range conns {
		nick := p.Nickname
		if nick == "" {
			nick = "(unnamed)"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\ttrusted=%v\n", p.NodeID, nick, p.Status, p.Trusted)
	}
	return strings.TrimRight(b.String(), "\n")
}

// RemoteScreenshot asks a paired peer to capture its app window (or VR-View when vr) and writes the
// returned PNG to path locally. nodeID "" picks the first connected peer. Lets a desk instance see a
// headset PC's screen/VR view over the peer link.
func (c *appControl) RemoteScreenshot(nodeID, path string, vr bool) string {
	if c.peerMgr == nil || c.remoteCtl == nil {
		return "error: peer link unavailable"
	}
	if path == "" {
		return "usage: remote-screenshot[-vr] <path> [nodeID]"
	}
	if nodeID == "" {
		for _, p := range c.peerMgr.Connections() {
			if p.Status == peerlink.StatusConnected {
				nodeID = p.NodeID
				break
			}
		}
	}
	if nodeID == "" {
		return "error: no connected peer (run `ctl list-peers`)"
	}
	client := remotectl.NewClient(c.remoteCtl, nodeID)
	if client == nil {
		return "error: invalid peer"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var png []byte
	var err error
	if vr {
		png, err = client.ScreenshotVR(ctx)
	} else {
		png, err = client.ScreenshotApp(ctx)
	}
	if err != nil {
		return "error: " + err.Error()
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return "error: write: " + err.Error()
	}
	return fmt.Sprintf("ok %s (%d bytes from %s)", path, len(png), nodeID)
}

// VRInputDiag dumps this instance's SteamVR Input action state (manifest loaded? per-action live
// state + bound physical inputs). For debugging why a binding "does nothing".
func (c *appControl) VRInputDiag() string {
	if c.vrOverlay == nil {
		return "VR overlay unavailable"
	}
	return c.vrOverlay.InputDiag()
}

// RemoteVRInputDiag asks a paired peer for its VR input diagnostic (nodeID "" = first connected).
// Lets a desk instance read a headset PC's SteamVR binding state over the peer link.
func (c *appControl) RemoteVRInputDiag(nodeID string) string {
	if c.peerMgr == nil || c.remoteCtl == nil {
		return "error: peer link unavailable"
	}
	if nodeID == "" {
		for _, p := range c.peerMgr.Connections() {
			if p.Status == peerlink.StatusConnected {
				nodeID = p.NodeID
				break
			}
		}
	}
	if nodeID == "" {
		return "error: no connected peer (run `ctl list-peers`)"
	}
	client := remotectl.NewClient(c.remoteCtl, nodeID)
	if client == nil {
		return "error: invalid peer"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	text, err := client.VRInputDiag(ctx)
	if err != nil {
		return "error: " + err.Error()
	}
	return text
}

// EncoderScan reports which video encoders are in use (OBS config + Parsec + live GPU util) and
// previews the device the affinity planner would pick for a medialink source - read-only diagnosis
// (ctl encoder-scan). Never touches the GPU or starts an encode.
func (c *appControl) EncoderScan() string {
	rep := encoderscan.Detect(c.obsEncoderFunc())

	var cpu float64
	if c.perfMon != nil {
		if ss := c.perfMon.Snapshot(); len(ss) > 0 {
			cpu = ss[len(ss)-1].CPUPct
		}
	}
	// Candidate devices come from what we ADVERTISE (the planner's own input), not from the in-proc
	// router: with the media plane isolated in its child, that router holds no caps at all. Each
	// encoder binds to its real adapter by vendor name (Devices), so LoadPct/VRAMFree are that
	// device's live numbers; sessions stay unknown (vendor-SDK only).
	var advertised []string
	var live encoderscan.AdvertisePlan
	if c.mediaCaps != nil {
		advertised, _, live = c.mediaCaps.snapshot()
	} else if c.media != nil {
		advertised = c.media.Encoders()
	}
	devices := encoderscan.Devices(advertised, rep, cpu)
	policy := encoderscan.ExhaustReduceQuality // TODO: drive from config once the exhaustion setting lands
	plan := live.Plan
	if plan.Action == "" {
		plan = encoderscan.PlanEncode(devices, encoderscan.DefaultEncodeCost(), encoderscan.DefaultCeilings(), policy)
	}

	var b strings.Builder
	b.WriteString(rep.String())
	fmt.Fprintf(&b, "cpu=%.0f%%  advertised-encoders=%d  policy=%s\n", cpu, len(devices), policy)
	if len(advertised) > 0 {
		fmt.Fprintf(&b, "advertised order: [%s]\n", strings.Join(advertised, " "))
	}
	if len(live.Withheld) > 0 {
		fmt.Fprintf(&b, "withheld from peers: [%s]  (critical consumer holds that silicon)\n", strings.Join(live.Withheld, " "))
	}
	for _, d := range devices {
		vram, sess := "vram=?", "sessions=?"
		if d.VRAMFree >= 0 {
			vram = fmt.Sprintf("vram-free=%.0fMB", d.VRAMFree)
		}
		if d.Sessions >= 0 {
			sess = fmt.Sprintf("sessions-free=%d", d.Sessions)
		}
		dev := d.Key
		if dev == "" {
			dev = "device=?"
			if d.IsCPU {
				dev = "cpu"
			}
		}
		fmt.Fprintf(&b, "  cand %-16s fam=%-7s %-24s load=%.0f%% %s %s\n", d.Encoder, d.Family, dev, d.LoadPct, vram, sess)
	}
	for _, n := range live.Notes {
		b.WriteString("note: " + n + "\n")
	}
	// Encoder diagnostics (why the plan has what it has): working set + in-build-but-not-working
	// (HW encoder present in the ffmpeg build yet failing to encode = driver/contention, vs absent
	// = a software-only ffmpeg build). Read from the cached probe - never re-probes / never blocks.
	if caps, ok := mediapipe.Cached(); ok {
		working := map[string]bool{}
		hw := false
		for _, e := range caps.Encoders {
			working[e] = true
			switch encoderscan.FamilyFromOBSID(e) {
			case encoderscan.FamilyNVENC, encoderscan.FamilyAMF, encoderscan.FamilyQSV:
				hw = true
			}
		}
		fmt.Fprintf(&b, "encoders working: [%s]\n", strings.Join(caps.Encoders, " "))
		var failed []string
		for _, e := range caps.InBuild {
			if !working[e] {
				failed = append(failed, e)
			}
		}
		if len(failed) > 0 {
			fmt.Fprintf(&b, "encoders in-build but NOT working: [%s]  (present in ffmpeg build, test-encode failed)\n", strings.Join(failed, " "))
			for _, e := range failed {
				if msg := caps.Errors[e]; msg != "" {
					fmt.Fprintf(&b, "  %s: %s\n", e, msg)
				}
			}
		}
		if !hw {
			b.WriteString("warn: NO working hardware encoder - software-only would peg CPU and starve other encoders; check ffmpeg build has nvenc/amf/qsv\n")
		}
		if len(caps.HWAccels) > 0 {
			fmt.Fprintf(&b, "hwaccels: [%s]\n", strings.Join(caps.HWAccels, " "))
		}
	} else {
		b.WriteString("note: no VALIDATED codec probe yet - test encodes are deferred while a stream is live (they take a real encode session); the advertised set above is the ffmpeg build listing\n")
	}
	fmt.Fprintf(&b, "plan: %s family=%s encoder=%s device=%s scale=%d%%  (%s)\n",
		plan.Action, plan.Family, plan.Encoder, plan.Device, plan.ScalePct, plan.Reason)
	b.WriteString("note: app-agnostic headroom planner; adapter binding is a vendor-name join (exact per-encoder LUID pinning pending), encode-session counts need a vendor SDK and stay unknown\n")
	return b.String()
}

// obsEncoderFunc builds the scan's OBS input: encoder id from OBS config + the live
// streaming/recording flag from obs-websocket (best effort; "" = no OBS config found).
func (c *appControl) obsEncoderFunc() encoderscan.OBSEncoderFunc {
	return func() (stream, record string, active bool, err error) {
		s, r, ok := encoderscan.OBSConfigEncoder()
		if !ok {
			return "", "", false, nil
		}
		if c.obs != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if st, e := c.obs.GetStreamStatus(ctx); e == nil && st.Active {
				active = true
			}
			if rs, e := c.obs.GetRecordStatus(ctx); e == nil && rs.Active {
				active = true
			}
		}
		return s, r, active, nil
	}
}

// Perf builds this instance's perf-diagnosis report: build stamp + the perfmon rings, system +
// top-process sampling (~1s), feature children, and every registered probe section.
func (c *appControl) Perf() string {
	hdr := fmt.Sprintf("build %s (#%d) %s\n", version.Version, version.BuildNum(), version.Commit)
	if c.perfMon == nil {
		return hdr + "perf collector unavailable"
	}
	return hdr + c.perfMon.Report()
}

// LogTail returns the recent formatted log ring (the same lines `ctl logs` streams), at most max
// lines (0 → 500), keeping only lines containing filter (case-insensitive; "" = all). Serves the
// peer app.logs RPC so a paired controller can read this machine's diagnostics remotely.
func (c *appControl) LogTail(max int, filter string) string {
	if max <= 0 {
		max = 500
	}
	filter = strings.ToLower(filter)
	entries := c.log.Snapshot()
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		line := fmt.Sprintf("%s %-5s [%s] %s", e.Time.Format("15:04:05.000"), e.Level, e.Source, e.Msg)
		if len(e.Fields) > 0 {
			line += fmt.Sprintf(" %v", e.Fields)
		}
		if filter != "" && !strings.Contains(strings.ToLower(line), filter) {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return strings.Join(lines, "\n")
}

// RemoteLogs asks a paired peer for its recent log tail (nodeID "" = first connected), optionally
// substring-filtered - read a headset/desk peer's diagnostics without physical access.
func (c *appControl) RemoteLogs(nodeID, filter string, max int) string {
	if c.peerMgr == nil || c.remoteCtl == nil {
		return "error: peer link unavailable"
	}
	if nodeID == "" {
		for _, p := range c.peerMgr.Connections() {
			if p.Status == peerlink.StatusConnected {
				nodeID = p.NodeID
				break
			}
		}
	}
	if nodeID == "" {
		return "error: no connected peer"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	txt, err := remotectl.NewClient(c.remoteCtl, nodeID).Logs(ctx, max, filter)
	if err != nil {
		return "error: " + err.Error()
	}
	return txt
}

// RemotePerf asks a paired peer for its perf report (nodeID "" = first connected) - "is the VR
// PC slow because of rave-mate or something else on that box", answered from the desk instance.
func (c *appControl) RemotePerf(nodeID string) string {
	if c.peerMgr == nil || c.remoteCtl == nil {
		return "error: peer link unavailable"
	}
	if nodeID == "" {
		for _, p := range c.peerMgr.Connections() {
			if p.Status == peerlink.StatusConnected {
				nodeID = p.NodeID
				break
			}
		}
	}
	if nodeID == "" {
		return "error: no connected peer (run `ctl list-peers`)"
	}
	client := remotectl.NewClient(c.remoteCtl, nodeID)
	if client == nil {
		return "error: invalid peer"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second) // peer samples ~1s
	defer cancel()
	text, err := client.Perf(ctx)
	if err != nil {
		return "error: " + err.Error()
	}
	return text
}

// RemoteEncoderScan asks a paired peer for its live encoder-utilization scan + placement plan
// (nodeID "" = first connected). Read-only on the peer (PDH GPU-engine sampling, no encode) - lets
// the desk instance see the VR PC's encoder headroom BEFORE launching a peer-link stream to it.
func (c *appControl) RemoteEncoderScan(nodeID string) string {
	if c.peerMgr == nil || c.remoteCtl == nil {
		return "error: peer link unavailable"
	}
	if nodeID == "" {
		for _, p := range c.peerMgr.Connections() {
			if p.Status == peerlink.StatusConnected {
				nodeID = p.NodeID
				break
			}
		}
	}
	if nodeID == "" {
		return "error: no connected peer (run `ctl list-peers`)"
	}
	client := remotectl.NewClient(c.remoteCtl, nodeID)
	if client == nil {
		return "error: invalid peer"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second) // peer samples ~300ms
	defer cancel()
	text, err := client.EncoderScan(ctx)
	if err != nil {
		return "error: " + err.Error()
	}
	return text
}

// RemoteSelfUpdate triggers a paired peer's self-update+relaunch (nodeID "" = first connected). Lets
// a desk instance update a headset PC's rave-mate the instant a CI build lands - app relaunch only.
func (c *appControl) RemoteSelfUpdate(nodeID string) string {
	if c.peerMgr == nil || c.remoteCtl == nil {
		return "error: peer link unavailable"
	}
	if nodeID == "" {
		for _, p := range c.peerMgr.Connections() {
			if p.Status == peerlink.StatusConnected {
				nodeID = p.NodeID
				break
			}
		}
	}
	if nodeID == "" {
		return "error: no connected peer (run `ctl list-peers`)"
	}
	client := remotectl.NewClient(c.remoteCtl, nodeID)
	if client == nil {
		return "error: invalid peer"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	text, err := client.SelfUpdate(ctx)
	if err != nil {
		return "error: " + err.Error()
	}
	return "peer " + nodeID + ": " + text
}

// RemoteListDir lists a paired peer's directory over the peer link (nodeID "" = first connected).
// Debug aid: inspect a headset PC's files (e.g. the written SteamVR action manifest + binding files).
func (c *appControl) RemoteListDir(nodeID, path string) string {
	if c.peerMgr == nil || c.remoteCtl == nil {
		return "error: peer link unavailable"
	}
	if path == "" {
		return "usage: remote-ls <path> [nodeID]"
	}
	if nodeID == "" {
		for _, p := range c.peerMgr.Connections() {
			if p.Status == peerlink.StatusConnected {
				nodeID = p.NodeID
				break
			}
		}
	}
	if nodeID == "" {
		return "error: no connected peer (run `ctl list-peers`)"
	}
	client := remotectl.NewClient(c.remoteCtl, nodeID)
	if client == nil {
		return "error: invalid peer"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	l, err := client.ListDirectory(ctx, path, true)
	if err != nil {
		return "error: " + err.Error()
	}
	if l.Error != "" {
		return "error: " + l.Error
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d entries)\n", l.Path, len(l.Entries))
	for _, e := range l.Entries {
		tag := "f"
		if e.IsDirectory {
			tag = "d"
		}
		fmt.Fprintf(&b, "  %s %10d  %s  %s\n", tag, e.SizeBytes, e.ModifiedAt, e.Name)
	}
	return b.String()
}

// SubscribeLogs replays the ring buffer then streams new entries as formatted lines for
// the CLI log viewer (`rave-mate ctl logs`).
func (c *appControl) SubscribeLogs() (<-chan string, func()) {
	src, cancel := c.log.Subscribe()
	out := make(chan string, 1024)
	fmtLine := func(e logbus.Entry) string {
		line := fmt.Sprintf("%s %-5s [%s] %s", e.Time.Format("15:04:05.000"), e.Level, e.Source, e.Msg)
		if len(e.Fields) > 0 {
			line += fmt.Sprintf(" %v", e.Fields)
		}
		return line
	}
	debuglog.Go(c.log, "ctl", func() {
		defer close(out)
		for _, e := range c.log.Snapshot() { // recent history first
			out <- fmtLine(e)
		}
		for e := range src {
			select {
			case out <- fmtLine(e):
			default: // drop if the client can't keep up
			}
		}
	})
	return out, cancel
}

func stderrSink(log *logbus.Bus) {
	ch, _ := log.Subscribe()
	for e := range ch {
		fmt.Fprintf(os.Stderr, "%s %-5s [%s] %s\n", e.Time.Format("15:04:05"), e.Level, e.Source, e.Msg)
	}
}

func modeLabel(service bool) string {
	if service {
		return "service"
	}
	return "windowed"
}

// motionRecordingsDir is the Motion Studio recordings dir (mirrors vroverlay/motion.go +
// ui/view_motion.go). Empty if the config dir can't be resolved.
func motionRecordingsDir() string {
	p, err := config.DataPath("vr_recordings.x")
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "vr_recordings")
}
