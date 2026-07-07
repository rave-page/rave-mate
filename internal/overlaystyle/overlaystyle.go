// Package overlaystyle loads the shared overlay appearance (overlay-style.json) that the browser
// overlay's edit panel writes, so the NATIVE renderers (Spout/Syphon/PipeWire + PNG) honour the
// same colours, gradients, per-band EQ colours and card borders - one source of truth, edited live
// in the browser, applied everywhere. The schema mirrors the browser's `style` object; every field
// is optional (zero → the renderer's built-in default). Back-compat: the old flat waveColor/
// waveBgColor keys are read when the newer waveFill/waveBg objects are absent.
package overlaystyle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Stop is one gradient colour stop (position 0..1, #rrggbb).
type Stop struct {
	P float64 `json:"p"`
	C string  `json:"c"`
}

// Gradient is a multi-stop linear/radial gradient (angle in degrees: 0=left→right, 90=top→bottom).
type Gradient struct {
	Kind  string `json:"kind"` // "linear" | "radial"
	Angle float64
	Stops []Stop `json:"stops"`
}

// UnmarshalJSON tolerates the angle being absent.
func (g *Gradient) UnmarshalJSON(b []byte) error {
	var raw struct {
		Kind  string  `json:"kind"`
		Angle float64 `json:"angle"`
		Stops []Stop  `json:"stops"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	g.Kind, g.Angle, g.Stops = raw.Kind, raw.Angle, raw.Stops
	return nil
}

// Fill is a solid colour or a gradient.
type Fill struct {
	Type  string    `json:"type"` // "solid" | "gradient"
	Color string    `json:"color"`
	Grad  *Gradient `json:"grad"`
}

// IsGradient reports whether this fill should render as a gradient.
func (f *Fill) IsGradient() bool {
	return f != nil && f.Type == "gradient" && f.Grad != nil && len(f.Grad.Stops) > 0
}

// EQColors are per-band EQ-curve colours + per-direction filter-cut colours.
type EQColors struct {
	Low  string `json:"low"`
	Mid  string `json:"mid"`
	High string `json:"high"`
	HP   string `json:"hp"`
	LP   string `json:"lp"`
}

// Card holds deck-card border + radius styling.
type Card struct {
	Border      string   `json:"border"` // "none" | "solid" | "glow"
	BorderW     float64  `json:"borderW"`
	BorderColor string   `json:"borderColor"`
	Radius      *float64 `json:"radius"`
}

// Style is the full overlay appearance (subset the native renderer uses).
type Style struct {
	ZoomSeconds float64 `json:"zoomSeconds"`
	PlayheadPct float64 `json:"playheadPct"`

	WaveOpacity   *float64 `json:"waveOpacity"`
	WaveBgOpacity *float64 `json:"waveBgOpacity"`
	WaveDim       *float64 `json:"waveDim"`
	WaveColor     string   `json:"waveColor"`   // legacy flat fallback
	WaveBgColor   string   `json:"waveBgColor"` // legacy flat fallback

	WaveFill *Fill     `json:"waveFill"`
	WaveBg   *Fill     `json:"waveBg"`
	EQColors *EQColors `json:"eqColors"`
	Card     *Card     `json:"card"`

	// CardFaderReact: when true, a deck card's overall opacity follows its channel fader (fades out
	// as the fader is pulled down) - only when fader data is available. nil/false → cards stay at
	// full opacity (the right default when there's no controller feeding fader). Editable in the
	// browser overlay edit panel + Settings.
	CardFaderReact *bool `json:"cardFaderReact"`
}

// WaveFillSpec resolves the waveform fill (new waveFill, else legacy flat waveColor, else def).
func (s Style) WaveFillSpec(def string) Fill {
	if s.WaveFill != nil {
		return *s.WaveFill
	}
	if s.WaveColor != "" {
		return Fill{Type: "solid", Color: s.WaveColor}
	}
	return Fill{Type: "solid", Color: def}
}

// BgFillSpec resolves the background fill the same way.
func (s Style) BgFillSpec(def string) Fill {
	if s.WaveBg != nil {
		return *s.WaveBg
	}
	if s.WaveBgColor != "" {
		return Fill{Type: "solid", Color: s.WaveBgColor}
	}
	return Fill{Type: "solid", Color: def}
}

// Load reads + parses overlay-style.json. A missing/empty/invalid file yields a zero Style (all
// defaults) with a nil error for the common missing-file case.
func Load(path string) (Style, error) {
	var st Style
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	if len(b) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	return st, nil
}

// GetBool reads a top-level boolean key from overlay-style.json (def if absent/unreadable).
func GetBool(path, key string, def bool) bool {
	m := rawMap(path)
	if raw, ok := m[key]; ok {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			return v
		}
	}
	return def
}

// SetBool surgically sets a top-level boolean key, PRESERVING every other (browser-owned) key in
// overlay-style.json - the browser owns the schema, so we never re-marshal through the typed Style.
func SetBool(path, key string, val bool) error {
	m := rawMap(path)
	b, _ := json.Marshal(val)
	m[key] = b
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// Push POSTs overlay-style.json to a running overlay server's /style so live SSE clients (the
// browser overlay) apply changes made OUTSIDE the browser editor (e.g. the Settings toggle). The
// server re-broadcasts + rewrites the file. Best-effort: a disabled/closed server is a no-op since
// the caller already persisted the change to disk.
func Push(port int, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/style", port), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// rawMap reads overlay-style.json as a raw key→value map (empty map on any failure).
func rawMap(path string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// Watcher caches a Style, re-reading only when the file's mtime/size changes - cheap to call every
// render frame. Not safe for concurrent use; each sink owns one.
type Watcher struct {
	path  string
	mu    sync.Mutex
	cur   Style
	mtime time.Time
	size  int64
	read  bool
}

// NewWatcher builds a watcher for the given overlay-style.json path.
func NewWatcher(path string) *Watcher { return &Watcher{path: path} }

// Get returns the current style, reloading if the file changed since the last call.
func (w *Watcher) Get() Style {
	w.mu.Lock()
	defer w.mu.Unlock()
	fi, err := os.Stat(w.path)
	if err != nil {
		if !w.read { // never read + no file → defaults
			w.read = true
		}
		return w.cur
	}
	if w.read && fi.ModTime().Equal(w.mtime) && fi.Size() == w.size {
		return w.cur
	}
	if st, err := Load(w.path); err == nil {
		w.cur = st
	}
	w.mtime, w.size, w.read = fi.ModTime(), fi.Size(), true
	return w.cur
}
