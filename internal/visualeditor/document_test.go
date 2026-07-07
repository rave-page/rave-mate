package visualeditor

import (
	"image/color"
	"testing"
)

func TestDocumentRoundTrip(t *testing.T) {
	d := NewDocument(1080, 1350)
	d.Vars = map[string]string{"venue": "Club X"}
	g := NewGroup("Lower third")
	g.Opacity = 0.9
	g.Blend = BlendScreen
	g.Children = append(g.Children,
		NewSolid("bar", 0, 1200, 1080, 150, color.NRGBA{0, 0, 0, 180}),
		NewText("title", 40, 1220, 1000, 100, "{track.title}", "Orbitron Bold", 48, color.NRGBA{250, 250, 250, 255}),
	)
	d.Root.Children = append(d.Root.Children, g)

	data, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.W != 1080 || got.H != 1350 || got.Schema != SchemaVersion {
		t.Fatalf("header mismatch: %+v", got)
	}
	if got.Vars["venue"] != "Club X" {
		t.Fatalf("vars lost: %v", got.Vars)
	}
	if len(got.Root.Children) != 1 || len(got.Root.Children[0].Children) != 2 {
		t.Fatalf("tree shape lost")
	}
	gg := got.Root.Children[0]
	if gg.Blend != BlendScreen || gg.Opacity != 0.9 {
		t.Fatalf("group props lost: %+v", gg)
	}
	txt := gg.Children[1]
	if txt.Text == nil || txt.Text.Content != "{track.title}" || txt.Text.FontSize != 48 {
		t.Fatalf("text props lost: %+v", txt.Text)
	}
}

func TestUnmarshalRejectsNewerSchema(t *testing.T) {
	_, err := Unmarshal([]byte(`{"schema":999,"w":10,"h":10,"root":{"kind":"group"}}`))
	if err == nil {
		t.Fatal("expected schema-too-new error")
	}
}

func TestUnmarshalNormalizesDefaults(t *testing.T) {
	// Missing blend + zero scale should normalize to normal + scale 1.
	got, err := Unmarshal([]byte(`{"schema":1,"w":10,"h":10,"root":{"kind":"group","children":[{"kind":"solid","w":5,"h":5}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	c := got.Root.Children[0]
	if c.Blend != BlendNormal {
		t.Fatalf("blend not normalized: %q", c.Blend)
	}
	if c.Transform.ScaleX != 1 || c.Transform.ScaleY != 1 {
		t.Fatalf("scale not normalized: %+v", c.Transform)
	}
	if c.ID == "" {
		t.Fatal("id not assigned")
	}
}

func TestCloneIsDeepWithFreshIDs(t *testing.T) {
	d := NewDocument(10, 10)
	orig := NewGroup("g")
	orig.Children = append(orig.Children, NewSolid("s", 0, 0, 5, 5, color.NRGBA{1, 2, 3, 255}))
	d.Root.Children = append(d.Root.Children, orig)

	clone := orig.Clone()
	if clone.ID == orig.ID {
		t.Fatal("clone kept the same id")
	}
	if clone.Children[0].ID == orig.Children[0].ID {
		t.Fatal("clone child kept the same id")
	}
	// Mutating the clone must not touch the original.
	clone.Children[0].Solid.Color = RGBA{9, 9, 9, 255}
	if orig.Children[0].Solid.Color.R == 9 {
		t.Fatal("clone shares payload pointer with original")
	}
}

func TestFind(t *testing.T) {
	d := NewDocument(10, 10)
	g := NewGroup("g")
	leaf := NewSolid("s", 0, 0, 5, 5, color.NRGBA{})
	g.Children = append(g.Children, leaf)
	d.Root.Children = append(d.Root.Children, g)

	found, parent := d.Find(leaf.ID)
	if found != leaf || parent != g {
		t.Fatalf("find leaf: got layer=%v parent=%v", found, parent)
	}
	if _, _ = d.Find("nope"); found == nil {
		t.Fatal("sanity")
	}
}
