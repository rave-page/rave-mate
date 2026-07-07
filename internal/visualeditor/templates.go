package visualeditor

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Template is a reusable, insertable composition fragment: a named Group authored on a
// W×H canvas. Insert deep-copies its layer into a document (fresh IDs). A whole document
// is saved as a template by wrapping its root group.
type Template struct {
	Schema  int    `json:"schema"`
	Name    string `json:"name"`
	W       int    `json:"w"` // authoring canvas (thumbnail + placement reference)
	H       int    `json:"h"`
	Builtin bool   `json:"-"`
	Layer   *Layer `json:"layer"`
}

// MarshalTemplate serializes t to indented JSON (stamps schema).
func MarshalTemplate(t Template) ([]byte, error) {
	t.Schema = SchemaVersion
	return json.MarshalIndent(t, "", "  ")
}

// UnmarshalTemplate parses + normalizes a template.
func UnmarshalTemplate(data []byte) (Template, error) {
	var t Template
	if err := json.Unmarshal(data, &t); err != nil {
		return Template{}, err
	}
	if t.Schema > SchemaVersion {
		return Template{}, fmt.Errorf("visualeditor: template schema %d newer than supported %d", t.Schema, SchemaVersion)
	}
	if t.Layer == nil {
		return Template{}, fmt.Errorf("visualeditor: template %q has no layer", t.Name)
	}
	if t.Layer.Kind != KindGroup { // wrap a bare leaf so insert is uniform
		g := NewGroup(t.Name)
		g.Children = []*Layer{t.Layer}
		t.Layer = g
	}
	normalize(t.Layer)
	return t, nil
}

// Instantiate returns a fresh deep copy of the template's group (new IDs) ready to append
// to a document's root children.
func (t Template) Instantiate() *Layer { return t.Layer.Clone() }

// TemplateStore lists + persists user templates in a directory, merged with the built-ins.
type TemplateStore struct{ Dir string }

// NewTemplateStore returns a store rooted at dir (created on first Save).
func NewTemplateStore(dir string) *TemplateStore { return &TemplateStore{Dir: dir} }

// safeName maps a template name to a filesystem-safe base filename.
func safeName(name string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_")
	s := strings.TrimSpace(r.Replace(name))
	if s == "" {
		s = "template"
	}
	return s
}

// Save writes a template (name + group + canvas) to Dir/<safeName>.json.
func (s *TemplateStore) Save(name string, layer *Layer, w, h int) error {
	if s.Dir == "" {
		return fmt.Errorf("visualeditor: no template dir")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	g := layer
	if g == nil || g.Kind != KindGroup {
		wrap := NewGroup(name)
		if layer != nil {
			wrap.Children = []*Layer{layer}
		}
		g = wrap
	}
	data, err := MarshalTemplate(Template{Name: name, W: w, H: h, Layer: g.Clone()})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, safeName(name)+".json"), data, 0o644)
}

// UserTemplates loads every *.json in Dir (skips unreadable/invalid files).
func (s *TemplateStore) UserTemplates() []Template {
	if s.Dir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	var out []Template
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			continue
		}
		t, err := UnmarshalTemplate(data)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// All returns built-ins followed by user templates.
func (s *TemplateStore) All() []Template {
	return append(BuiltinTemplates(), s.UserTemplates()...)
}

// ── built-in text-layout presets ──────────────────────────────────────────────
// Authored on a 1920×1080 canvas; scale on insert as needed.

const (
	presetW = 1920
	presetH = 1080
)

var (
	white = color.NRGBA{R: 0xfa, G: 0xfa, B: 0xfa, A: 0xff}
	muted = color.NRGBA{R: 0xa6, G: 0xab, B: 0xb6, A: 0xff}
	brand = color.NRGBA{R: 0xF7, G: 0x08, B: 0x64, A: 0xff}
	scrim = color.NRGBA{R: 0x0a, G: 0x0a, B: 0x0a, A: 0xc8}
)

// BuiltinTemplates returns the shipped text-layout presets (fresh instances each call).
func BuiltinTemplates() []Template {
	return []Template{
		lowerThird(), centeredTitle(), cornerCaption(), tickerBar(),
	}
}

func mark(t Template) Template { t.Builtin = true; t.W, t.H = presetW, presetH; return t }

// lowerThird: scrim bar bottom-left with a brand accent, title + subtitle placeholders.
func lowerThird() Template {
	g := NewGroup("Lower third")
	g.Children = []*Layer{
		NewSolid("Scrim", 0, 820, 1100, 200, scrim),
		NewSolid("Accent", 60, 852, 8, 136, brand),
		NewText("Title", 92, 856, 980, 70, "{track.title}", "Orbitron Bold", 54, white),
		NewText("Subtitle", 92, 934, 980, 50, "{track.artist}", "Orbitron", 32, muted),
	}
	return mark(Template{Name: "Lower third", Layer: g})
}

// centeredTitle: big centered title + subtitle over a full-frame scrim.
func centeredTitle() Template {
	g := NewGroup("Centered title")
	title := NewText("Title", 160, 430, 1600, 130, "{track.title}", "Orbitron Bold", 96, white)
	title.Text.Align = AlignCenter
	sub := NewText("Subtitle", 160, 580, 1600, 70, "{track.artist}", "Orbitron", 40, muted)
	sub.Text.Align = AlignCenter
	g.Children = []*Layer{
		NewSolid("Scrim", 0, 0, 1920, 1080, color.NRGBA{0, 0, 0, 0x66}),
		title, sub,
	}
	return mark(Template{Name: "Centered title", Layer: g})
}

// cornerCaption: compact top-right caption chip.
func cornerCaption() Template {
	g := NewGroup("Corner caption")
	txt := NewText("Caption", 1360, 56, 500, 44, "{track.title}", "Orbitron", 30, white)
	txt.Text.Align = AlignRight
	g.Children = []*Layer{
		NewSolid("Chip", 1330, 44, 540, 70, scrim),
		txt,
	}
	return mark(Template{Name: "Corner caption", Layer: g})
}

// tickerBar: full-width bottom bar with a gradient + running caption.
func tickerBar() Template {
	g := NewGroup("Ticker bar")
	stops := []GradientStop{
		{Pos: 0, Color: FromNRGBA(color.NRGBA{0x0a, 0x0a, 0x0a, 0xf0})},
		{Pos: 1, Color: FromNRGBA(color.NRGBA{0xA1, 0x13, 0x8E, 0xf0})},
	}
	g.Children = []*Layer{
		NewGradient("Bar", 0, 1000, 1920, 80, 0, stops),
		NewText("Now playing", 40, 1018, 1840, 44, "NOW PLAYING  -  {track.artist} · {track.title}", "Orbitron", 30, white),
	}
	return mark(Template{Name: "Ticker bar", Layer: g})
}
