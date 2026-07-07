package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPlayerGioTriState: unset = Gio default; explicit false = legacy; true = Gio.
func TestPlayerGioTriState(t *testing.T) {
	var p PlayerFeature
	if !p.UseGioWindow() {
		t.Error("unset must default to Gio")
	}
	v := false
	p.GioWindow = &v
	if p.UseGioWindow() {
		t.Error("explicit false must select legacy")
	}
	v = true
	if !p.UseGioWindow() {
		t.Error("explicit true must select Gio")
	}
}

// TestPlayerGioJSONRoundTrip: absent field stays nil (no version bump needed); explicit
// values persist; nil marshals away (omitempty).
func TestPlayerGioJSONRoundTrip(t *testing.T) {
	var p PlayerFeature
	if err := json.Unmarshal([]byte(`{"embed":true}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.GioWindow != nil {
		t.Error("absent gioWindow must stay nil")
	}
	if err := json.Unmarshal([]byte(`{"gioWindow":false}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.GioWindow == nil || *p.GioWindow || p.UseGioWindow() {
		t.Error("explicit false lost in decode")
	}

	raw, err := json.Marshal(PlayerFeature{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "gioWindow") {
		t.Errorf("nil GioWindow must be omitted, got %s", raw)
	}
	f := false
	raw, err = json.Marshal(PlayerFeature{GioWindow: &f})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"gioWindow":false`) {
		t.Errorf("explicit false must persist, got %s", raw)
	}
}
