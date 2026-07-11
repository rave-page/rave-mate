//go:build windows && manual

package midi

// Raw-KS capture-pin probes for the ravemidi driver — the evidence base for the
// 2026-07-11 "kernel quirks" investigation (design doc: "Raw-KS user-mode reader
// quirks" section). Findings, all measured against the installed v3 driver:
//
//   - user-mode KS readers (ksuser KsCreatePin + IOCTL_KS_READ_STREAM, standard
//     streaming — exactly midisrv's KSA client): reads pend while the running pin
//     is virgin, but once ANY byte flows every read completes instantly with
//     DataUsed=FrameExtent and an untouched (zero) buffer — the empty-frame
//     firehose, and total data loss.
//   - teVirtualMIDI (loopMIDI) shows byte-identical behavior => portcls
//     CPortPinMidi-inherent, NOT our miniport (QUERY_PORT counters prove our
//     Read handed the bytes to portcls).
//   - kernel-mode readers get correct KSMUSICFORMAT records over the same wire
//     (TestRaveMIDIKernelTapDelivery), winmm/wdmaud delivers correctly
//     (TestRaveMIDIWinmmDelivery) => portcls only materializes capture data for
//     kernel-mode requestors.
//
// Hard assertions = what our driver CAN fix: KSPROPERTY_PIN_NAME returns the
// port name (quirk 2; passes once the fixed driver is installed) and a virgin
// pin's read pends. The user-mode data loss is logged, not failed. Run:
//   GOWORK=off go test -tags manual -run 'TestRaveMIDI|TestTeVirtualMIDI' -v ./internal/midi/
// Read-only vs the driver beyond creating (and destroying) its own probe ports.

import (
	"encoding/binary"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ks.h / ksmedia.h constants + GUIDs (fixed ABI).
const (
	ioctlKsProperty   = 0x2F0003 // IOCTL_KS_PROPERTY
	ioctlKsReadStream = 0x2F4017 // IOCTL_KS_READ_STREAM

	ksStateStop    = 0
	ksStateAcquire = 1
	ksStatePause   = 2
	ksStateRun     = 3

	ksPropertyTypeGet = 1
	ksPropertyTypeSet = 2

	ksPriorityNormal = 0x40000000

	// KSSTREAM_HEADER x64: FrameExtent@32 DataUsed@36 Data@40, size 56.
	ksStreamHeaderSize = 56
	probeFrameExtent   = 512
)

func ksGUID(a uint32, b, c uint16, d [8]byte) windows.GUID {
	return windows.GUID{Data1: a, Data2: b, Data3: c, Data4: d}
}

var (
	ksCatAudio          = ksGUID(0x6994AD04, 0x93EF, 0x11D0, [8]byte{0xA3, 0xCC, 0x00, 0xA0, 0xC9, 0x22, 0x31, 0x96})
	ksIfaceSetStandard  = ksGUID(0x1A8766A0, 0x62CE, 0x11CF, [8]byte{0xA5, 0xD6, 0x28, 0xDB, 0x04, 0xC1, 0x00, 0x00})
	ksMediumSetStandard = ksGUID(0x4747B320, 0x62CE, 0x11CF, [8]byte{0xA5, 0xD6, 0x28, 0xDB, 0x04, 0xC1, 0x00, 0x00})
	ksTypeMusic         = ksGUID(0xE725D360, 0x62CC, 0x11CF, [8]byte{0xA5, 0xD6, 0x28, 0xDB, 0x04, 0xC1, 0x00, 0x00})
	ksSubtypeMidi       = ksGUID(0x1D262760, 0xE957, 0x11CF, [8]byte{0xA5, 0xD6, 0x28, 0xDB, 0x04, 0xC1, 0x00, 0x00})
	ksSpecifierNone     = ksGUID(0x0F6417D6, 0xC318, 0x11D0, [8]byte{0xA4, 0x3F, 0x00, 0xA0, 0xC9, 0x22, 0x31, 0x96})
	ksPropSetConnection = ksGUID(0x1D58C920, 0xAC9B, 0x11CF, [8]byte{0xA5, 0xD6, 0x28, 0xDB, 0x04, 0xC1, 0x00, 0x00})
	ksPropSetPin        = ksGUID(0x8C134960, 0x51AD, 0x11CF, [8]byte{0x87, 0x8A, 0x94, 0xF8, 0x01, 0xC1, 0x00, 0x00})
)

var ksuserCreatePin = windows.NewLazySystemDLL("ksuser.dll").NewProc("KsCreatePin")

func putGUID(b []byte, g windows.GUID) {
	binary.LittleEndian.PutUint32(b[0:], g.Data1)
	binary.LittleEndian.PutUint16(b[4:], g.Data2)
	binary.LittleEndian.PutUint16(b[6:], g.Data3)
	copy(b[8:], g.Data4[:])
}

// findPortSymlink locates the KSCATEGORY_AUDIO device interface whose ref-string
// tail is "RavePort<id>" (the subdevice PcRegisterSubdevice registered).
func findPortSymlink(portID uint32) (string, error) {
	want := fmt.Sprintf(`\raveport%d`, portID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ifaces, err := windows.CM_Get_Device_Interface_List("", &ksCatAudio,
			windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
		if err != nil {
			return "", fmt.Errorf("CM_Get_Device_Interface_List: %v", err)
		}
		for _, s := range ifaces {
			if strings.HasSuffix(strings.ToLower(s), want) {
				return s, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no KSCATEGORY_AUDIO interface ends in %q", want)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// ksCreatePin instantiates pin 0 (the capture stream pin of an OUT_ONLY port)
// with the MUSIC/MIDI/NONE format, exactly like midisrv / our kernel tap.
func ksCreatePin(filter windows.Handle) (windows.Handle, error) {
	// KSPIN_CONNECT (72 B on x64) + KSDATAFORMAT (64 B).
	blob := make([]byte, 72+64)
	putGUID(blob[0:], ksIfaceSetStandard) // Interface.Set, Id=0 (STREAMING), Flags=0
	putGUID(blob[24:], ksMediumSetStandard)
	binary.LittleEndian.PutUint32(blob[48:], 0) // PinId = 0
	// PinToHandle @56 = NULL
	binary.LittleEndian.PutUint32(blob[64:], ksPriorityNormal)
	binary.LittleEndian.PutUint32(blob[68:], 1) // PrioritySubClass
	f := blob[72:]
	binary.LittleEndian.PutUint32(f[0:], 64) // FormatSize
	putGUID(f[16:], ksTypeMusic)
	putGUID(f[32:], ksSubtypeMidi)
	putGUID(f[48:], ksSpecifierNone)

	var pin windows.Handle
	r, _, _ := ksuserCreatePin.Call(uintptr(filter), uintptr(unsafe.Pointer(&blob[0])),
		uintptr(windows.GENERIC_READ), uintptr(unsafe.Pointer(&pin)))
	if r != 0 {
		return 0, fmt.Errorf("KsCreatePin: 0x%X", r)
	}
	return pin, nil
}

// ksSyncIoctl issues an overlapped DeviceIoControl on a KS handle and waits for it.
func ksSyncIoctl(h windows.Handle, code uint32, in, out []byte) (uint32, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(ev) }()
	ov := windows.Overlapped{HEvent: ev}
	var inPtr, outPtr *byte
	if len(in) > 0 {
		inPtr = &in[0]
	}
	if len(out) > 0 {
		outPtr = &out[0]
	}
	var br uint32
	err = windows.DeviceIoControl(h, code, inPtr, uint32(len(in)), outPtr, uint32(len(out)), &br, &ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(h, &ov, &br, true)
	}
	return br, err
}

func setPinState(pin windows.Handle, state uint32) error {
	prop := make([]byte, 24) // KSPROPERTY
	putGUID(prop, ksPropSetConnection)
	binary.LittleEndian.PutUint32(prop[16:], 0) // KSPROPERTY_CONNECTION_STATE
	binary.LittleEndian.PutUint32(prop[20:], ksPropertyTypeSet)
	st := make([]byte, 4)
	binary.LittleEndian.PutUint32(st, state)
	_, err := ksSyncIoctl(pin, ioctlKsProperty, prop, st)
	return err
}

func getPinState(pin windows.Handle) (uint32, error) {
	prop := make([]byte, 24)
	putGUID(prop, ksPropSetConnection)
	binary.LittleEndian.PutUint32(prop[16:], 0)
	binary.LittleEndian.PutUint32(prop[20:], ksPropertyTypeGet)
	st := make([]byte, 4)
	if _, err := ksSyncIoctl(pin, ioctlKsProperty, prop, st); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(st), nil
}

// getPinName reads KSPROPERTY_PIN_NAME (id 12) via KSP_PIN on the FILTER handle —
// what midisrv uses to name MIDI 1 ports / UMP endpoints (quirk 2).
func getPinName(filter windows.Handle, pinID uint32) (string, error) {
	prop := make([]byte, 32) // KSP_PIN: KSPROPERTY + PinId + Reserved
	putGUID(prop, ksPropSetPin)
	binary.LittleEndian.PutUint32(prop[16:], 12) // KSPROPERTY_PIN_NAME
	binary.LittleEndian.PutUint32(prop[20:], ksPropertyTypeGet)
	binary.LittleEndian.PutUint32(prop[24:], pinID)
	out := make([]byte, 256)
	br, err := ksSyncIoctl(filter, ioctlKsProperty, prop, out)
	if err != nil {
		return "", err
	}
	u := make([]uint16, 0, br/2)
	for i := 0; i+1 < int(br); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(out[i:]))
	}
	return windows.UTF16ToString(u), nil
}

// getPinULong reads a ULONG-typed KSPROPSETID_Pin property via KSP_PIN.
func getPinULong(filter windows.Handle, pinID, propID uint32) (uint32, error) {
	prop := make([]byte, 32)
	putGUID(prop, ksPropSetPin)
	binary.LittleEndian.PutUint32(prop[16:], propID)
	binary.LittleEndian.PutUint32(prop[20:], ksPropertyTypeGet)
	binary.LittleEndian.PutUint32(prop[24:], pinID)
	out := make([]byte, 4)
	if _, err := ksSyncIoctl(filter, ioctlKsProperty, prop, out); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(out), nil
}

func listAudioIfaces() (map[string]bool, error) {
	ifaces, err := windows.CM_Get_Device_Interface_List("", &ksCatAudio,
		windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(ifaces))
	for _, s := range ifaces {
		m[s] = true
	}
	return m, nil
}

// ksProbeStats snapshots the QUERY_PORT counters relevant to the read loop.
type ksProbeStats struct {
	newStream, lastSetState, readCalls, readZero, lastReadBufLen, notify uint32
}

func queryKsProbeStats(portID uint32) (ksProbeStats, error) {
	in := make([]byte, 4)
	binary.LittleEndian.PutUint32(in, portID)
	buf := make([]byte, 72) // RAVEMIDI_PORT_STATS pack(1)
	if err := rmIoctl(raveMIDICtl(0x806, fileReadData), in, buf); err != nil {
		return ksProbeStats{}, err
	}
	u := func(i int) uint32 { return binary.LittleEndian.Uint32(buf[i*4:]) }
	return ksProbeStats{
		newStream: u(8), lastSetState: u(9), readCalls: u(10),
		readZero: u(11), lastReadBufLen: u(12), notify: u(13),
	}, nil
}

// asyncRead posts one overlapped IOCTL_KS_READ_STREAM. hdr+data must stay live.
type asyncRead struct {
	pin  windows.Handle
	ev   windows.Handle
	ov   *windows.Overlapped
	hdr  []byte
	data []byte
}

func startRead(pin windows.Handle) (*asyncRead, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	r := &asyncRead{
		pin: pin, ev: ev, ov: &windows.Overlapped{HEvent: ev},
		hdr: make([]byte, ksStreamHeaderSize), data: make([]byte, probeFrameExtent),
	}
	binary.LittleEndian.PutUint32(r.hdr[0:], ksStreamHeaderSize)                  // Size
	binary.LittleEndian.PutUint32(r.hdr[32:], probeFrameExtent)                   // FrameExtent
	*(*uintptr)(unsafe.Pointer(&r.hdr[40])) = uintptr(unsafe.Pointer(&r.data[0])) // Data
	var br uint32
	err = windows.DeviceIoControl(pin, ioctlKsReadStream, nil, 0,
		&r.hdr[0], ksStreamHeaderSize, &br, r.ov)
	if err != nil && err != windows.ERROR_IO_PENDING {
		_ = windows.CloseHandle(ev)
		return nil, fmt.Errorf("IOCTL_KS_READ_STREAM: %v", err)
	}
	return r, nil
}

// wait blocks up to d; returns (completed, dataUsed).
func (r *asyncRead) wait(d time.Duration) (bool, uint32) {
	w, _ := windows.WaitForSingleObject(r.ev, uint32(d.Milliseconds()))
	if w != windows.WAIT_OBJECT_0 {
		return false, 0
	}
	var br uint32
	_ = windows.GetOverlappedResult(r.pin, r.ov, &br, false)
	return true, binary.LittleEndian.Uint32(r.hdr[36:]) // DataUsed
}

func (r *asyncRead) close() {
	_ = windows.CancelIoEx(r.pin, r.ov)
	_, _ = windows.WaitForSingleObject(r.ev, 2000) // reap before freeing buffers
	_ = windows.CloseHandle(r.ev)
}

func TestRaveMIDIKsCapturePin(t *testing.T) {
	if !raveMIDIAvailable() {
		t.Skip("ravemidi control device not available")
	}
	const portName = "ravemidi ks probe"
	op, err := openRaveMIDIOut(portName)
	if err != nil {
		t.Fatalf("create port: %v", err)
	}
	rp := op.(*RaveMIDIOut)
	defer rp.Close()
	t.Logf("port %q id=%d", portName, rp.portID)

	sym, err := findPortSymlink(rp.portID)
	if err != nil {
		t.Fatalf("find interface: %v", err)
	}
	t.Logf("interface: %s", sym)

	symW, _ := windows.UTF16PtrFromString(sym)
	filter, err := windows.CreateFile(symW, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		t.Fatalf("open filter: %v", err)
	}
	defer func() { _ = windows.CloseHandle(filter) }()

	// Quirk 2 observability: the pin factory name midisrv reads. Pre-fix drivers
	// have no KSPROPERTY_PIN_NAME handler → portcls falls back to the category
	// name (or fails); fixed drivers return the port name.
	if name, err := getPinName(filter, 0); err != nil {
		t.Errorf("KSPROPERTY_PIN_NAME(0): %v (no handler — midisrv falls back to generic naming; fixed driver answers the port name)", err)
	} else {
		t.Logf("KSPROPERTY_PIN_NAME(0) = %q", name)
		if name != portName {
			t.Errorf("pin name = %q, want port name %q (quirk 2: midisrv names endpoints from this)", name, portName)
		}
	}

	pin, err := ksCreatePin(filter)
	if err != nil {
		t.Fatalf("create pin: %v", err)
	}
	// Teardown order matters: pin must close before rp.Close(), else the port
	// destroy sees StreamCount>0.
	defer func() { _ = windows.CloseHandle(pin) }()
	defer func() { _ = setPinState(pin, ksStatePause); _ = setPinState(pin, ksStateStop) }()

	for _, s := range []uint32{ksStateAcquire, ksStatePause, ksStateRun} {
		if err := setPinState(pin, s); err != nil {
			t.Fatalf("set pin state %d: %v", s, err)
		}
	}
	if st, err := getPinState(pin); err != nil || st != ksStateRun {
		t.Fatalf("pin state = %d (%v), want RUN", st, err)
	}

	before, err := queryKsProbeStats(rp.portID)
	if err != nil {
		t.Fatalf("QUERY_PORT: %v", err)
	}
	t.Logf("counters pre-read:  %+v", before)

	// PHASE 1 — empty pin: the read must PEND (fixed behavior). The pre-fix
	// firehose completes instantly with DataUsed=FrameExtent and zero payload.
	r1, err := startRead(pin)
	if err != nil {
		t.Fatalf("start read: %v", err)
	}
	completed, used := r1.wait(500 * time.Millisecond)
	mid, _ := queryKsProbeStats(rp.portID)
	t.Logf("counters post-read: %+v", mid)
	if completed {
		t.Errorf("FIREHOSE: empty-pin read completed instantly, DataUsed=%d FrameExtent=%d payload[0:16]=% X",
			used, probeFrameExtent, r1.data[:16])
		// Rate sample: how fast do empty frames spew?
		n, t0 := 0, time.Now()
		for time.Since(t0) < 200*time.Millisecond {
			rr, err := startRead(pin)
			if err != nil {
				break
			}
			done, u2 := rr.wait(50 * time.Millisecond)
			rr.close()
			if !done {
				break
			}
			_ = u2
			n++
		}
		after, _ := queryKsProbeStats(rp.portID)
		t.Logf("firehose rate: %d completions in 200ms; counters now: %+v", n, after)
		r1.close()
	} else {
		t.Logf("OK: empty-pin read pending after 500ms (no firehose)")

		// PHASE 2 — inject via IOCTL_RAVEMIDI_WRITE; the pended read must complete
		// with our bytes wrapped in a KSMUSICFORMAT record.
		rp.Send(0x90, 0x40, 0x7F)
		completed, used = r1.wait(2 * time.Second)
		if !completed {
			t.Errorf("read did not complete within 2s of IOCTL_RAVEMIDI_WRITE")
		} else {
			tdMs := binary.LittleEndian.Uint32(r1.data[0:])
			bc := binary.LittleEndian.Uint32(r1.data[4:])
			t.Logf("read completed: DataUsed=%d (FrameExtent=%d) TimeDeltaMs=%d ByteCount=%d frame[0:32]=% X",
				used, probeFrameExtent, tdMs, bc, r1.data[:32])
			if bc == 3 && r1.data[8] == 0x90 && r1.data[9] == 0x40 && r1.data[10] == 0x7F {
				t.Logf("KSMUSICFORMAT record intact: portcls delivered to a user-mode reader (fixed OS?)")
			} else {
				// Known portcls CPortPinMidi limitation: user-mode readers get
				// DataUsed=FrameExtent with an untouched buffer (kernel readers +
				// wdmaud receive fine). Identical on teVirtualMIDI — see design doc.
				t.Logf("KNOWN portcls limitation: user-mode reader got no data (ByteCount=%d, DataUsed=%d)", bc, used)
			}
		}
		r1.close()
	}

	after, _ := queryKsProbeStats(rp.portID)
	t.Logf("counters final: %+v (delta read=%d readZero=%d notify=%d lastBufLen=%d)",
		after, after.readCalls-before.readCalls, after.readZero-before.readZero,
		after.notify-before.notify, after.lastReadBufLen)
}

// TestTeVirtualMIDIKsCapturePin runs the same raw-KS read against a teVirtualMIDI
// (loopMIDI) port — the reference PortCls/PortMidi implementation midisrv is
// exposed to in the field. Comparative control for the ravemidi probe: identical
// behavior = portcls-inherent, divergent = our miniport.
func TestTeVirtualMIDIKsCapturePin(t *testing.T) {
	if !VirtualAvailable() {
		t.Skip("teVirtualMIDI DLL not installed")
	}
	beforeIfaces, err := listAudioIfaces()
	if err != nil {
		t.Fatalf("list interfaces: %v", err)
	}
	const portName = "tvm ks probe"
	vp, err := OpenVirtualOut(portName)
	if err != nil {
		t.Skipf("teVirtualMIDI create failed: %v", err)
	}
	defer vp.Close()

	// The new port's KS interface = the diff vs the pre-create snapshot.
	var sym string
	deadline := time.Now().Add(5 * time.Second)
	for sym == "" {
		now, err := listAudioIfaces()
		if err != nil {
			t.Fatalf("list interfaces: %v", err)
		}
		for s := range now {
			if !beforeIfaces[s] {
				sym = s
				break
			}
		}
		if sym == "" && time.Now().After(deadline) {
			t.Fatalf("no new KSCATEGORY_AUDIO interface appeared for %q", portName)
		}
		if sym == "" {
			time.Sleep(250 * time.Millisecond)
		}
	}
	t.Logf("interface: %s", sym)

	symW, _ := windows.UTF16PtrFromString(sym)
	filter, err := windows.CreateFile(symW, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		t.Fatalf("open filter: %v", err)
	}
	defer func() { _ = windows.CloseHandle(filter) }()

	// Pin census: count, dataflow, name — what does the REFERENCE driver answer?
	ctypes, err := getPinULong(filter, 0, 1) // KSPROPERTY_PIN_CTYPES
	if err != nil {
		t.Fatalf("PIN_CTYPES: %v", err)
	}
	capturePin := uint32(0xFFFFFFFF)
	for i := uint32(0); i < ctypes; i++ {
		flow, err := getPinULong(filter, i, 2) // KSPROPERTY_PIN_DATAFLOW
		name, nerr := getPinName(filter, i)
		t.Logf("pin %d: dataflow=%d (err %v) name=%q (err %v)", i, flow, err, name, nerr)
		if err == nil && flow == 2 && capturePin == 0xFFFFFFFF { // KSPIN_DATAFLOW_OUT
			capturePin = i
		}
	}
	if capturePin == 0xFFFFFFFF {
		t.Fatalf("no capture (DATAFLOW_OUT) pin on the teVirtualMIDI filter")
	}

	// Same connect as the ravemidi probe, but on the discovered pin id.
	blob := make([]byte, 72+64)
	putGUID(blob[0:], ksIfaceSetStandard)
	putGUID(blob[24:], ksMediumSetStandard)
	binary.LittleEndian.PutUint32(blob[48:], capturePin)
	binary.LittleEndian.PutUint32(blob[64:], ksPriorityNormal)
	binary.LittleEndian.PutUint32(blob[68:], 1)
	f := blob[72:]
	binary.LittleEndian.PutUint32(f[0:], 64)
	putGUID(f[16:], ksTypeMusic)
	putGUID(f[32:], ksSubtypeMidi)
	putGUID(f[48:], ksSpecifierNone)
	var pin windows.Handle
	r, _, _ := ksuserCreatePin.Call(uintptr(filter), uintptr(unsafe.Pointer(&blob[0])),
		uintptr(windows.GENERIC_READ), uintptr(unsafe.Pointer(&pin)))
	if r != 0 {
		t.Fatalf("KsCreatePin(pin %d): 0x%X", capturePin, r)
	}
	defer func() { _ = windows.CloseHandle(pin) }()
	defer func() { _ = setPinState(pin, ksStatePause); _ = setPinState(pin, ksStateStop) }()

	for _, s := range []uint32{ksStateAcquire, ksStatePause, ksStateRun} {
		if err := setPinState(pin, s); err != nil {
			t.Fatalf("set pin state %d: %v", s, err)
		}
	}

	r1, err := startRead(pin)
	if err != nil {
		t.Fatalf("start read: %v", err)
	}
	defer r1.close()
	completed, used := r1.wait(500 * time.Millisecond)
	if completed {
		t.Logf("REFERENCE FIREHOSE: empty-pin read completed instantly, DataUsed=%d frame[0:32]=% X", used, r1.data[:32])
		return
	}
	t.Logf("reference: empty-pin read pending after 500ms")
	vp.Send(0x90, 0x40, 0x7F)
	completed, used = r1.wait(2 * time.Second)
	if !completed {
		t.Logf("reference: read still pending 2s after SendData")
		return
	}
	t.Logf("reference completed: DataUsed=%d (FrameExtent=%d) frame[0:32]=% X", used, probeFrameExtent, r1.data[:32])
}

func dumpFrame(t *testing.T, tag string, data []byte, used uint32) {
	n := int(used)
	if n > len(data) {
		n = len(data)
	}
	// locate our marker message anywhere in the frame
	idx := -1
	for i := 0; i+2 < n; i++ {
		if data[i] == 0x90 && data[i+1] == 0x40 && data[i+2] == 0x7F {
			idx = i
			break
		}
	}
	t.Logf("%s: DataUsed=%d markerAt=%d frame[0:48]=% X", tag, used, idx, data[:48])
	if idx > 0 {
		lo := idx - 8
		if lo < 0 {
			lo = 0
		}
		t.Logf("%s: around marker [%d:%d]=% X", tag, lo, idx+8, data[lo:idx+8])
	}
}

// TestRaveMIDIKsReadSemantics maps portcls CPortPinMidi completion behavior:
// (A) data queued BEFORE the read arrives, (B) read pending -> write -> requeue
// storm check (the firehose), (C) two queued reads -> one write.
func TestRaveMIDIKsReadSemantics(t *testing.T) {
	if !raveMIDIAvailable() {
		t.Skip("ravemidi control device not available")
	}
	op, err := openRaveMIDIOut("ravemidi ks sem")
	if err != nil {
		t.Fatalf("create port: %v", err)
	}
	rp := op.(*RaveMIDIOut)
	defer rp.Close()
	sym, err := findPortSymlink(rp.portID)
	if err != nil {
		t.Fatalf("find interface: %v", err)
	}
	symW, _ := windows.UTF16PtrFromString(sym)
	filter, err := windows.CreateFile(symW, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		t.Fatalf("open filter: %v", err)
	}
	defer func() { _ = windows.CloseHandle(filter) }()
	pin, err := ksCreatePin(filter)
	if err != nil {
		t.Fatalf("create pin: %v", err)
	}
	defer func() { _ = windows.CloseHandle(pin) }()
	defer func() { _ = setPinState(pin, ksStatePause); _ = setPinState(pin, ksStateStop) }()
	for _, s := range []uint32{ksStateAcquire, ksStatePause, ksStateRun} {
		if err := setPinState(pin, s); err != nil {
			t.Fatalf("set pin state %d: %v", s, err)
		}
	}

	// (A) write FIRST, then read.
	rp.Send(0x90, 0x40, 0x7F)
	time.Sleep(100 * time.Millisecond)
	rA, err := startRead(pin)
	if err != nil {
		t.Fatalf("A: start read: %v", err)
	}
	if done, used := rA.wait(500 * time.Millisecond); done {
		dumpFrame(t, "A (write-then-read)", rA.data, used)
	} else {
		t.Logf("A (write-then-read): read STILL PENDING with data queued")
	}
	rA.close()

	// (B) read pending -> write -> completion, then requeue with NO further
	// writes: instant completions here = the midisrv firehose.
	rB, err := startRead(pin)
	if err != nil {
		t.Fatalf("B: start read: %v", err)
	}
	if done, _ := rB.wait(300 * time.Millisecond); done {
		t.Logf("B: read completed with no data queued (leftover state from A)")
	} else {
		rp.Send(0x90, 0x40, 0x7F)
		if done, used := rB.wait(2 * time.Second); done {
			dumpFrame(t, "B (pend-then-write)", rB.data, used)
		} else {
			t.Logf("B: read still pending 2s after write")
		}
	}
	rB.close()
	instant := 0
	for i := 0; i < 5; i++ {
		rr, err := startRead(pin)
		if err != nil {
			t.Fatalf("B requeue %d: %v", i, err)
		}
		done, used := rr.wait(200 * time.Millisecond)
		if done {
			instant++
			dumpFrame(t, fmt.Sprintf("B requeue %d (NO write)", i), rr.data, used)
		}
		rr.close()
		if !done {
			break
		}
	}
	t.Logf("B: %d/5 requeued reads completed with no new data (firehose if >0 sustained)", instant)

	// (C) TWO queued reads -> one write: which completes, where's the record?
	rC1, err := startRead(pin)
	if err != nil {
		t.Fatalf("C: read1: %v", err)
	}
	rC2, err := startRead(pin)
	if err != nil {
		t.Fatalf("C: read2: %v", err)
	}
	if d, u := rC1.wait(200 * time.Millisecond); d {
		dumpFrame(t, "C read1 pre-write", rC1.data, u)
	}
	if d, u := rC2.wait(50 * time.Millisecond); d {
		dumpFrame(t, "C read2 pre-write", rC2.data, u)
	}
	rp.Send(0x90, 0x40, 0x7F)
	if d, u := rC1.wait(1 * time.Second); d {
		dumpFrame(t, "C read1 post-write", rC1.data, u)
	} else {
		t.Logf("C read1: still pending after write")
	}
	if d, u := rC2.wait(1 * time.Second); d {
		dumpFrame(t, "C read2 post-write", rC2.data, u)
	} else {
		t.Logf("C read2: still pending after write")
	}
	rC1.close()
	rC2.close()
}

// TestRaveMIDIWinmmDelivery is the wdmaud control: classic winmm midiInOpen on a
// ravemidi port must deliver IOCTL_RAVEMIDI_WRITE bytes as MIM_DATA. Proves the
// portcls capture path works for wdmaud even where raw-KS standard streaming
// (midisrv-style) gets empty frames.
func TestRaveMIDIWinmmDelivery(t *testing.T) {
	if !raveMIDIAvailable() {
		t.Skip("ravemidi control device not available")
	}
	const portName = "ravemidi winmm probe"
	op, err := openRaveMIDIOut(portName)
	if err != nil {
		t.Fatalf("create port: %v", err)
	}
	rp := op.(*RaveMIDIOut)
	defer rp.Close()

	// winmm enumeration lag: wait for the port to appear.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ins, _ := Ports()
		found := false
		for _, n := range ins {
			if n == portName {
				found = true
			}
		}
		if found || time.Now().After(deadline) {
			if !found {
				t.Fatalf("port %q never appeared in winmm midiIn list", portName)
			}
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	in, err := Open(portName)
	if err != nil {
		t.Fatalf("midiInOpen: %v", err)
	}
	defer func() { _ = in.Close() }()

	rp.Send(0x90, 0x40, 0x7F)
	select {
	case m := <-in.Messages():
		t.Logf("winmm delivered: % X", []byte{m.Status, m.Data1, m.Data2})
		if m.Status != 0x90 || m.Data1 != 0x40 || m.Data2 != 0x7F {
			t.Errorf("wrong message: % X", []byte{m.Status, m.Data1, m.Data2})
		}
	case <-time.After(3 * time.Second):
		t.Errorf("winmm delivered NOTHING within 3s (wdmaud capture path broken too)")
	}
}

// TestRaveMIDIKernelTapDelivery: kernel-mode KS client control. A legacy mirror
// (driver-side RAVE_TAP, kernel ZwCreateFile + IOCTL_KS_READ_STREAM, standard
// streaming) taps port A's capture pin and fans into port B. If B receives the
// bytes, the same portcls pin that hands user-mode readers zeroed frames delivers
// fine to kernel-mode readers — the loss is portcls user-buffer handling, not our
// miniport Read contract.
func TestRaveMIDIKernelTapDelivery(t *testing.T) {
	if !raveMIDIAvailable() {
		t.Skip("ravemidi control device not available")
	}
	opA, err := openRaveMIDIOut("ravemidi tapsrc")
	if err != nil {
		t.Fatalf("create port A: %v", err)
	}
	rpA := opA.(*RaveMIDIOut)
	defer rpA.Close()
	opB, err := openRaveMIDIOut("ravemidi tapdst")
	if err != nil {
		t.Fatalf("create port B: %v", err)
	}
	rpB := opB.(*RaveMIDIOut)
	defer rpB.Close()

	symUser, err := findPortSymlink(rpA.portID)
	if err != nil {
		t.Fatalf("find interface: %v", err)
	}
	symKernel := symUser
	if strings.HasPrefix(symUser, `\\?\`) {
		symKernel = `\??\` + symUser[4:]
	}

	// RAVEMIDI_CREATE_MIRROR_IN: Version, OutputCount, OutputPortIds[4], WCHAR[256].
	in := make([]byte, 4+4+16+512)
	binary.LittleEndian.PutUint32(in[0:], raveMIDIProtocolVersion)
	binary.LittleEndian.PutUint32(in[4:], 1)
	binary.LittleEndian.PutUint32(in[8:], rpB.portID)
	rmPutWstr(in, 24, 256, symKernel)
	out := make([]byte, 4)
	// Issue on port A's persistent ctl handle: rmIoctl's throwaway handle would
	// destroy the mirror at its CLOSE.
	var ret uint32
	ioctlCreateMirror := raveMIDICtl(0x804, fileReadData|fileWriteData)
	if err := syscall.DeviceIoControl(rpA.h, ioctlCreateMirror,
		&in[0], uint32(len(in)), &out[0], uint32(len(out)), &ret, nil); err != nil {
		t.Fatalf("CREATE_MIRROR(source=%s): %v", symKernel, err)
	}
	mirrorID := binary.LittleEndian.Uint32(out)
	t.Logf("mirror %d: %s -> port %d", mirrorID, symKernel, rpB.portID)
	defer func() {
		ref := make([]byte, 4)
		binary.LittleEndian.PutUint32(ref, mirrorID)
		var r2 uint32
		_ = syscall.DeviceIoControl(rpA.h, raveMIDICtl(0x805, fileReadData|fileWriteData),
			&ref[0], 4, nil, 0, &r2, nil)
	}()

	time.Sleep(300 * time.Millisecond) // tap pump up + pin RUN
	rpA.Send(0x90, 0x40, 0x7F)
	time.Sleep(300 * time.Millisecond)

	es, err := QueryDriverTrace(rpB.portID)
	if err != nil {
		t.Fatalf("QUERY_TRACE(B): %v", err)
	}
	got := false
	for _, e := range es {
		t.Logf("B trace: seq=%d dir=%d len=%d % X", e.Seq, e.Dir, e.Len, e.Bytes)
		if e.Dir == 1 && e.Len == 3 && len(e.Bytes) >= 3 &&
			e.Bytes[0] == 0x90 && e.Bytes[1] == 0x40 && e.Bytes[2] == 0x7F {
			got = true
		}
	}
	if got {
		t.Logf("kernel-mode KS reader received the bytes (portcls delivers to kernel clients)")
	} else {
		t.Errorf("kernel tap did NOT receive the bytes — kernel KS read path also broken")
	}
}
