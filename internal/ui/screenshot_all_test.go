package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Live":       "live",
		"App Groups": "app-groups",
		"VRChat":     "vrchat",
		"  Worlds  ": "worlds",
		"a/b (c) d":  "a-b-c-d",
		"---":        "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBiggestScroll(t *testing.T) {
	small := container.NewVScroll(widget.NewLabel("s"))
	small.Resize(fyne.NewSize(10, 10))
	big := container.NewVScroll(widget.NewLabel("b"))
	big.Resize(fyne.NewSize(300, 200))
	root := container.NewVBox(widget.NewLabel("head"), small, container.NewVBox(big))
	if got := biggestScroll(root); got != big {
		t.Fatalf("biggestScroll picked %v", got)
	}
	if got := biggestScroll(widget.NewLabel("none")); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
