//go:build zigui

package webui

import (
	"encoding/binary"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Three-way byte-equality gate for the RZW1 binary state wire (wave B-1 pilots):
// the Go renderer, the Zig JSON path (v1) and the Zig binary path (v2) must produce the
// SAME bytes for every fixture in the existing golden suites - full document AND every
// patched fragment. Run: make zig && GOWORK=off go test -count=1 -tags zigui ./internal/webui -run TestZigWire

func TestZigWireThreeWayAppGroups(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := agFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireAgState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderAppGroups(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderAppGroupsV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", appGroupsHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			b1, ok := zigui.RenderAppGroupsBody(js)
			if !ok {
				t.Fatal("v1 body render failed")
			}
			b2, ok := zigui.RenderAppGroupsBodyV2(doc)
			if !ok {
				t.Fatal("v2 body render failed")
			}
			assertBytesEqual(t, "body go==v1", appGroupsBodyHTML(st), b1)
			assertBytesEqual(t, "body v1==v2", b1, b2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

func TestZigWireThreeWayLogs(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := logsFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireLogsState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderLogs(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderLogsV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", logsHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			ldoc, ljs := wireLogsLines(st.Lines), stateJSON(st.Lines)
			l1, ok := zigui.RenderLogsLines(ljs)
			if !ok {
				t.Fatal("v1 lines render failed")
			}
			l2, ok := zigui.RenderLogsLinesV2(ldoc)
			if !ok {
				t.Fatal("v2 lines render failed")
			}
			assertBytesEqual(t, "lines go==v1", logsLinesHTML(st.Lines), l1)
			assertBytesEqual(t, "lines v1==v2", l1, l2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

// TestZigWireRejectsForeignDocuments pins the header contract: an export must refuse a
// document built for another message or another schema (that is what makes a stale
// libraveui.a a clean v1 downgrade instead of a mis-decode).
func TestZigWireRejectsForeignDocuments(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	st := agFixtures()["populated"]
	doc := wireAgState(st)

	if _, ok := zigui.RenderLogsV2(doc); ok {
		t.Error("logs export accepted an appgroups document")
	}
	if _, ok := zigui.RenderAppGroupsV2(stateJSON(st)); ok {
		t.Error("v2 export accepted a JSON document")
	}
	cases := map[string]func([]byte){
		"magic":      func(b []byte) { b[0] = 'X' },
		"msgID":      func(b []byte) { binary.LittleEndian.PutUint16(b[4:], 0xBEEF) },
		"schemaHash": func(b []byte) { binary.LittleEndian.PutUint32(b[6:], 0xDEADBEEF) },
		"arenaLen":   func(b []byte) { binary.LittleEndian.PutUint32(b[10:], 0xFFFF) },
	}
	for name, mutate := range cases {
		bad := append([]byte(nil), doc...)
		mutate(bad)
		if _, ok := zigui.RenderAppGroupsV2(bad); ok {
			t.Errorf("%s: mutated header accepted", name)
		}
	}
	for n := 0; n < len(doc); n++ { // every truncation must be refused
		if _, ok := zigui.RenderAppGroupsV2(doc[:n]); ok {
			t.Fatalf("truncation to %d bytes accepted", n)
		}
	}
}

// TestWireEmptyListsAreAbsentNotNull: the JSON path needed `,omitempty` on every nested slice
// because a nil slice marshalled `null` and the Zig parser rejected it (silently dropping a
// whole tab to Go). On the wire an empty list is simply an absent tag, and absent decodes to
// an empty slice - so nil and empty must render identically.
func TestWireEmptyListsAreAbsentNotNull(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	nilSt := agState{Title: "T", Subtitle: "S", Available: true, Empty: "none"} // Groups == nil
	emptySt := nilSt
	emptySt.Groups = []agGroup{}
	nilDoc, emptyDoc := wireAgState(nilSt), wireAgState(emptySt)
	if string(nilDoc) != string(emptyDoc) {
		t.Fatalf("nil and empty slice encode differently (%d vs %d bytes)", len(nilDoc), len(emptyDoc))
	}
	got, ok := zigui.RenderAppGroupsV2(nilDoc)
	if !ok {
		t.Fatal("v2 render of a nil-slice state failed")
	}
	assertBytesEqual(t, "nil slice", appGroupsHTML(nilSt), got)

	// Same one level deeper: a group with no apps, and a zero-value nested struct (logsState's
	// selects) - the JSON-era hazard class, now unrepresentable.
	deep := agState{Title: "T", Available: true, Launch: "Go", Groups: []agGroup{{ID: "g", Name: "n", Up: "0/0", Variant: "muted"}}}
	if h, ok := zigui.RenderAppGroupsV2(wireAgState(deep)); !ok {
		t.Fatal("v2 render of a nil apps slice failed")
	} else {
		assertBytesEqual(t, "nil nested slice", appGroupsHTML(deep), h)
	}
	zero := logsState{Title: "L"} // Level/Source/Lines all zero-value, every slice nil
	if h, ok := zigui.RenderLogsV2(wireLogsState(zero)); !ok {
		t.Fatal("v2 render of an all-zero logs state failed")
	} else {
		assertBytesEqual(t, "zero logs state", logsHTML(zero), h)
	}
}

// assertNoNewFallbacks fails when a render downgraded (v2→v1 or v1→Go) during the test.
func assertNoNewFallbacks(t *testing.T, before map[string]int) {
	t.Helper()
	for k, v := range zigui.FallbackCounts() {
		if v > before[k] {
			t.Errorf("fallback recorded during golden run: %s +%d", k, v-before[k])
		}
	}
}
