package worker

import (
	"testing"
)

func TestParseFpcalc(t *testing.T) {
	type tc struct {
		name        string
		input       string
		wantFP      string
		wantDur     float64
		wantErrFrag string
	}
	cases := []tc{
		{
			name:    "valid",
			input:   `{"duration":183.5,"fingerprint":"AQAAZ0mkUUmY5lGSRIk"}`,
			wantFP:  "AQAAZ0mkUUmY5lGSRIk",
			wantDur: 183.5,
		},
		{
			name:        "empty fingerprint",
			input:       `{"duration":10,"fingerprint":""}`,
			wantErrFrag: "empty fingerprint",
		},
		{
			name:        "invalid JSON",
			input:       `not json`,
			wantErrFrag: "unexpected output",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fp, dur, err := parseFpcalc([]byte(c.input))
			if c.wantErrFrag != "" {
				if err == nil {
					t.Fatalf("expected error, got nil (fp=%q dur=%v)", fp, dur)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fp != c.wantFP {
				t.Errorf("fingerprint: got %q want %q", fp, c.wantFP)
			}
			if dur != c.wantDur {
				t.Errorf("duration: got %v want %v", dur, c.wantDur)
			}
		})
	}
}
