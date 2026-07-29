package remotectl

// Method names mirror the Local Studio registry namespaces (localMedia.*, automations.*) so
// peer control is the same logic system as browser control. library.* + the schedules/runs
// automations verbs extend the scheme for the native manager surface (which studio doesn't
// expose). Param/result shapes are the studio/web shapes where one already exists
// (localMedia.listDirectory ⇒ localmedia.Listing, automations.* ⇒ automation.* structs).
const (
	// localMedia - streamed remote file browse (controller renders its own picker; the
	// controlled machine never pops a native dialog) + file ops so a paired controller has
	// Library parity (rename/move/duplicate/delete on the controlled machine's filesystem).
	MethodListDirectory = "localMedia.listDirectory"
	MethodGetDefaults   = "localMedia.getDefaults"
	MethodFileRename    = "localMedia.rename"
	MethodFileMove      = "localMedia.move"
	MethodFileDuplicate = "localMedia.duplicate"
	MethodFileDelete    = "localMedia.delete"

	// automations - list/CRUD/run over the peer's automation.Manager.
	MethodAutoList           = "automations.list"
	MethodAutoSchedules      = "automations.listSchedules"
	MethodAutoRuns           = "automations.runs"
	MethodAutoSave           = "automations.save" // empty id ⇒ create
	MethodAutoDelete         = "automations.delete"
	MethodAutoSetEnabled     = "automations.setEnabled"
	MethodAutoRunManual      = "automations.runManual"
	MethodAutoSaveSchedule   = "automations.saveSchedule"
	MethodAutoDeleteSchedule = "automations.deleteSchedule"

	// library - browse + tag-edit the peer's persisted collection.
	MethodLibInfo       = "library.info"
	MethodLibTracks     = "library.tracks"
	MethodLibWriteTags  = "library.writeTags"
	MethodLibRevertTags = "library.revertTags"

	// library cue editing - a controller edits a peer's track cues/beatgrid/drops LOCALLY
	// (it pulls the audio via fileChunk, renders its own waveform) and writes the result
	// back. trackDetail carries a StateSHA (see CueStateSHA) as the optimistic-concurrency
	// baseline: writeCueData refuses (Conflict + fresh detail) when the peer's state moved
	// under the controller, unless Force. cueWriteTargets/writeCuesTo route the result into
	// the PEER's installed DJ software (cuewriteback, backup-first). playlistTracks resolves
	// a peer playlist to paths for whole-set cue prep.
	MethodLibTrackDetail    = "library.trackDetail"
	MethodLibFileChunk      = "library.fileChunk"
	MethodLibWriteCueData   = "library.writeCueData"
	MethodLibCueTargets     = "library.cueWriteTargets"
	MethodLibWriteCuesTo    = "library.writeCuesTo"
	MethodLibPlaylistTracks = "library.playlistTracks"

	// recorder - drive the peer's recording/publish cockpit: list its recorded sets (summaries,
	// paged), page one set's tracklist, list its captured audio/video files, export a tracklist,
	// rename or delete a finished set. Read + rename/export/delete only; no live start/finish over
	// the link. Sets and tracklists page like library.tracks so a monster set stays under the
	// control-frame cap.
	MethodRecList      = "recorder.listSets"
	MethodRecTracklist = "recorder.tracklist"
	MethodRecCaptures  = "recorder.captures"
	MethodRecExport    = "recorder.export"
	MethodRecRename    = "recorder.rename"
	MethodRecDelete    = "recorder.delete"
	MethodRecMatch     = "recorder.matchHistory" // reconcile a finished set against the peer's Traktor history

	// media - transcode a file on the controlled machine via its worker pool.
	MethodMediaTranscode = "media.transcode"

	// screenshot - capture the controlled machine's app window / SteamVR VR-View; PNG returned
	// base64 in the JSON result (remotectl frames are JSON - no separate binary channel).
	// media.frameShot - sample a LOCAL video-share sender's content ON THE PEER and return the
	// verdict + the last frame. Read-only, and the point of it: the peer forms the "is this picture
	// moving" judgement at the ORIGIN, so no physical access to the sending machine is needed.
	MethodFrameShot = "media.frameShot"

	// media.testcard - drive the peer's deterministic diagnostic source (start/stop/stats/reset).
	// start/stop DO mutate the peer (they open/close a Spout sender named "rave-mate testcard"),
	// which is the point: the generator has to run on the SENDING machine of the chain under test.
	MethodTestcard = "media.testcard"

	MethodScreenshotApp = "screenshot.app"
	MethodScreenshotVR  = "screenshot.vr"

	// motion - replicate Motion Studio recordings (<dir>/*.json) to paired peers. Read-only pull:
	// list returns name+size+sha256 for diffing; get returns one recording's JSON base64-in-result
	// (frames are JSON). Recordings are small (single-digit MB keyframe JSON) - one frame each.
	MethodMotionList = "motion.list"
	MethodMotionGet  = "motion.get"

	// vrm - replicate VRChat/VRM avatar models (<dir>/*.vrm|glb|gltf) to paired peers. Files are large
	// (15–60+ MB), so get transfers in CHUNKS: whole-file base64 would blow the 24 MiB control frame.
	// list returns name(with ext)+size+sha256 for diffing.
	MethodVRMList     = "vrm.list"
	MethodVRMGetChunk = "vrm.getChunk"

	// vr.inputdiag - dump the controlled machine's SteamVR Input action state (bound origins + live
	// state) as text, for debugging why a binding does nothing.
	MethodVRInputDiag = "vr.inputdiag"

	// app.selfupdate - trigger the controlled machine's self-updater (download+apply+relaunch). Used
	// to update a headset PC remotely the moment a CI build lands.
	MethodSelfUpdate = "app.selfupdate"

	// app.perf - the controlled machine's perf-diagnosis report (build stamp, cpu/rss/goroutine/heap
	// rings, GC, system CPU + top processes, feature children, probe sections, recent warn/error
	// counts) as text. Answers "is that box slow because of rave-mate or something else".
	MethodAppPerf = "app.perf"

	// app.pprof* - profile-grade attribution when app.perf shows churn nothing instruments: the
	// controlled machine captures a runtime/pprof CPU/heap profile and returns the raw bytes
	// base64 (frames are JSON; profiles are small - far under maxControlFrame). The CPU handler
	// BLOCKS for the capture window, so seconds are clamped to fit inside serveTimeout.
	// app.goroutines returns the grouped goroutine dump (debug=1) as text - often enough to spot
	// a hot loop without a full profile.
	MethodAppPprofCPU   = "app.pprofcpu"
	MethodAppPprofHeap  = "app.pprofheap"
	MethodAppGoroutines = "app.goroutines"

	// app.logs - the controlled machine's recent log tail (the same ring `ctl logs` streams, formatted
	// identically), optionally substring-filtered. Lets a paired controller read a headset/desk peer's
	// diagnostics (e.g. media receive-sink telemetry) without physical access or a screen.
	MethodAppLogs = "app.logs"

	// app.encoderscan - the controlled machine's live encoder-utilization scan + placement plan
	// (which encoders OBS/Parsec/etc are using, per-adapter encode %, CPU, and the app-agnostic
	// headroom plan) as text. Read-only: samples PDH GPU-engine counters, touches no GPU encode.
	// Lets the desk instance see the VR PC's encoder headroom before launching a peer-link stream.
	MethodAppEncoderScan = "app.encoderscan"

	// vrchat.* - VRChat-link federation: ONE paired instance holds the VRChat session; every
	// other peer reads friends/groups/roles/members through it as if linked locally (Worlds
	// pickers + publish-time group-role expansion). READ-ONLY by design - the session cookie
	// never crosses the link, only query results over the MAC'd pair. status answers on every
	// peer (linked=false when no session) so controllers can discover who serves the data.
	MethodVrcStatus       = "vrchat.status"
	MethodVrcFriends      = "vrchat.friends"
	MethodVrcUserGroups   = "vrchat.userGroups"
	MethodVrcSearchGroups = "vrchat.searchGroups"
	MethodVrcGroupRoles   = "vrchat.groupRoles"
	MethodVrcGroupMembers = "vrchat.groupMembers"
	// vrchat.proxy - FULL federation tunnel: the serving instance executes one
	// VRChat API call with ITS session and returns status+body, so an unlinked
	// peer's vrchat.Manager works as if logged in locally (all tabs/features).
	// Server-side validation: GET/POST/PUT/DELETE only; API-relative paths only;
	// /auth* + /logout refused (except GET /auth/user, the pure session read) so
	// a peer can never re-auth, verify 2FA, or kill the serving session.
	MethodVrcProxy = "vrchat.proxy"
)
