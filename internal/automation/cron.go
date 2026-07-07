package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronExpr is a parsed 5-field cron (minute hour day-of-month month day-of-week) as bitsets.
// Supports '*', lists 'a,b', ranges 'a-b', and steps '*/n' / 'a-b/n'. day-of-week 0 and 7 are
// both Sunday. Minimal by design - no names, no '?'/'L'/'#'. Stdlib only (no soak-gated dep).
type cronExpr struct {
	min, hour, dom, month, dow uint64
}

type cronField struct {
	lo, hi int
}

var (
	fMin   = cronField{0, 59}
	fHour  = cronField{0, 23}
	fDom   = cronField{1, 31}
	fMonth = cronField{1, 12}
	fDow   = cronField{0, 7} // 7 normalized to 0 (Sunday)
)

// ValidateCron reports whether expr is a valid 5-field cron expression (for UI validation).
func ValidateCron(expr string) error {
	_, err := parseCron(expr)
	return err
}

// parseCron parses a 5-field cron expression.
func parseCron(expr string) (cronExpr, error) {
	parts := strings.Fields(strings.TrimSpace(expr))
	if len(parts) != 5 {
		return cronExpr{}, fmt.Errorf("cron: want 5 fields, got %d", len(parts))
	}
	var c cronExpr
	var err error
	if c.min, err = parseCronField(parts[0], fMin); err != nil {
		return c, fmt.Errorf("cron minute: %w", err)
	}
	if c.hour, err = parseCronField(parts[1], fHour); err != nil {
		return c, fmt.Errorf("cron hour: %w", err)
	}
	if c.dom, err = parseCronField(parts[2], fDom); err != nil {
		return c, fmt.Errorf("cron day-of-month: %w", err)
	}
	if c.month, err = parseCronField(parts[3], fMonth); err != nil {
		return c, fmt.Errorf("cron month: %w", err)
	}
	if c.dow, err = parseCronField(parts[4], fDow); err != nil {
		return c, fmt.Errorf("cron day-of-week: %w", err)
	}
	c.dow = normalizeDow(c.dow)
	return c, nil
}

// parseCronField parses one comma-list of terms into a bitset.
func parseCronField(field string, f cronField) (uint64, error) {
	var set uint64
	for term := range strings.SplitSeq(field, ",") {
		bits, err := parseCronTerm(term, f)
		if err != nil {
			return 0, err
		}
		set |= bits
	}
	if set == 0 {
		return 0, fmt.Errorf("empty field %q", field)
	}
	return set, nil
}

// parseCronTerm parses one term: '*', 'a', 'a-b', '*/n', or 'a-b/n'.
func parseCronTerm(term string, f cronField) (uint64, error) {
	step := 1
	rangePart := term
	if base, stepStr, ok := strings.Cut(term, "/"); ok {
		rangePart = base
		n, err := strconv.Atoi(stepStr)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("bad step in %q", term)
		}
		step = n
	}

	lo, hi := f.lo, f.hi
	if rangePart != "*" {
		if a, b, ok := strings.Cut(rangePart, "-"); ok {
			av, err1 := strconv.Atoi(a)
			bv, err2 := strconv.Atoi(b)
			if err1 != nil || err2 != nil {
				return 0, fmt.Errorf("bad range %q", rangePart)
			}
			lo, hi = av, bv
		} else {
			n, err := strconv.Atoi(rangePart)
			if err != nil {
				return 0, fmt.Errorf("bad value %q", rangePart)
			}
			lo, hi = n, n
		}
	}
	if lo < f.lo || hi > f.hi || lo > hi {
		return 0, fmt.Errorf("value out of range %d-%d (allowed %d-%d)", lo, hi, f.lo, f.hi)
	}
	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << uint(v)
	}
	return bits, nil
}

// normalizeDow folds bit 7 (Sunday) into bit 0.
func normalizeDow(set uint64) uint64 {
	if set&(1<<7) != 0 {
		set |= 1 << 0
		set &^= 1 << 7
	}
	return set
}

// matches reports whether t satisfies the expression (minute granularity). Per Vixie cron, when
// both day-of-month and day-of-week are restricted (neither '*'), a match on EITHER suffices.
func (c cronExpr) matches(t time.Time) bool {
	if c.min&(1<<uint(t.Minute())) == 0 {
		return false
	}
	if c.hour&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if c.month&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	domMatch := c.dom&(1<<uint(t.Day())) != 0
	dowMatch := c.dow&(1<<uint(int(t.Weekday()))) != 0

	domRestricted := c.dom != allBits(fDom)
	dowRestricted := c.dow != normalizeDow(allBits(fDow))
	if domRestricted && dowRestricted {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
}

// allBits is the full bitset for a field (every value lo..hi set).
func allBits(f cronField) uint64 {
	var bits uint64
	for v := f.lo; v <= f.hi; v++ {
		bits |= 1 << uint(v)
	}
	return bits
}
