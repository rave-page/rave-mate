//go:build windows

package midi

// Manual driver diagnosis: RAVEMIDI_DEBUG_DUMP=1 go test -run TestDriverDebugDump -v
// Dumps every managed input's port stats (QUERY_PORT) + wire trace (QUERY_TRACE).
// Read-only against the live driver; safe while rave-mate + DJ software run.

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

var traceDirNames = []string{"TapRaw", "ToApp", "ReadPop", "FromApp", "FeedbackOut", "LoopDrop"}

func dumpPort(t *testing.T, label string, id uint32) {
	if id == 0 {
		t.Logf("%s: port pending (id 0)", label)
		return
	}
	// RAVEMIDI_PORT_STATS: 16 ULONGs + 1 ULONGLONG, pack(1) = 72 bytes
	in := make([]byte, 4)
	binary.LittleEndian.PutUint32(in, id)
	buf := make([]byte, 72)
	if err := rmIoctl(raveMIDICtl(0x806, fileReadData), in, buf); err != nil {
		t.Logf("%s (port %d): QUERY_PORT failed: %v", label, id, err)
		return
	}
	u := func(i int) uint32 { return binary.LittleEndian.Uint32(buf[i*4:]) }
	t.Logf("%s (port %d): kind=%d streams=%d captureRunning=%d toAppBuf=%d fromAppBuf=%d toAppDropped=%d fromAppDropped=%d newStream=%d lastSetState=%d readCalls=%d readZero=%d lastReadBufLen=%d notify=%d writeIoctls=%d streamWrites=%d readBytesTotal=%d",
		label, u(0), u(1), u(2), u(3), u(4), u(5), u(6), u(7), u(8), int32(u(9)), u(10), u(11), u(12), u(13), u(14), u(15),
		binary.LittleEndian.Uint64(buf[64:]))
	es, err := QueryDriverTrace(id)
	if err != nil {
		t.Logf("%s: QUERY_TRACE failed: %v", label, err)
		return
	}
	var prev uint64
	for _, e := range es {
		dir := fmt.Sprintf("dir%d", e.Dir)
		if int(e.Dir) < len(traceDirNames) {
			dir = traceDirNames[e.Dir]
		}
		dt := ""
		if prev != 0 {
			dt = fmt.Sprintf("+%.1fms", float64(e.Time100ns-prev)/1e4)
		}
		prev = e.Time100ns
		t.Logf("  seq=%-5d %-8s %-11s len=%-3d % X", e.Seq, dt, dir, e.Len, e.Bytes)
	}
	t.Logf("%s: %d trace entries", label, len(es))
}

func TestDriverDebugDump(t *testing.T) {
	if os.Getenv("RAVEMIDI_DEBUG_DUMP") == "" {
		t.Skip("set RAVEMIDI_DEBUG_DUMP=1")
	}
	sts, err := QueryDriverInputs()
	if err != nil {
		t.Fatalf("QueryDriverInputs: %v", err)
	}
	for _, st := range sts {
		t.Logf("== input %q (name %q) bound=%v feedback=%v retries=%d iface=%q",
			st.ID, st.Name, st.Bound, st.FeedbackBound, st.RetryCount, st.BoundIface)
		dumpPort(t, "reserved", st.ReservedPortID)
		for i, oid := range st.OutPortIDs {
			dumpPort(t, fmt.Sprintf("fanout[%d]", i), oid)
		}
	}
}
