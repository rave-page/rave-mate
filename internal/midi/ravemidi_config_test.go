package midi

import "testing"

// DJ fan-out naming: clone the physical device name by default (Serato matches by name),
// "<Name> THRU" when distinct, and always fall back to "<Name> THRU" with no source.
func TestDJPortName(t *testing.T) {
	cases := []struct {
		name, source string
		distinct     bool
		want         string
	}{
		{"Controller 2", "DJ2GO2 Touch MIDI", false, "DJ2GO2 Touch MIDI"}, // clone (default)
		{"A61", "KOMPLETE KONTROL A61 MIDI", false, "KOMPLETE KONTROL A61 MIDI"},
		{"Controller 2", "DJ2GO2 Touch MIDI", true, "Controller 2 THRU"}, // distinct opt-in
		{"A61", "", false, "A61 THRU"},                                   // no source yet → fallback
		{"A61", "", true, "A61 THRU"},
	}
	for _, c := range cases {
		if got := DJPortName(c.name, c.source, c.distinct); got != c.want {
			t.Errorf("DJPortName(%q,%q,%v)=%q want %q", c.name, c.source, c.distinct, got, c.want)
		}
	}
}

// ManagedCfgs names the DJ-facing fan-out per the clone toggle: the device name (SourceMatch)
// by default, "<Name> THRU" when Distinct.
func TestManagedCfgsFanOutName(t *testing.T) {
	cfgs := ManagedCfgs([]ManagedInput{
		{Name: "Controller 2", SourceMatch: "DJ2GO2 Touch MIDI"},
		{Name: "A61", SourceMatch: "KOMPLETE KONTROL A61 MIDI", Distinct: true},
	})
	if len(cfgs) != 2 {
		t.Fatalf("want 2 cfgs, got %d", len(cfgs))
	}
	if got := cfgs[0].OutNames; len(got) != 1 || got[0] != "DJ2GO2 Touch MIDI" {
		t.Errorf("clone fan-out = %v, want [DJ2GO2 Touch MIDI]", got)
	}
	if got := cfgs[1].OutNames; len(got) != 1 || got[0] != "A61 THRU" {
		t.Errorf("distinct fan-out = %v, want [A61 THRU]", got)
	}
	// reserved-port ID/name is the config Name regardless of the fan-out clone.
	if cfgs[0].Name != "Controller 2" {
		t.Errorf("reserved Name = %q, want Controller 2", cfgs[0].Name)
	}
}
