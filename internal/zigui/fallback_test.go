package zigui

import "testing"

func TestFallbackCountsKeyedByCallerAndCopied(t *testing.T) {
	before := FallbackCounts()["TestFallbackCountsKeyedByCallerAndCopied"]
	noteFallback(1) // 1 = this test function
	got := FallbackCounts()
	if got["TestFallbackCountsKeyedByCallerAndCopied"] != before+1 {
		t.Fatalf("count not keyed by caller: %v", got)
	}
	got["TestFallbackCountsKeyedByCallerAndCopied"] = 999 // snapshot must be a copy
	if FallbackCounts()["TestFallbackCountsKeyedByCallerAndCopied"] != before+1 {
		t.Fatal("FallbackCounts leaked the live map")
	}
}
