package visualeditor

import (
	"image/color"
	"testing"
)

func TestBuiltinTemplatesRenderable(t *testing.T) {
	c := NewCompositor(nil, nil)
	for _, tpl := range BuiltinTemplates() {
		if tpl.Layer == nil || tpl.Layer.Kind != KindGroup {
			t.Fatalf("template %q not a group", tpl.Name)
		}
		if !tpl.Builtin || tpl.W != presetW || tpl.H != presetH {
			t.Fatalf("template %q missing builtin/canvas stamp", tpl.Name)
		}
		d := NewDocument(tpl.W, tpl.H)
		d.Root.Children = append(d.Root.Children, tpl.Instantiate())
		img := c.Render(d, fakeProvider{"track.title": "T", "track.artist": "A"})
		if img.Bounds().Dx() != tpl.W {
			t.Fatalf("render size mismatch for %q", tpl.Name)
		}
	}
}

func TestInstantiateFreshIDs(t *testing.T) {
	tpl := lowerThird()
	a := tpl.Instantiate()
	b := tpl.Instantiate()
	if a.ID == b.ID {
		t.Fatal("instances share group id")
	}
	if a.Children[0].ID == b.Children[0].ID {
		t.Fatal("instances share child id")
	}
}

func TestTemplateStoreRoundTrip(t *testing.T) {
	store := NewTemplateStore(t.TempDir())
	g := NewGroup("My preset")
	g.Children = append(g.Children, NewSolid("bar", 0, 0, 100, 50, color.NRGBA{1, 2, 3, 255}))
	if err := store.Save("My preset", g, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	users := store.UserTemplates()
	if len(users) != 1 || users[0].Name != "My preset" || users[0].W != 1920 {
		t.Fatalf("user template not persisted: %+v", users)
	}
	if len(users[0].Layer.Children) != 1 {
		t.Fatalf("template layer tree lost")
	}
	// All = builtins + user.
	all := store.All()
	if len(all) != len(BuiltinTemplates())+1 {
		t.Fatalf("All() count = %d", len(all))
	}
}

func TestSaveWrapsBareLeaf(t *testing.T) {
	store := NewTemplateStore(t.TempDir())
	leaf := NewText("cap", 0, 0, 100, 40, "hi", "Orbitron", 20, color.NRGBA{255, 255, 255, 255})
	if err := store.Save("cap", leaf, 100, 100); err != nil {
		t.Fatal(err)
	}
	users := store.UserTemplates()
	if len(users) != 1 || users[0].Layer.Kind != KindGroup {
		t.Fatalf("bare leaf not wrapped in a group: %+v", users)
	}
}
