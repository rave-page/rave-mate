package webui

// PSH1 - the procShell wire (phase B5). One `shell` implementation (shell_proc.go) speaks it to a
// supervised child (`rave-mate feature webview`, shell_proc_child.go) that owns the WebView2 window.
// Framing + supervision are featurehost's (duplex newline-JSON over the child's stdio: method!=""
// = request, event!="" = fire-and-forget event, else response); PSH1 is the message set on top.
// B6 swaps the Go child for a Zig exe behind THIS contract, so the spec - not the code - is the
// deliverable: .devnotes/ZIG_UI_GUIDE.md "Phase B - B5 procShell protocol".
//
// Two lanes, one pipe:
//   ORDERED (doc, eval)  FIFO end-to-end, seq-numbered, bounded (procOrdQueueCap, drop-oldest +
//                        cache wipe). Ack is IN-BAND: the daemon appends __rave_evalResult to each
//                        batch (ui.go dispatchEvals) and holds until it returns, so at most ONE
//                        un-acked batch is in flight - the in-proc bound, unchanged.
//   DIRECT  (xeval, act, resize, show, quit, streaming, screenshot)  request/response or
//                        fire-and-forget, NEVER queued behind the ordered lane (the writer drains it
//                        first) - ctl's evalValue would otherwise deadlock behind a flooded batch
//                        stream.
//
// The round-trip id is carried INSIDE the script by the __rave_evalResult(id,…) call the daemon
// wraps around it; the child never parses a script - it forwards every binding invocation verbatim
// as an evalres event. Ordered-lane acks and ctl results therefore share ONE mechanism, identical
// to the in-proc shell.

const procFeatureName = "webview"

// Parent → child events.
const (
	procEvDoc    = "doc"        // ordered: load a full document (procDoc)
	procEvEval   = "eval"       // ordered: run a batched page script (procEval)
	procEvXEval  = "xeval"      // direct: run a ctl round-trip script (procXEval)
	procEvAct    = "act"        // direct: replay a Go-originated act payload on the child's act worker
	procEvResize = "resize"     // direct: resize the window viewport (procResize)
	procEvShow   = "show"       // direct: raise + foreground the window
	procEvQuit   = "quit"       // direct: close the window, then exit (procQuit)
	procEvStream = "streaming"  // direct: governor streaming signal (procStream)
	procEvShot   = "screenshot" // direct: capture the window to a path (procShot) → procEvShotRes
	// procEvSurfaceTest toggles the CHILD's native-surface test hole (ctl surface-test). Pure
	// command plumbing: one bool crosses, and nothing else ever does - the element, its rect, its
	// colour and its lifetime are the child's (SDL_WEBVIEW_SURFACE_DESIGN §1 directive #2, §4.3).
	procEvSurfaceTest = "surfacetest" // direct
)

// Child → parent events.
const (
	procEvReady   = "ready"   // window created; carries the native handle (procReady)
	procEvEvalRes = "evalres" // page called __rave_evalResult (procEvalRes) - ack AND ctl result
	procEvAction  = "action"  // page act payload (window.rave) - drained off the child's UI thread
	procEvWin     = "win"     // window state changed (procWin) → daemon governor + eval gate
	procEvGone    = "gone"    // message loop returned / window destroyed
	procEvShotRes = "shotres" // screenshot finished (procShotRes) - the PNG is on disk, not on the wire
)

// procInit is the featurehost init payload. Everything the child needs arrives here: it reads NO
// config, opens NO database, holds NO identity - it is a pure view + input transport.
type procInit struct {
	Title       string `json:"title"`
	W           int    `json:"w"`
	H           int    `json:"h"`
	StartHidden bool   `json:"startHidden"`
	AllowGPU    bool   `json:"allowGpu"` // false = WebView2 GPU compositing off (good-neighbour default)
	DataDir     string `json:"dataDir"`  // resolved WebView2 profile dir (the child must not read config)
	// ShellHosting is the WebView2 hosting mode the Zig child should use: ""/"windowed" (default,
	// a child HWND) or "visual" (DirectComposition visual hosting). The child falls back to
	// windowed by itself if composition bring-up fails, so this is a request, not a guarantee.
	ShellHosting string `json:"shellHosting,omitempty"`
	// RuntimeJS is the transport+introspection runtime injected at document start. It travels on the
	// wire (not read from the child's own binary) because it is byte-contracted with Go-generated SVG
	// ids and data-mse attributes - B6's Zig child gets the same bytes the daemon rendered against.
	RuntimeJS string `json:"runtimeJs"`
	// InitialHTML is the document to load on create. Re-evaluated per (re)spawn, so a restarted
	// child comes up on the CURRENT document; the daemon still follows with a full doc + patch pass.
	InitialHTML string `json:"initialHtml"`
	// MediaOrigin/MediaSession identify the daemon's loopback media endpoint the page may fetch
	// (mediahttp.go). Passed explicitly so the child's origin is a stated fact, not an inference;
	// MediaSession is the per-shell-session path segment every /m//mi//img/ URL carries.
	MediaOrigin  string `json:"mediaOrigin"`
	MediaSession string `json:"mediaSession"`
	Streaming    bool   `json:"streaming"` // governor: a stream is live (child right-sizes its own priority)
	// Virtual runs the loopback page model instead of WebView2: no window, no cgo, scripted
	// __rave_evalResult answers. Test transport fixture ONLY - never a production shell.
	Virtual bool `json:"virtual"`
}

// procDoc is one ordered-lane full-document load.
type procDoc struct {
	Seq  uint64 `json:"seq"`
	HTML string `json:"html"`
}

// procEval is one ordered-lane batched script (already coalesced by the daemon's eval queue).
type procEval struct {
	Seq uint64 `json:"seq"`
	JS  string `json:"js"`
}

// procXEval is one direct-lane script (ctl round-trip; its result id rides inside JS).
type procXEval struct {
	JS string `json:"js"`
}

// procAct is one Go-originated act payload (MIDI-mapped input, ctl ACT).
type procAct struct {
	Payload string `json:"payload"`
}

type procResize struct {
	W int `json:"w"`
	H int `json:"h"`
}

// procQuit closes the window; the child force-exits after GraceMS if its loop does not unwind.
type procQuit struct {
	GraceMS int `json:"graceMs"`
}

type procStream struct {
	On bool `json:"on"`
}

// procSurfaceTest is the whole surface-test wire: a bool. No id, no rect, no colour.
type procSurfaceTest struct {
	On bool `json:"on"`
}

// procShot asks the child to capture ITS OWN window and write the PNG to Path. Only the request and
// the rect cross the pipe - never pixels: a 1280x820 PNG is hundreds of KB, and base64 in a
// newline-JSON frame would put that on the ordered stream's pipe for every ctl screenshot. The daemon
// picks Path (it is the operator's ctl destination), so there is no temp file to hand back and clean
// up either. Rect in device px; W<=0||H<=0 = the whole window.
type procShot struct {
	RID  string `json:"rid"`
	Path string `json:"path"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

// procShotRes reports one capture's outcome. Err "" = the PNG is at the requested path.
type procShotRes struct {
	RID string `json:"rid"`
	Err string `json:"err,omitempty"`
}

// procReady is emitted once per child session, after the window exists.
type procReady struct {
	HWND    uint64 `json:"hwnd"`
	Virtual bool   `json:"virtual"`
}

// procEvalRes is one __rave_evalResult invocation, forwarded verbatim.
type procEvalRes struct {
	ID     string `json:"id"`
	Result string `json:"result"`
}

// procWin mirrors the window-state signals the in-proc subclass fed the governor directly.
type procWin struct {
	Focused   bool `json:"focused"`
	Minimized bool `json:"minimized"`
	SizeMove  bool `json:"sizeMove"`
	Hidden    bool `json:"hidden"` // close-to-tray happened (log parity with onWindowHidden)
}
