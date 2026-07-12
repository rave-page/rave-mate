// Command rave-mate is the cross-platform native companion for rave.page. Modes:
//
//	rave-mate                     windowed (tray + window)
//	rave-mate --service|--headless   headless daemon (no window; logs to stderr)
//	rave-mate worker <type>       a job worker subprocess (spawned by the daemon)
//	rave-mate feature <name>      a feature-host subprocess (spawned by the daemon)
//	rave-mate install|uninstall|status   OS service management
//	rave-mate ctl <cmd>           control a running instance
//	                             (status/logs/show/tab/quit/resize/click/tap/type/read/set/screenshot/screenshot-region/screenshot-vr)
//
// See CLAUDE.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/app"
	"rave.page/mate/internal/elevate"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/guardian"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/serato"
	"rave.page/mate/internal/service"
	"rave.page/mate/internal/session/sources/rekordboxsrc"
	"rave.page/mate/internal/sysexec"
	"rave.page/mate/internal/traktormap"
	"rave.page/mate/internal/traktorqml"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/virtualdj"
	"rave.page/mate/internal/worker"
)

func main() {
	// When launched by the Windows SCM, run the service handler and nothing else.
	if service.RunWindowsServiceIfNeeded(func(ctx context.Context) error { return app.RunCtx(ctx, true) }) {
		return
	}

	// Subcommands run before flag parsing. A custom-scheme deeplink (ravepage://…) is
	// NOT a subcommand and falls through to the default app path, which extracts it.
	if args := os.Args[1:]; len(args) > 0 {
		switch args[0] {
		case "worker":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "usage: rave-mate worker <type>")
				os.Exit(2)
			}
			sysexec.SetProcName("rmate-" + args[1]) // Linux comm: rmate-probe / rmate-transcode …
			os.Exit(worker.RunWorker(args[1]))
		case "feature":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "usage: rave-mate feature <name>")
				os.Exit(2)
			}
			sysexec.SetProcName("rmate-" + args[1]) // Linux comm: rmate-player / rmate-traktor …
			os.Exit(featurehost.RunFeature(args[1]))
		case "guardian": // crash supervisor child (spawned by the app; not user-facing)
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "usage: rave-mate guardian <state-dir>")
				os.Exit(2)
			}
			sysexec.SetProcName("rmate-guardian")
			os.Exit(guardian.Run(args[1]))
		case "install", "uninstall", "status":
			os.Exit(runServiceCmd(args[0]))
		case "ctl":
			os.Exit(runCtl(args[1:]))
		case "import":
			os.Exit(runImport(args[1:]))
		case "traktor-qml":
			os.Exit(runTraktorQML(args[1:]))
		case "traktor-map":
			os.Exit(runTraktorMap(args[1:]))
		case "rbxscan":
			os.Exit(runRbxScan(args[1:]))
		case "version", "--version", "-v":
			fmt.Println("rave-mate", version.String())
			os.Exit(0)
		}
	}

	headless := flag.Bool("headless", false, "run headless for an OS service manager (no tray/window)")
	svc := flag.Bool("service", false, "alias of --headless")
	flag.Parse()

	if err := app.Run(*headless || *svc); err != nil {
		fmt.Fprintln(os.Stderr, "rave-mate:", err)
		os.Exit(1)
	}
}

// runRbxScan hunts rekordbox.exe memory for pointer chains to a known loaded string (title/artist),
// printing candidate static chains to seed the per-version offsets (rekordboxsrc/memory_windows.go).
func runRbxScan(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: rave-mate rbxscan "<a track title/artist currently loaded on a deck>"`)
		return 2
	}
	if err := rekordboxsrc.RunScan(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "rbxscan:", err)
		return 1
	}
	return 0
}

// runImport scans an external DJ library into the app's model (local only): discovers the
// install, reads the collection, prints a summary. Traktor / Serato / VirtualDJ.
func runImport(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rave-mate import <traktor|serato|virtualdj>")
		return 2
	}
	switch args[0] {
	case "traktor":
		return importTraktor()
	case "serato":
		return importSerato()
	case "virtualdj":
		return importVirtualDJ()
	default:
		fmt.Fprintln(os.Stderr, "usage: rave-mate import <traktor|serato|virtualdj>")
		return 2
	}
}

// importTraktor discovers every Traktor version, streams the newest collection, prints a summary.
func importTraktor() int {
	installs, err := musiclib.DiscoverTraktor()
	if err != nil || len(installs) == 0 {
		fmt.Fprintln(os.Stderr, "no Traktor install found under Documents/Native Instruments")
		return 1
	}
	for _, in := range installs {
		fmt.Printf("found Traktor %s - %s\n", in.Version, in.Dir)
	}
	in := installs[0]
	if in.Collection == "" {
		fmt.Fprintln(os.Stderr, "newest install has no collection.nml")
		return 1
	}
	f, err := os.Open(in.Collection)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open collection:", err)
		return 1
	}
	defer func() { _ = f.Close() }()

	fmt.Printf("scanning %s …\n", in.Collection)
	genres := map[string]int{}
	missing := 0
	n, err := musiclib.ParseCollection(f, func(t musiclib.Track) {
		if t.Genre != "" {
			genres[t.Genre]++
		}
		if _, e := os.Stat(t.Path); e != nil {
			missing++
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		return 1
	}
	fmt.Printf("\n%d tracks (%d files missing on disk) across %d genres\n", n, missing, len(genres))
	for _, g := range topN(genres, 8) {
		fmt.Printf("  %5d  %s\n", g.count, g.name)
	}
	if in.HistoryDir != "" {
		if ents, e := os.ReadDir(in.HistoryDir); e == nil {
			sessions := 0
			for _, en := range ents {
				if strings.HasSuffix(en.Name(), ".nml") {
					sessions++
				}
			}
			fmt.Printf("%d play-history sessions in %s\n", sessions, in.HistoryDir)
		}
	}
	return 0
}

// importSerato reads the Serato library (database V2 + crates) from the default _Serato_ dir
// (+ per-drive _Serato_ folders) and prints a summary.
func importSerato() int {
	dir, err := serato.DefaultDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "serato:", err)
		return 1
	}
	dirs := append([]string{dir}, serato.DrivesSeratoDirs()...)
	var tracks []serato.Track
	var crates int
	for _, d := range dirs {
		ts, cs, e := serato.LoadCollection(d)
		if e != nil {
			continue // a drive without a real _Serato_ db just contributes nothing
		}
		fmt.Printf("found %s - %d tracks, %d crates\n", d, len(ts), len(cs))
		tracks = append(tracks, ts...)
		crates += len(cs)
	}
	if len(tracks) == 0 {
		fmt.Fprintln(os.Stderr, "no Serato library found (is _Serato_/database V2 present?)")
		return 1
	}
	printLibrarySummary(len(tracks), countGenresMissing(tracks))
	fmt.Printf("%d crates\n", crates)
	return 0
}

// importVirtualDJ reads VirtualDJ's database.xml (Documents + per-drive) and prints a summary.
func importVirtualDJ() int {
	dir, err := virtualdj.DefaultDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "virtualdj:", err)
		return 1
	}
	files := virtualdj.DatabaseFiles(dir)
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "no VirtualDJ database.xml found under", dir)
		return 1
	}
	for _, f := range files {
		fmt.Printf("found %s\n", f)
	}
	tracks, err := virtualdj.LoadCollection(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		return 1
	}
	gs := map[string]int{}
	missing := 0
	for _, t := range tracks {
		if t.Genre != "" {
			gs[t.Genre]++
		}
		if t.Path != "" {
			if _, e := os.Stat(t.Path); e != nil {
				missing++
			}
		}
	}
	printLibrarySummary(len(tracks), summary{genres: gs, missing: missing})
	return 0
}

// summary holds a collection's genre histogram + count of files missing on disk.
type summary struct {
	genres  map[string]int
	missing int
}

// countGenresMissing tallies genres + on-disk-missing for Serato tracks.
func countGenresMissing(tracks []serato.Track) summary {
	gs := map[string]int{}
	missing := 0
	for _, t := range tracks {
		if t.Genre != "" {
			gs[t.Genre]++
		}
		if t.Path != "" {
			if _, e := os.Stat(t.Path); e != nil {
				missing++
			}
		}
	}
	return summary{genres: gs, missing: missing}
}

// printLibrarySummary prints the shared "N tracks (M missing) across G genres" + top genres.
func printLibrarySummary(n int, s summary) {
	fmt.Printf("\n%d tracks (%d files missing on disk) across %d genres\n", n, s.missing, len(s.genres))
	for _, g := range topN(s.genres, 8) {
		fmt.Printf("  %5d  %s\n", g.count, g.name)
	}
}

type genreCount struct {
	name  string
	count int
}

func topN(m map[string]int, n int) []genreCount {
	gs := make([]genreCount, 0, len(m))
	for k, v := range m {
		gs = append(gs, genreCount{k, v})
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].count > gs[j].count })
	if len(gs) > n {
		gs = gs[:n]
	}
	return gs
}

// runTraktorQML installs/removes/inspects the Traktor api-client QML mod (the localhost:8080
// feed). apply/revert write under Program Files, so they self-elevate (UAC) when not already
// admin; the elevated child writes any error to --result for the caller to read.
func runTraktorQML(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rave-mate traktor-qml <status|apply|revert> [--result <file>]")
		return 2
	}
	cmd := args[0]
	resultPath := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--result" && i+1 < len(args) {
			resultPath = args[i+1]
		}
	}

	in, ok, err := traktorqml.Newest()
	if err != nil {
		fmt.Fprintln(os.Stderr, "traktor-qml:", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "no Traktor Pro install with a D2 QML folder found under Program Files")
		return 1
	}

	switch cmd {
	case "status":
		s := traktorqml.Probe(in)
		fmt.Printf("Traktor %s\n  D2:      %s\n  patched: %v\n  Api/:    %v\n  healthy: %v\n  backups: %v\n",
			in.Version, in.D2Dir, s.Patched, s.ApiPresent, s.Healthy, s.HasBackup)
		return 0
	case "apply", "revert":
		if !elevate.IsElevated() {
			code, eerr := elevate.RunSelfElevated(append([]string{"traktor-qml"}, args...))
			if eerr != nil {
				if errors.Is(eerr, elevate.ErrDeclined) {
					fmt.Fprintln(os.Stderr, "elevation declined")
					return 1
				}
				fmt.Fprintln(os.Stderr, "elevation failed:", eerr)
				return 1
			}
			return code
		}
		var werr error
		if cmd == "apply" {
			_, werr = traktorqml.Apply(in, nil)
		} else {
			werr = traktorqml.Revert(in, nil)
		}
		writeQMLResult(resultPath, werr)
		if werr != nil {
			fmt.Fprintln(os.Stderr, "traktor-qml "+cmd+":", werr)
			return 1
		}
		fmt.Println("traktor-qml", cmd, "ok")
		return 0
	default:
		fmt.Fprintln(os.Stderr, "unknown traktor-qml command:", cmd)
		return 2
	}
}

// writeQMLResult records the elevated child's outcome ("" = success) for the caller to read.
func writeQMLResult(path string, err error) {
	if path == "" {
		return
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_ = os.WriteFile(path, []byte(msg), 0o644)
}

// runTraktorMap inspects the Traktor Controller Manager (devices in the target Settings.tsi).
func runTraktorMap(args []string) int {
	if len(args) == 0 || args[0] != "status" {
		fmt.Fprintln(os.Stderr, "usage: rave-mate traktor-map status")
		return 2
	}
	m := traktormap.New(logbus.New(1))
	names, err := m.DeviceNames()
	if err != nil {
		fmt.Fprintln(os.Stderr, "traktor-map:", err)
		return 1
	}
	if len(names) == 0 {
		fmt.Println("no Controller Manager devices found (or no Settings.tsi)")
		return 0
	}
	fmt.Println("Controller Manager devices:")
	for _, n := range names {
		fmt.Printf("  - %s\n", n)
	}
	return 0
}

// remotePprofArgs resolves the local output path (name in the ctl caller's cwd) and splits the
// optional args: an all-digit token is seconds, any other token is the nodeID.
func remotePprofArgs(args []string, name string) (path string, seconds int, nodeID string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", 0, "", err
	}
	for _, a := range args {
		if n, e := strconv.Atoi(a); e == nil {
			seconds = n
		} else {
			nodeID = a
		}
	}
	return filepath.Join(cwd, name), min(seconds, 60), nodeID, nil
}

// runCtl talks to a running instance over the control socket.
func runCtl(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rave-mate ctl <status|snapshot|logs|show|tab NAME|quit|\n"+
			"                       resize WxH|click LABEL|act ACT [VAL]|tap X Y|type TEXT|read LABEL|\n"+
			"                       set LABEL VALUE|screenshot PATH|screenshot-all DIR|screenshot-region PATH X Y W H|screenshot-vr PATH|\n"+
			"                       gio-snapshot [WINDOWID]|gio-tap WINDOWID CONTROLID|\n"+
			"                       sync-library|library-sync-status|sync-media [BUDGET]|media-sync-status|\n"+
			"                       sync-playlists|playlist-sync-status|cleanup-missing [dry]|\n"+
			"                       dmx-status|perf|pprof-cpu [SECONDS]|pprof-heap|goroutines|\n"+
			"                       tc-status|tc-start|tc-stop|ablelink-status|ablelink-resync|\n"+
			"                       encoder-scan|remote-encoder-scan [NODEID]|\n"+
			"                       remote-perf|remote-pprof-cpu [SECONDS] [NODEID]|remote-pprof-heap [NODEID]|remote-goroutines [NODEID]>")
		return 2
	}
	switch args[0] {
	case "status", "show", "quit", "sync-library", "library-sync-status", "media-sync-status",
		"sync-playlists", "playlist-sync-status", "libsync-status", "libsync-list", "libsync-auto-status", "vrperf", "obs-sync-status", "list-peers", "gpu-selftest",
		"tc-status", "tc-start", "tc-stop", "ablelink-status", "ablelink-resync":
		resp, err := app.Send(map[string]string{
			"status": "STATUS", "show": "SHOW", "quit": "QUIT",
			"sync-library": "SYNC-LIBRARY", "library-sync-status": "LIBRARY-SYNC-STATUS",
			"media-sync-status": "MEDIA-SYNC-STATUS",
			"sync-playlists":    "SYNC-PLAYLISTS", "playlist-sync-status": "PLAYLIST-SYNC-STATUS",
			"libsync-status": "LIBSYNC-STATUS", "libsync-list": "LIBSYNC-LIST",
			"libsync-auto-status": "LIBSYNC-AUTO-STATUS", "vrperf": "VRPERF", "obs-sync-status": "OBS-SYNC-STATUS", "list-peers": "LIST-PEERS",
			"gpu-selftest": "GPU-SELFTEST",
			"tc-status":    "TC-STATUS", "tc-start": "TC-START", "tc-stop": "TC-STOP",
			"ablelink-status": "ABLELINK-STATUS", "ablelink-resync": "ABLELINK-RESYNC",
		}[args[0]])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "dmx-status": // DMX plane: universes seen (pps, source IP) + grid sink state (multi-line)
		resp, err := app.SendMulti("DMX-STATUS")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "vrinput": // local VR input/binding diagnostic (multi-line - read to EOF)
		resp, err := app.SendMulti("VRINPUT")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "perf": // local perf-diagnosis report (multi-line; the daemon samples ~1s)
		resp, err := app.SendMulti("PERF")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "encoder-scan": // read-only: which encoders OBS/Parsec/GPU are using + the affinity plan preview
		resp, err := app.SendMulti("ENCODER-SCAN")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "pprof-cpu": // CPU-profile the daemon ([seconds], default 10, cap 60) → config-dir .pprof + top-15 summary
		secs := 10
		if len(args) > 1 {
			n, err := strconv.Atoi(args[1])
			if err != nil || n <= 0 {
				fmt.Fprintln(os.Stderr, "usage: rave-mate ctl pprof-cpu [seconds]")
				return 2
			}
			secs = min(n, 60)
		}
		// capture blocks for the window - read deadline must outlast it
		resp, err := app.SendMultiTimeout(fmt.Sprintf("PPROF-CPU %d", secs), time.Duration(secs)*time.Second+45*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "pprof-heap": // heap profile → config-dir .pprof + top allocation summary
		resp, err := app.SendMultiTimeout("PPROF-HEAP", 45*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "goroutines": // full goroutine dump (grouped stacks) inline - often enough to spot a hot loop
		resp, err := app.SendMulti("GOROUTINES")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-pprof-cpu": // a paired peer's CPU profile ([seconds] [nodeID]) → rave-mate_remote_cpu.pprof in CWD
		path, secs, nodeID, err := remotePprofArgs(args[1:], "rave-mate_remote_cpu.pprof")
		if err != nil {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl remote-pprof-cpu [seconds] [nodeID]")
			return 2
		}
		wire := "REMOTE-PPROF-CPU " + path
		if secs > 0 {
			wire += fmt.Sprintf(" %d", secs)
		} else {
			secs = 10
		}
		if nodeID != "" {
			wire += " " + nodeID
		}
		resp, err := app.SendMultiTimeout(wire, time.Duration(secs)*time.Second+60*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-pprof-heap": // a paired peer's heap profile ([nodeID]) → rave-mate_remote_heap.pprof in CWD
		path, _, nodeID, err := remotePprofArgs(args[1:], "rave-mate_remote_heap.pprof")
		if err != nil {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl remote-pprof-heap [nodeID]")
			return 2
		}
		wire := "REMOTE-PPROF-HEAP " + path
		if nodeID != "" {
			wire += " " + nodeID
		}
		resp, err := app.SendMultiTimeout(wire, 60*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-goroutines": // a paired peer's goroutine dump ([nodeID]), inline
		resp, err := app.SendMultiTimeout("REMOTE-GOROUTINES "+strings.Join(args[1:], " "), 30*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "sync-dj": // run a cross-DJ-software sync job by id; "dry" = preview only
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl sync-dj <jobID> [dry]")
			return 2
		}
		wire := "SYNC-DJ " + args[1]
		if len(args) > 2 && args[2] == "dry" {
			wire += " DRY"
		}
		resp, err := app.Send(wire)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "cleanup-missing": // remove missing-file tracks from DB + prune collection.nml; "dry" = report only
		wire := "CLEANUP-MISSING"
		if len(args) > 1 && args[1] == "dry" {
			wire = "CLEANUP-MISSING DRY"
		}
		resp, err := app.Send(wire)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "launch-group": // relaunch a crash-recovery app group by id
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl launch-group <groupID>")
			return 2
		}
		resp, err := app.Send("LAUNCH-GROUP " + args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "sync-media": // optional arg: waveform budget per run
		cmd := "SYNC-MEDIA"
		if len(args) > 1 {
			cmd += " " + args[1]
		}
		resp, err := app.Send(cmd)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "gio-snapshot": // Gio windows: no arg = list open windows; with ID = that window's control tree
		resp, err := app.SendMulti(strings.TrimSpace("GIO-SNAPSHOT " + strings.Join(args[1:], " ")))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "gio-tap": // synthesize a control activation in a Gio window
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl gio-tap <windowID> <controlID>")
			return 2
		}
		resp, err := app.Send("GIO-TAP " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "snapshot":
		resp, err := app.Snapshot()
		if err != nil && resp == "" {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Print(resp)
	case "resize":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl resize <w>x<h>  (e.g. 400x800)")
			return 2
		}
		resp, err := app.Send("RESIZE " + args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "click":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl click <label substring>")
			return 2
		}
		resp, err := app.Send("CLICK " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "act": // post a raw UI action through the page act pipeline (webview renderer)
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, `usage: rave-mate ctl act <act> [val]  (act with spaces: quote it - act '"ce-open:C:\My Music\a.flac"' [val]; \" = literal quote, other backslashes verbatim)`)
			return 2
		}
		resp, err := app.Send("ACT " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "tap":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl tap <x> <y>")
			return 2
		}
		resp, err := app.Send("TAP " + args[1] + " " + args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "tap2":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl tap2 <x> <y>  (right-click)")
			return 2
		}
		resp, err := app.Send("TAP2 " + args[1] + " " + args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "type":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl type <text...>")
			return 2
		}
		resp, err := app.Send("TYPE " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "read":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl read <label substring>")
			return 2
		}
		resp, err := app.Send("READ " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl set <label substring> <value...>")
			return 2
		}
		// The server splits the first token as label, rest as value, so spaces in
		// either side round-trip correctly.
		resp, err := app.Send("SET " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "screenshot":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl screenshot <path.png>")
			return 2
		}
		resp, err := app.Send("SCREENSHOT " + args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "screenshot-all": // sweep every tab (+scroll positions) → PNGs + report.txt (visual verification)
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl screenshot-all <dir>")
			return 2
		}
		// The sweep visits every tab + scroll positions - well past the 5s default deadline.
		resp, err := app.SendMultiTimeout("SCREENSHOT-ALL "+args[1], 5*time.Minute)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "screenshot-region":
		if len(args) < 6 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl screenshot-region <path.png> <x> <y> <w> <h>")
			return 2
		}
		resp, err := app.Send("SCREENSHOT-REGION " + args[1] + " " + strings.Join(args[2:6], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "screenshot-vr": // capture the SteamVR VR-View mirror window (opt-in: VROverlay.vrViewCapture)
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl screenshot-vr <path.png>")
			return 2
		}
		resp, err := app.Send("SCREENSHOT-VR " + args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-screenshot", "remote-screenshot-vr": // capture a paired peer's app window / VR-View to a local PNG
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl "+args[0]+" <path.png> [nodeID]")
			return 2
		}
		wire := "REMOTE-SHOT "
		if args[0] == "remote-screenshot-vr" {
			wire = "REMOTE-SHOT-VR "
		}
		wire += strings.Join(args[1:], " ")
		resp, err := app.Send(wire)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-ls": // list a paired peer's directory (debug: inspect its files)
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl remote-ls <path> [nodeID]")
			return 2
		}
		resp, err := app.SendMulti("REMOTE-LS " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "self-update": // trigger the LOCAL instance's self-update+relaunch (socket cmd existed; CLI verb was missing)
		resp, err := app.Send("SELF-UPDATE")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-update": // trigger a paired peer's self-update+relaunch (rave-mate app only; optional nodeID)
		resp, err := app.Send("REMOTE-UPDATE " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-vrinput": // a paired peer's VR input/binding diagnostic (optional nodeID; multi-line)
		resp, err := app.SendMulti("REMOTE-VRINPUT " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-perf": // a paired peer's perf-diagnosis report (optional nodeID; multi-line)
		resp, err := app.SendMulti("REMOTE-PERF " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-logs": // a paired peer's recent log tail ([nodeID] [filter-substring]; multi-line)
		resp, err := app.SendMulti("REMOTE-LOGS " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "remote-encoder-scan": // a paired peer's encoder-utilization scan + plan (optional nodeID; read-only, multi-line)
		resp, err := app.SendMulti("REMOTE-ENCODER-SCAN " + strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "tab":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: rave-mate ctl tab <name>")
			return 2
		}
		resp, err := app.Send("TAB " + args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
		fmt.Println(resp)
	case "logs":
		out := os.Stdout
		for _, a := range args[1:] {
			if a == "--stderr" {
				out = os.Stderr
			}
		}
		if err := app.StreamLogs(out); err != nil {
			fmt.Fprintln(os.Stderr, "ctl:", err)
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown ctl command:", args[0])
		return 2
	}
	return 0
}

// runServiceCmd handles install/uninstall/status. install/uninstall on Windows need an
// elevated prompt (SCM); the unix paths are user-scoped.
func runServiceCmd(cmd string) int {
	switch cmd {
	case "install":
		if err := service.InstallInteractive(); err != nil {
			fmt.Fprintln(os.Stderr, "install:", err)
			return 1
		}
		fmt.Println("rave-mate service installed.")
	case "uninstall":
		if err := service.UninstallInteractive(); err != nil {
			fmt.Fprintln(os.Stderr, "uninstall:", err)
			return 1
		}
		fmt.Println("rave-mate service uninstalled.")
	case "status":
		st, err := service.Status()
		if err != nil {
			fmt.Fprintln(os.Stderr, "status:", err)
			return 1
		}
		fmt.Println("rave-mate service:", st)
	}
	return 0
}
