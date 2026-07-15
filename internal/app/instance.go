package app

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/shared/auth"
)

// lockAddr is the single-instance guard + control IPC channel. The primary holds the
// listen for its lifetime; secondary launches forward deeplinks, and the `rave-mate ctl`
// CLI connects here to query/drive the running daemon (status, logs, show, tab, quit).
// RAVE_MATE_CTL_ADDR overrides it (dev/test: isolated instance beside a real one; pair
// with RAVE_MATE_CONFIG_DIR so state stays isolated too).
var lockAddr = envOr("RAVE_MATE_CTL_ADDR", "127.0.0.1:47620")

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Control is the daemon-side surface the IPC server exposes to CLI clients.
type Control interface {
	Show()
	Quit()
	SelectTab(name string) (bool, []string) // ok, available names
	Status() string
	Snapshot() string                                            // text tree of the rendered UI (empty in service mode)
	Resize(w, h float32) bool                                    // set the window size (viewport); false in service mode
	Click(query string) bool                                     // tap a button/check/tab by label; false if no match / service mode
	Act(act, val string) bool                                    // post a raw UI action through the page act pipeline (webview renderer only)
	Tap(x, y float32) bool                                       // tap the topmost leaf at canvas coords; false if no hit / service mode
	TapSecondary(x, y float32) bool                              // right-click: fire the deepest SecondaryTappable (context menus); false if no hit / service
	Type(text string) bool                                       // append text to the focused Entry; false if no entry / service mode
	Read(query string) (string, bool)                            // current value of a leaf matched by label; ("", false) on miss / service
	Set(query, value string) bool                                // mutate Entry/Select/Check matched by label; false on miss / service
	Screenshot(path string) bool                                 // capture the window to a PNG at path; false on failure / service mode
	ScreenshotAll(dir string) string                             // sweep EVERY tab (+scroll positions) to PNGs + report.txt in dir; status line
	ScreenshotRegion(path string, x, y, w, h float32) bool       // capture a sub-rect; false on failure / service
	ScreenshotVR(path string) bool                               // capture the SteamVR VR-View mirror window to a PNG; false if opt-in off / not found / non-windows
	GioSnapshot(windowID string) string                          // Gio surfaces: labeled-control tree of one window; "" lists open windows
	GioTap(windowID, controlID string) string                    // Gio surfaces: queue a control activation; status line
	ListPeers() string                                           // connected paired peers (nodeID/nick/status), one per line
	RemoteScreenshot(nodeID, path string, vr bool) string        // screenshot a paired peer's app window (vr=VR-View) to a local PNG; status line
	VRInputDiag() string                                         // this instance's SteamVR Input action/binding diagnostic
	RemoteVRInputDiag(nodeID string) string                      // a paired peer's VR input/binding diagnostic (nodeID "" = first connected)
	Perf() string                                                // this instance's perf-diagnosis report (perfmon rings + system + probes; samples ~1s)
	EncoderScan() string                                         // encoder-contention scan + affinity plan preview (ctl encoder-scan; read-only)
	RemotePerf(nodeID string) string                             // a paired peer's perf report (nodeID "" = first connected)
	RemoteLogs(nodeID, filter string, max int) string            // a paired peer's recent log tail (nodeID "" = first connected; filter substring)
	RemoteEncoderScan(nodeID string) string                      // a paired peer's encoder-utilization scan + plan (nodeID "" = first connected; read-only)
	PprofCPU(seconds int) string                                 // CPU-profile the daemon for N s (blocks) → config-dir .pprof path + top summary
	PprofHeap() string                                           // heap profile → config-dir .pprof path + top summary
	Goroutines() string                                          // full goroutine dump (debug=1), inline text
	RemotePprofCPU(nodeID, localPath string, seconds int) string // a paired peer's CPU profile → local .pprof at localPath
	RemotePprofHeap(nodeID, localPath string) string             // a paired peer's heap profile → local .pprof at localPath
	RemoteGoroutines(nodeID string) string                       // a paired peer's goroutine dump, inline text
	RemoteListDir(nodeID, path string) string                    // list a paired peer's directory (debug: inspect its files)
	RemoteSelfUpdate(nodeID string) string                       // trigger a paired peer's self-update+relaunch (remote VR-PC update)
	HandleDeepLink(url string)
	AdoptToken(payloadB64 string) string         // adopt a co-located app's token bundle; returns status
	SelfUpdate() string                          // run the self-updater (cross-app coordination); returns status
	UploadRecordedSet(recordingID string) string // publish a local recording to the play layer; "ok <streamId>"/error
	SyncRecordedSet(recordingID string) string   // identify a recording's tracks against the catalog; status line
	ListRecordedSets() string                    // JSON array of local recordings + upload status
	SyncLibrary() string                         // start the async library metadata upload; "started"/"already running"/error token
	LibrarySyncStatus() string                   // one-line JSON: running + last result/timestamps
	SyncMedia(budget int) string                 // start the async waveform/artwork upload; "started"/"already running"/error token
	MediaSyncStatus() string                     // one-line JSON: running + progress/last result
	SyncPlaylists() string                       // start the async playlist sync (push local-ahead + pull remote-ahead); status token
	PlaylistSyncStatus() string                  // one-line JSON: running + last run's per-playlist statuses
	CleanupMissing(dry bool) string              // remove missing-file tracks from DB + prune collection.nml (dry=report only)
	SyncDJ(id string, dry bool) string           // start a cross-DJ-software sync job; "started"/"already running"/error token
	DJSyncStatus() string                        // one-line JSON: running + last cross-DJ run result
	DJSyncList() string                          // JSON array of configured sync jobs
	DJSyncAutoStatus() string                    // one-line JSON: auto-sync scheduler state + armed jobs
	VRPerf() string                              // JSON array: VR perf/debug telemetry from every instance
	DMXStatus() string                           // DMX plane: universes seen (pps, source IP) + grid sink state
	StreamStatus() string                        // VRSL DMX-over-video stream: push target + encoder state
	TCStatus() string                            // one line: timecode running/rate/current TC/sinks on-off
	TCStart() string                             // start the house timecode clock; status line
	TCStop() string                              // stop the house timecode clock; status line
	OBSSyncStatus() string                       // one-line JSON: media-sync house clock + per-source chase status
	AbleLinkStatus() string                      // one-line JSON: Ableton Link session state (tempo/beat/phase/peers)
	AbleLinkResync() string                      // hard-realign the Link phrase (map beat 0 to now); status line
	LaunchAppGroup(id string) string             // relaunch a crash-recovery app group; status line
	GPUSelfTest() string                         // inject a synthetic GPU hang to prove auto-recovery (relaunches)
	SubscribeLogs() (<-chan string, func())      // stream formatted log lines; cancel to stop
}

type instance struct{ ln net.Listener }

// acquire becomes the primary instance, or returns primary=false if one already runs.
func acquire() (*instance, bool) {
	ln, err := net.Listen("tcp", lockAddr)
	if err != nil {
		return nil, false
	}
	return &instance{ln: ln}, true
}

// acquireWithRetry tries to become primary, retrying (250ms steps) until d elapses. Used after a
// self-update relaunch: the prior instance is still releasing the lock as it exits, so a plain
// one-shot acquire would lose the race and the relaunched app would forward-show + exit.
func acquireWithRetry(d time.Duration) (*instance, bool) {
	deadline := time.Now().Add(d)
	for {
		if inst, ok := acquire(); ok {
			return inst, true
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (i *instance) close() { _ = i.ln.Close() }

// serve handles forwarded deeplinks + CLI control commands until the listener closes.
// Handlers run panic-guarded - a control command (e.g. Snapshot into the UI tree) that
// panics logs instead of killing the daemon.
func (i *instance) serve(ctrl Control, bus *logbus.Bus) {
	for {
		conn, err := i.ln.Accept()
		if err != nil {
			return
		}
		debuglog.Go(bus, "ctl", func() { handleConn(conn, ctrl) })
	}
}

// parseActPayload splits an `ACT <act> [val]` payload. Unquoted payloads keep the legacy
// first-whitespace split (every existing caller/script unchanged). A leading `"` quotes the
// act so it can carry embedded whitespace - prefix-acts embedding paths, e.g.
// `"ce-open:C:\My Music\track.flac" 0.5`: the act runs to the next unescaped `"`; `\"`
// yields a literal quote, every other byte (backslashes included - Windows/UNC paths need
// no doubling) is verbatim; the trimmed remainder is val. No closing quote = malformed ->
// legacy raw split.
func parseActPayload(rest string) (act, val string) {
	if len(rest) > 1 && rest[0] == '"' {
		var b strings.Builder
		for i := 1; i < len(rest); i++ {
			switch {
			case rest[i] == '\\' && i+1 < len(rest) && rest[i+1] == '"':
				b.WriteByte('"')
				i++
			case rest[i] == '"':
				return b.String(), strings.TrimSpace(rest[i+1:])
			default:
				b.WriteByte(rest[i])
			}
		}
	}
	act, val = rest, ""
	if sp := strings.IndexAny(rest, " \t"); sp >= 0 {
		act, val = rest[:sp], strings.TrimSpace(rest[sp:])
	}
	return act, val
}

func handleConn(conn net.Conn, ctrl Control) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	cmd := strings.TrimSpace(line)

	switch {
	case cmd == showMsg:
		ctrl.Show()
		fmt.Fprintln(conn, "ok")
	case cmd == "QUIT":
		fmt.Fprintln(conn, "ok")
		ctrl.Quit()
	case cmd == "STATUS":
		fmt.Fprintln(conn, ctrl.Status())
	case cmd == "SNAPSHOT":
		fmt.Fprint(conn, ctrl.Snapshot()) // multi-line; client reads to EOF (conn close)
	case strings.HasPrefix(cmd, "CLICK "):
		if ctrl.Click(strings.TrimSpace(cmd[len("CLICK "):])) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "no match")
		}
	case strings.HasPrefix(cmd, "ACT "):
		// ACT <act> [val] - post through the page act pipeline (what a page event sends)
		act, val := parseActPayload(strings.TrimSpace(cmd[len("ACT "):]))
		if ctrl.Act(act, val) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "unsupported (webview renderer only)")
		}
	case strings.HasPrefix(cmd, "TAP2 "):
		var x, y float32
		if _, err := fmt.Sscanf(strings.TrimSpace(cmd[len("TAP2 "):]), "%f %f", &x, &y); err == nil && ctrl.TapSecondary(x, y) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "no hit")
		}
	case strings.HasPrefix(cmd, "TAP "):
		var x, y float32
		if _, err := fmt.Sscanf(strings.TrimSpace(cmd[len("TAP "):]), "%f %f", &x, &y); err == nil && ctrl.Tap(x, y) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "no hit")
		}
	case strings.HasPrefix(cmd, "TYPE "):
		if ctrl.Type(strings.TrimRight(cmd[len("TYPE "):], "\r\n")) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "no focused entry")
		}
	case strings.HasPrefix(cmd, "READ "):
		if v, ok := ctrl.Read(strings.TrimSpace(cmd[len("READ "):])); ok {
			fmt.Fprintln(conn, v)
		} else {
			fmt.Fprintln(conn, "no match")
		}
	case strings.HasPrefix(cmd, "SET "):
		// SET <label-substring> <value...> - split first token as the query,
		// the rest of the line is the value (so values with spaces round-trip).
		rest := strings.TrimSpace(cmd[len("SET "):])
		sp := strings.IndexAny(rest, " \t")
		if sp < 0 {
			fmt.Fprintln(conn, "usage: SET <label> <value>")
		} else if ctrl.Set(rest[:sp], strings.TrimSpace(rest[sp:])) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "no match")
		}
	case strings.HasPrefix(cmd, "SCREENSHOT-ALL "):
		fmt.Fprintln(conn, ctrl.ScreenshotAll(strings.TrimSpace(cmd[len("SCREENSHOT-ALL "):])))
	case strings.HasPrefix(cmd, "SCREENSHOT-REGION "):
		var x, y, w, h float32
		var p string
		if _, err := fmt.Sscanf(strings.TrimSpace(cmd[len("SCREENSHOT-REGION "):]), "%s %f %f %f %f", &p, &x, &y, &w, &h); err == nil && ctrl.ScreenshotRegion(p, x, y, w, h) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "failed (no UI?)")
		}
	case strings.HasPrefix(cmd, "SCREENSHOT-VR "):
		if ctrl.ScreenshotVR(strings.TrimSpace(cmd[len("SCREENSHOT-VR "):])) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "failed (vr-view capture off / no VR View window / non-windows)")
		}
	case cmd == "GIO-SNAPSHOT" || strings.HasPrefix(cmd, "GIO-SNAPSHOT "):
		// multi-line; client reads to EOF. No arg = list open Gio windows.
		fmt.Fprint(conn, ctrl.GioSnapshot(strings.TrimSpace(strings.TrimPrefix(cmd, "GIO-SNAPSHOT"))))
	case strings.HasPrefix(cmd, "GIO-TAP "):
		f := strings.Fields(cmd[len("GIO-TAP "):])
		if len(f) < 2 {
			fmt.Fprintln(conn, "usage: GIO-TAP <windowID> <controlID>")
		} else {
			fmt.Fprintln(conn, ctrl.GioTap(f[0], strings.Join(f[1:], " ")))
		}
	case strings.HasPrefix(cmd, "SCREENSHOT "):
		if ctrl.Screenshot(strings.TrimSpace(cmd[len("SCREENSHOT "):])) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "failed (no UI?)")
		}
	case strings.HasPrefix(cmd, "RESIZE "):
		var w, h float32
		if _, err := fmt.Sscanf(strings.TrimSpace(cmd[len("RESIZE "):]), "%fx%f", &w, &h); err == nil && ctrl.Resize(w, h) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "usage: RESIZE <w>x<h> (no UI in service mode)")
		}
	case strings.HasPrefix(cmd, "TAB "):
		ok, names := ctrl.SelectTab(strings.TrimSpace(cmd[len("TAB "):]))
		if ok {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "unknown tab; available: "+strings.Join(names, ", "))
		}
	case strings.HasPrefix(cmd, "ADOPT-TOKEN "):
		fmt.Fprintln(conn, ctrl.AdoptToken(strings.TrimSpace(cmd[len("ADOPT-TOKEN "):])))
	case cmd == "SELF-UPDATE":
		fmt.Fprintln(conn, ctrl.SelfUpdate())
	case strings.HasPrefix(cmd, "UPLOAD-SET "):
		fmt.Fprintln(conn, ctrl.UploadRecordedSet(strings.TrimSpace(cmd[len("UPLOAD-SET "):])))
	case strings.HasPrefix(cmd, "SYNC-SET "):
		fmt.Fprintln(conn, ctrl.SyncRecordedSet(strings.TrimSpace(cmd[len("SYNC-SET "):])))
	case cmd == "LIST-RECORDED-SETS":
		fmt.Fprintln(conn, ctrl.ListRecordedSets())
	case cmd == "SYNC-LIBRARY":
		fmt.Fprintln(conn, ctrl.SyncLibrary())
	case cmd == "LIBRARY-SYNC-STATUS":
		fmt.Fprintln(conn, ctrl.LibrarySyncStatus())
	case cmd == "SYNC-MEDIA" || strings.HasPrefix(cmd, "SYNC-MEDIA "):
		budget := 0 // 0 = default waveform budget
		if rest := strings.TrimSpace(strings.TrimPrefix(cmd, "SYNC-MEDIA")); rest != "" {
			if _, err := fmt.Sscanf(rest, "%d", &budget); err != nil {
				fmt.Fprintln(conn, "usage: SYNC-MEDIA [budget]")
				return
			}
		}
		fmt.Fprintln(conn, ctrl.SyncMedia(budget))
	case cmd == "MEDIA-SYNC-STATUS":
		fmt.Fprintln(conn, ctrl.MediaSyncStatus())
	case cmd == "SYNC-PLAYLISTS":
		fmt.Fprintln(conn, ctrl.SyncPlaylists())
	case cmd == "PLAYLIST-SYNC-STATUS":
		fmt.Fprintln(conn, ctrl.PlaylistSyncStatus())
	case cmd == "CLEANUP-MISSING" || cmd == "CLEANUP-MISSING DRY":
		fmt.Fprintln(conn, ctrl.CleanupMissing(cmd == "CLEANUP-MISSING DRY"))
	case strings.HasPrefix(cmd, "SYNC-DJ "):
		id, dry := parseSyncDJCmd(cmd)
		fmt.Fprintln(conn, ctrl.SyncDJ(id, dry))
	case cmd == "LIBSYNC-STATUS":
		fmt.Fprintln(conn, ctrl.DJSyncStatus())
	case cmd == "LIBSYNC-LIST":
		fmt.Fprintln(conn, ctrl.DJSyncList())
	case cmd == "LIBSYNC-AUTO-STATUS":
		fmt.Fprintln(conn, ctrl.DJSyncAutoStatus())
	case cmd == "VRPERF":
		fmt.Fprintln(conn, ctrl.VRPerf())
	case cmd == "DMX-STATUS":
		fmt.Fprintln(conn, ctrl.DMXStatus())
	case cmd == "STREAM-STATUS":
		fmt.Fprintln(conn, ctrl.StreamStatus())
	case cmd == "TC-STATUS":
		fmt.Fprintln(conn, ctrl.TCStatus())
	case cmd == "TC-START":
		fmt.Fprintln(conn, ctrl.TCStart())
	case cmd == "TC-STOP":
		fmt.Fprintln(conn, ctrl.TCStop())
	case cmd == "OBS-SYNC-STATUS":
		fmt.Fprintln(conn, ctrl.OBSSyncStatus())
	case cmd == "ABLELINK-STATUS":
		fmt.Fprintln(conn, ctrl.AbleLinkStatus())
	case cmd == "ABLELINK-RESYNC":
		fmt.Fprintln(conn, ctrl.AbleLinkResync())
	case strings.HasPrefix(cmd, "LAUNCH-GROUP "):
		fmt.Fprintln(conn, ctrl.LaunchAppGroup(strings.TrimSpace(cmd[len("LAUNCH-GROUP "):])))
	case cmd == "GPU-SELFTEST":
		fmt.Fprintln(conn, ctrl.GPUSelfTest())
	case cmd == "LIST-PEERS":
		fmt.Fprintln(conn, ctrl.ListPeers())
	case strings.HasPrefix(cmd, "REMOTE-SHOT-VR "):
		nodeID, path := parseRemoteShot(cmd[len("REMOTE-SHOT-VR "):])
		fmt.Fprintln(conn, ctrl.RemoteScreenshot(nodeID, path, true))
	case strings.HasPrefix(cmd, "REMOTE-SHOT "):
		nodeID, path := parseRemoteShot(cmd[len("REMOTE-SHOT "):])
		fmt.Fprintln(conn, ctrl.RemoteScreenshot(nodeID, path, false))
	case cmd == "VRINPUT":
		fmt.Fprintln(conn, ctrl.VRInputDiag())
	case strings.HasPrefix(cmd, "REMOTE-VRINPUT"):
		fmt.Fprintln(conn, ctrl.RemoteVRInputDiag(strings.TrimSpace(cmd[len("REMOTE-VRINPUT"):])))
	case cmd == "PERF":
		fmt.Fprintln(conn, ctrl.Perf())
	case cmd == "ENCODER-SCAN":
		fmt.Fprint(conn, ctrl.EncoderScan()) // multi-line; client reads to EOF

	case strings.HasPrefix(cmd, "REMOTE-ENCODER-SCAN"):
		fmt.Fprintln(conn, ctrl.RemoteEncoderScan(strings.TrimSpace(cmd[len("REMOTE-ENCODER-SCAN"):])))
	case strings.HasPrefix(cmd, "REMOTE-PERF"):
		fmt.Fprintln(conn, ctrl.RemotePerf(strings.TrimSpace(cmd[len("REMOTE-PERF"):])))
	case strings.HasPrefix(cmd, "REMOTE-LOGS"):
		// arg = optional filter substring; nodeID auto-selects the first connected peer.
		fmt.Fprint(conn, ctrl.RemoteLogs("", strings.TrimSpace(cmd[len("REMOTE-LOGS"):]), 0)) // multi-line; read to EOF
	case cmd == "PPROF-CPU" || strings.HasPrefix(cmd, "PPROF-CPU "):
		secs := 0
		if rest := strings.TrimSpace(strings.TrimPrefix(cmd, "PPROF-CPU")); rest != "" {
			if _, err := fmt.Sscanf(rest, "%d", &secs); err != nil {
				fmt.Fprintln(conn, "usage: PPROF-CPU [seconds]")
				return
			}
		}
		fmt.Fprintln(conn, ctrl.PprofCPU(secs)) // blocks the capture window; per-conn goroutine, no deadline on writes
	case cmd == "PPROF-HEAP":
		fmt.Fprintln(conn, ctrl.PprofHeap())
	case cmd == "GOROUTINES":
		fmt.Fprintln(conn, ctrl.Goroutines())
	case strings.HasPrefix(cmd, "REMOTE-PPROF-CPU "):
		path, secs, nodeID, err := parseRemotePprof(cmd[len("REMOTE-PPROF-CPU "):])
		if err != nil {
			fmt.Fprintln(conn, "usage: REMOTE-PPROF-CPU <path> [seconds] [nodeID]")
			return
		}
		fmt.Fprintln(conn, ctrl.RemotePprofCPU(nodeID, path, secs))
	case strings.HasPrefix(cmd, "REMOTE-PPROF-HEAP "):
		nodeID, path := parseRemoteShot(cmd[len("REMOTE-PPROF-HEAP "):]) // same "<path> [nodeID]" shape
		if path == "" {
			fmt.Fprintln(conn, "usage: REMOTE-PPROF-HEAP <path> [nodeID]")
			return
		}
		fmt.Fprintln(conn, ctrl.RemotePprofHeap(nodeID, path))
	case cmd == "REMOTE-GOROUTINES" || strings.HasPrefix(cmd, "REMOTE-GOROUTINES "):
		fmt.Fprintln(conn, ctrl.RemoteGoroutines(strings.TrimSpace(strings.TrimPrefix(cmd, "REMOTE-GOROUTINES"))))
	case strings.HasPrefix(cmd, "REMOTE-LS "):
		nodeID, path := parseRemoteShot(cmd[len("REMOTE-LS "):]) // reuse "<path> [nodeID]" parse
		fmt.Fprintln(conn, ctrl.RemoteListDir(nodeID, path))
	case strings.HasPrefix(cmd, "REMOTE-UPDATE"):
		fmt.Fprintln(conn, ctrl.RemoteSelfUpdate(strings.TrimSpace(cmd[len("REMOTE-UPDATE"):])))
	case cmd == "LOGS":
		streamLogs(conn, ctrl)
	case auth.IsDeepLink(cmd):
		ctrl.HandleDeepLink(cmd)
	}
}

// streamLogs pushes formatted log lines to the client until either side disconnects.
func streamLogs(conn net.Conn, ctrl Control) {
	ch, cancel := ctrl.SubscribeLogs()
	defer cancel()
	_ = conn.SetReadDeadline(time.Time{}) // no deadline while streaming
	// Detect client disconnect (it sends nothing more) to stop the stream.
	debuglog.Go(nil, "ctl", func() { _, _ = io.Copy(io.Discard, conn); cancel() })
	for ln := range ch {
		if _, err := fmt.Fprintln(conn, ln); err != nil {
			return
		}
	}
}

const showMsg = "SHOW"

// forward sends one fire-and-forget message to the running primary (deeplink / SHOW).
func forward(msg string) error {
	conn, err := net.DialTimeout("tcp", lockAddr, 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_, err = conn.Write([]byte(msg + "\n"))
	return err
}

// ── CLI control client (used by `rave-mate ctl …`) ───────────────────────────

// errNoDaemon is returned when nothing is listening on the control socket.
var errNoDaemon = fmt.Errorf("no running rave-mate instance (start the app first)")

// Send issues a one-shot control command and returns the daemon's single-line reply.
func Send(cmd string) (string, error) {
	conn, err := net.DialTimeout("tcp", lockAddr, 2*time.Second)
	if err != nil {
		return "", errNoDaemon
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return "", err
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := bufio.NewReader(conn).ReadString('\n')
	return strings.TrimRight(resp, "\r\n"), err
}

// SendMulti sends cmd and returns the FULL response read to EOF - for commands whose reply spans
// multiple lines (e.g. VRINPUT / REMOTE-VRINPUT diagnostics), which Send would truncate at the
// first newline.
func SendMulti(cmd string) (string, error) {
	conn, err := net.DialTimeout("tcp", lockAddr, 2*time.Second)
	if err != nil {
		return "", errNoDaemon
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return "", err
	}
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	data, err := io.ReadAll(conn)
	return strings.TrimRight(string(data), "\r\n"), err
}

// SendMultiTimeout is SendMulti with a caller-set read deadline - for commands that legitimately
// block before replying (PPROF-CPU captures for N seconds; remote-pprof adds a peer round-trip).
func SendMultiTimeout(cmd string, d time.Duration) (string, error) {
	conn, err := net.DialTimeout("tcp", lockAddr, 2*time.Second)
	if err != nil {
		return "", errNoDaemon
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return "", err
	}
	_ = conn.SetReadDeadline(time.Now().Add(d))
	data, err := io.ReadAll(conn)
	return strings.TrimRight(string(data), "\r\n"), err
}

// Snapshot connects, requests a UI snapshot, and returns the full multi-line text (read to
// EOF). Used by `rave-mate ctl snapshot`.
func Snapshot() (string, error) {
	conn, err := net.DialTimeout("tcp", lockAddr, 2*time.Second)
	if err != nil {
		return "", errNoDaemon
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("SNAPSHOT\n")); err != nil {
		return "", err
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(conn)
	return string(data), err
}

// StreamLogs connects, requests the log stream, and copies it to out until the daemon
// exits or the process is interrupted.
func StreamLogs(out io.Writer) error {
	conn, err := net.DialTimeout("tcp", lockAddr, 2*time.Second)
	if err != nil {
		return errNoDaemon
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("LOGS\n")); err != nil {
		return err
	}
	_, err = io.Copy(out, conn)
	return err
}

// parseRemoteShot parses "<path> [nodeID]" from a REMOTE-SHOT command tail.
func parseRemoteShot(s string) (nodeID, path string) {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return "", ""
	}
	path = f[0]
	if len(f) > 1 {
		nodeID = f[1]
	}
	return nodeID, path
}

// parseRemotePprof parses "<path> [seconds] [nodeID]" - after the path, an integer token is
// seconds and any other token is the nodeID (order-independent; node IDs are never all-digits).
func parseRemotePprof(s string) (path string, seconds int, nodeID string, err error) {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return "", 0, "", fmt.Errorf("path required")
	}
	path = f[0]
	for _, tok := range f[1:] {
		if n, e := strconv.Atoi(tok); e == nil {
			seconds = n
		} else {
			nodeID = tok
		}
	}
	return path, seconds, nodeID, nil
}

// extractDeepLink returns the first launch arg that is an auth deeplink, else "".
func extractDeepLink(args []string) string {
	for _, a := range args {
		if auth.IsDeepLink(a) {
			return a
		}
	}
	return ""
}
