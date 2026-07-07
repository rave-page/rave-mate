package playerwin

// trimState is the pure IN/OUT cut range. out < 0 = to end. Matches the Fyne trim
// editor's semantics (internal/ui view_player.go) so exports behave identically.
type trimState struct {
	in, out float64
}

// clear resets to the full range.
func (t *trimState) clear() { t.in, t.out = 0, -1 }

// setIn pins IN at cur; an OUT at/before it resets to end.
func (t *trimState) setIn(cur float64) {
	if cur < 0 {
		cur = 0
	}
	t.in = cur
	if t.out >= 0 && t.out <= t.in {
		t.out = -1
	}
}

// setOut pins OUT at cur (≤ IN resets to end).
func (t *trimState) setOut(cur float64) {
	if cur <= t.in {
		t.out = -1
		return
	}
	t.out = cur
}

// keeps returns the kept duration given the media total (OUT=end uses total).
func (t *trimState) keeps(total float64) float64 {
	end := t.out
	if end < 0 {
		end = total
	}
	if d := end - t.in; d > 0 {
		return d
	}
	return 0
}

// String renders "IN 0:00 · OUT end" for the transport readout.
func (t *trimState) String() string {
	out := "end"
	if t.out >= 0 {
		out = clock(t.out)
	}
	return "IN " + clock(t.in) + " · OUT " + out
}
