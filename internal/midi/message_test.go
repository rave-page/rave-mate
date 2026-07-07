package midi

import "testing"

func TestDescribe(t *testing.T) {
	cases := []struct {
		m    Message
		want string // prefix (kind + channel) we assert; full layout is cosmetic
	}{
		{Message{0xB0, 20, 127}, "CC"},      // CC ch1 #20 = 127
		{Message{0xB3, 23, 64}, "CC"},       // CC ch4
		{Message{0x90, 60, 100}, "NoteOn"},  // note-on
		{Message{0x90, 60, 0}, "NoteOff"},   // note-on vel0 = off
		{Message{0x80, 60, 0}, "NoteOff"},   // note-off
		{Message{0xE0, 0, 64}, "PitchBend"}, // pitch bend
		{Message{0xC0, 5, 0}, "Program"},    // program change
	}
	for _, c := range cases {
		got := c.m.Describe()
		if len(got) < len(c.want) || got[:len(c.want)] != c.want {
			t.Errorf("Describe(%v) = %q, want prefix %q", c.m, got, c.want)
		}
	}
}

func TestKindCC(t *testing.T) {
	m := Message{0xB2, 27, 64}
	if !m.IsCC() || m.Channel() != 2 || m.Controller() != 27 || m.Value() != 64 {
		t.Fatalf("CC decode wrong: cc=%v ch=%d ctrl=%d val=%d", m.IsCC(), m.Channel(), m.Controller(), m.Value())
	}
}
