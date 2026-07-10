//go:build windows

package midi

// Managed-input config plane of the ravemidi driver (driver/ravemidi/ioctl.h):
// SET_CONFIG persists kernel-side + applies live, so forwarding survives rave-mate
// exit AND reboots; QUERY_INPUT surfaces live bind state. Wire structs are packed
// (pack(1)) - encoded by hand below, byte-for-byte with the C layout.

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unicode/utf16"
)

const (
	rmMaxName      = raveMIDIMaxName // 32 WCHARs incl NUL
	rmMaxIface     = 256             // WCHARs
	rmMaxInputs    = 8
	rmMaxMirrorOut = 4

	rmInputCfgSize = (rmMaxName*3+rmMaxIface)*2 + 3*4 + rmMaxMirrorOut*rmMaxName*2 // 972
	rmConfigSize   = 8 + rmMaxInputs*rmInputCfgSize                                // 7784
	rmStatusSize   = rmMaxName*2*2 + 3*4 + rmMaxIface*2 + 2*4 + rmMaxMirrorOut*4   // 676
)

var (
	ioctlRaveMIDISetConfig  = raveMIDICtl(0x807, fileReadData|fileWriteData)
	ioctlRaveMIDIGetConfig  = raveMIDICtl(0x808, fileReadData)
	ioctlRaveMIDIQueryInput = raveMIDICtl(0x809, fileReadData)
	ioctlRaveMIDIReloadCfg  = raveMIDICtl(0x80A, fileReadData|fileWriteData)
)

func rmPutWstr(b []byte, off, maxW int, s string) {
	u := utf16.Encode([]rune(s))
	if len(u) > maxW-1 {
		u = u[:maxW-1]
	}
	for i, w := range u {
		binary.LittleEndian.PutUint16(b[off+i*2:], w)
	}
}

func rmGetWstr(b []byte, off, maxW int) string {
	var u []uint16
	for i := 0; i < maxW; i++ {
		w := binary.LittleEndian.Uint16(b[off+i*2:])
		if w == 0 {
			break
		}
		u = append(u, w)
	}
	return string(utf16.Decode(u))
}

// SetDriverConfig persists + applies the managed-input set.
func SetDriverConfig(inputs []DriverInputCfg) error {
	if len(inputs) > rmMaxInputs {
		return fmt.Errorf("too many managed inputs (max %d)", rmMaxInputs)
	}
	buf := make([]byte, rmConfigSize)
	binary.LittleEndian.PutUint32(buf[0:], 1) // RAVEMIDI_PROTOCOL_VERSION
	binary.LittleEndian.PutUint32(buf[4:], uint32(len(inputs)))
	for i, in := range inputs {
		if len(in.OutNames) > rmMaxMirrorOut {
			return fmt.Errorf("input %q: too many out ports (max %d)", in.ID, rmMaxMirrorOut)
		}
		o := 8 + i*rmInputCfgSize
		rmPutWstr(buf, o, rmMaxName, in.ID)
		rmPutWstr(buf, o+rmMaxName*2, rmMaxName, in.Name)
		rmPutWstr(buf, o+rmMaxName*4, rmMaxName, in.SourceMatch)
		rmPutWstr(buf, o+rmMaxName*6, rmMaxIface, in.SourceIface)
		n := o + rmMaxName*6 + rmMaxIface*2
		binary.LittleEndian.PutUint32(buf[n:], b2u(in.Thru))
		binary.LittleEndian.PutUint32(buf[n+4:], b2u(in.Feedback))
		binary.LittleEndian.PutUint32(buf[n+8:], uint32(len(in.OutNames)))
		for j, on := range in.OutNames {
			rmPutWstr(buf, n+12+j*rmMaxName*2, rmMaxName, on)
		}
	}
	return rmIoctl(ioctlRaveMIDISetConfig, buf, nil)
}

// GetDriverConfig reads the persisted managed-input set.
func GetDriverConfig() ([]DriverInputCfg, error) {
	buf := make([]byte, rmConfigSize)
	if err := rmIoctl(ioctlRaveMIDIGetConfig, nil, buf); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(buf[4:]))
	if n > rmMaxInputs {
		n = rmMaxInputs
	}
	out := make([]DriverInputCfg, 0, n)
	for i := 0; i < n; i++ {
		o := 8 + i*rmInputCfgSize
		f := o + rmMaxName*6 + rmMaxIface*2
		in := DriverInputCfg{
			ID:          rmGetWstr(buf, o, rmMaxName),
			Name:        rmGetWstr(buf, o+rmMaxName*2, rmMaxName),
			SourceMatch: rmGetWstr(buf, o+rmMaxName*4, rmMaxName),
			SourceIface: rmGetWstr(buf, o+rmMaxName*6, rmMaxIface),
			Thru:        binary.LittleEndian.Uint32(buf[f:]) != 0,
			Feedback:    binary.LittleEndian.Uint32(buf[f+4:]) != 0,
		}
		oc := int(binary.LittleEndian.Uint32(buf[f+8:]))
		if oc > rmMaxMirrorOut {
			oc = rmMaxMirrorOut
		}
		for j := 0; j < oc; j++ {
			in.OutNames = append(in.OutNames, rmGetWstr(buf, f+12+j*rmMaxName*2, rmMaxName))
		}
		out = append(out, in)
	}
	return out, nil
}

// QueryDriverInputs enumerates live bind status for every managed input.
func QueryDriverInputs() ([]DriverInputStatus, error) {
	var out []DriverInputStatus
	for i := 0; i < rmMaxInputs; i++ {
		in := make([]byte, 4)
		binary.LittleEndian.PutUint32(in, uint32(i))
		buf := make([]byte, rmStatusSize)
		if err := rmIoctl(ioctlRaveMIDIQueryInput, in, buf); err != nil {
			if i == 0 {
				return nil, err
			}
			break // NO_MORE_ENTRIES (or any terminal error) past the live set
		}
		f := rmMaxName * 4
		st := DriverInputStatus{
			ID:            rmGetWstr(buf, 0, rmMaxName),
			Name:          rmGetWstr(buf, rmMaxName*2, rmMaxName),
			Bound:         binary.LittleEndian.Uint32(buf[f:]) != 0,
			FeedbackBound: binary.LittleEndian.Uint32(buf[f+4:]) != 0,
			RetryCount:    binary.LittleEndian.Uint32(buf[f+8:]),
			BoundIface:    rmGetWstr(buf, f+12, rmMaxIface),
		}
		p := f + 12 + rmMaxIface*2
		st.ReservedPortID = binary.LittleEndian.Uint32(buf[p:])
		oc := int(binary.LittleEndian.Uint32(buf[p+4:]))
		if oc > rmMaxMirrorOut {
			oc = rmMaxMirrorOut
		}
		for j := 0; j < oc; j++ {
			st.OutPortIDs = append(st.OutPortIDs, binary.LittleEndian.Uint32(buf[p+8+j*4:]))
		}
		if st.ID == "" {
			break
		}
		out = append(out, st)
	}
	return out, nil
}

// ReloadDriverConfig re-applies the persisted config (manual reload).
func ReloadDriverConfig() error { return rmIoctl(ioctlRaveMIDIReloadCfg, nil, nil) }

// rmIoctl issues one DeviceIoControl against the ravemidi control device.
func rmIoctl(code uint32, in, out []byte) error {
	h, err := openRaveMIDICtl()
	if err != nil {
		return err
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var inPtr, outPtr *byte
	if len(in) > 0 {
		inPtr = &in[0]
	}
	if len(out) > 0 {
		outPtr = &out[0]
	}
	var ret uint32
	return syscall.DeviceIoControl(h, code,
		inPtr, uint32(len(in)), outPtr, uint32(len(out)), &ret, nil)
}

func b2u(v bool) uint32 {
	if v {
		return 1
	}
	return 0
}
