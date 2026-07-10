package tagfix

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// cp1252High maps bytes 0x80-0x9F to their Windows-1252 runes (0 = undefined byte).
// Hand-rolled: cp1252 differs from latin1 only in this range, so no x/text dep needed.
var cp1252High = [32]rune{
	0x20AC, 0, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017D, 0,
	0, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0, 0x017E, 0x0178,
}

// cp1252Rev: cp1252 punctuation rune → original byte (for byte recovery).
var cp1252Rev = func() map[rune]byte {
	m := make(map[rune]byte, 27)
	for i, r := range cp1252High {
		if r != 0 {
			m[r] = byte(0x80 + i)
		}
	}
	return m
}()

// repairMojibake returns the repaired string + evidence when s is unambiguously
// charset-mangled; ok=false when clean or ambiguous. Two conservative heuristics:
//  1. Double-encoded UTF-8: every rune maps back to a latin1/cp1252 byte AND those bytes
//     are valid multi-byte UTF-8 with no control chars (the classic Ã©/Ã¼/â€™ patterns).
//     Applied repeatedly (max 3) for multiply-double-encoded text (ÃƒÂ©).
//  2. cp1252-mislabeled latin1: C1 control runes (U+0080-U+009F) - never legitimate text -
//     are cp1252 punctuation (0x91-0x97 curly quotes/dashes etc.).
func repairMojibake(s string) (repaired, detail string, ok bool) {
	if isASCII(s) || !utf8.ValidString(s) {
		return "", "", false
	}
	cur, rounds := s, 0
	for rounds < 3 {
		next, undone := undoubleUTF8(cur)
		if !undone {
			break
		}
		cur, rounds = next, rounds+1
	}
	if rounds > 0 {
		return cur, fmt.Sprintf("latin1/cp1252-mislabeled UTF-8 bytes (x%d): %q → %q", rounds, s, cur), true
	}
	if fixed, found := fixC1(s); found {
		return fixed, fmt.Sprintf("C1 control chars are cp1252 punctuation: %q → %q", s, fixed), true
	}
	return "", "", false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// undoubleUTF8 recovers the original bytes (rune≤0xFF = latin1 byte, cp1252 punctuation
// via cp1252Rev) and reinterprets them as UTF-8. ok only when the result is valid UTF-8
// with ≥1 multi-byte rune and no control chars - the unambiguous-mojibake guard: genuine
// latin1 text (e.g. "Störung") recovers to invalid UTF-8 and is never flagged.
func undoubleUTF8(s string) (string, bool) {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r <= 0xFF:
			b = append(b, byte(r))
		default:
			c, mapped := cp1252Rev[r]
			if !mapped {
				return "", false
			}
			b = append(b, c)
		}
	}
	if !utf8.Valid(b) {
		return "", false
	}
	out := string(b)
	if out == s {
		return "", false // pure single-byte content: nothing was double-encoded
	}
	multi := false
	for _, r := range out {
		if r >= 0x80 {
			multi = true
		}
		if r < 0x20 || (r >= 0x7F && r <= 0x9F) {
			return "", false // repaired text with control chars = wrong guess
		}
	}
	if !multi {
		return "", false
	}
	return out, true
}

// fixC1 maps C1 control runes (latin1-decoded cp1252 bytes) to cp1252 punctuation.
// Any undefined cp1252 byte (0x81/0x8D/0x8F/0x90/0x9D) = ambiguous, don't repair.
func fixC1(s string) (string, bool) {
	found := false
	var b strings.Builder
	for _, r := range s {
		if r >= 0x80 && r <= 0x9F {
			m := cp1252High[r-0x80]
			if m == 0 {
				return "", false
			}
			b.WriteRune(m)
			found = true
			continue
		}
		b.WriteRune(r)
	}
	if !found {
		return "", false
	}
	return b.String(), true
}
