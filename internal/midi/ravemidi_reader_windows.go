//go:build windows

package midi

// Driver-backed Input for managed reserved ports. Protocol v3 makes the per-input
// reserved port INTERNAL: it has no winmm/KS presence (so DJ software only ever sees
// the THRU fan-out), and rave-mate reads it with pended IOCTL_READ on the control
// device instead of midiInOpen. The reader is self-healing: it re-resolves the port
// id whenever a read fails (the managed engine recreates ports on config change).

import (
	"encoding/binary"
	"strings"
	"sync"
	"syscall"
	"time"
)

var procCancelIoEx = syscall.NewLazyDLL("kernel32.dll").NewProc("CancelIoEx")

// tryOpenDriverInput resolves substr against the managed inputs' reserved-port
// names ("<Name> (rave-mate)") and, on a match, returns a driver-backed Input.
// ok=false → caller falls back to winmm enumeration.
func tryOpenDriverInput(substr string) (*Input, bool) {
	if substr == "" || !raveMIDIAvailable() {
		return nil, false
	}
	sts, err := QueryDriverInputs()
	if err != nil {
		return nil, false
	}
	for _, st := range sts {
		if strings.EqualFold(ReservedPortName(st.Name), substr) {
			in := &Input{Name: ReservedPortName(st.Name), ch: make(chan Message, 256)}
			r := &driverReader{in: in, cfgID: st.ID}
			in.stop = r.stop
			go r.run()
			return in, true
		}
	}
	return nil, false
}

// driverReader pumps one reserved port. One goroutine per Input; exits on Close.
type driverReader struct {
	in    *Input
	cfgID string
	mu    sync.Mutex // guards h for stop()'s CancelIoEx vs run()'s CloseHandle
	h     syscall.Handle
}

// stop cancels the in-flight pended IOCTL_READ so run() unblocks and exits
// (in.closed is already set by Input.Close).
func (r *driverReader) stop() {
	r.mu.Lock()
	if r.h != 0 && r.h != syscall.InvalidHandle {
		_, _, _ = procCancelIoEx.Call(uintptr(r.h), 0)
	}
	r.mu.Unlock()
}

// resolve maps the managed-input config id to its live reserved-port id (0 = pending).
func (r *driverReader) resolve() uint32 {
	sts, err := QueryDriverInputs()
	if err != nil {
		return 0
	}
	for _, st := range sts {
		if st.ID == r.cfgID {
			return st.ReservedPortID
		}
	}
	return 0
}

func (r *driverReader) run() {
	var fr framer
	deliver := func(m Message) {
		if r.in.closed.Load() {
			return
		}
		if fn := r.in.thru.Load(); fn != nil {
			(*fn)(m)
		}
		select {
		case r.in.ch <- m:
		default: // drop if the consumer is behind — never stall the reader
		}
	}
	for !r.in.closed.Load() {
		portID := r.resolve()
		if portID == 0 { // ports pending (driver still binding) — retry
			time.Sleep(time.Second)
			continue
		}
		h, err := openRaveMIDICtl()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		r.mu.Lock()
		r.h = h
		r.mu.Unlock()
		var ref [4]byte
		binary.LittleEndian.PutUint32(ref[:], portID)
		buf := make([]byte, 512)
		for !r.in.closed.Load() {
			var ret uint32
			// pends in the driver until tap data arrives; CancelIoEx (stop) or a
			// port destroy (managed reapply) completes it with an error
			err := syscall.DeviceIoControl(h, ioctlRaveMIDIRead,
				&ref[0], uint32(len(ref)), &buf[0], uint32(len(buf)), &ret, nil)
			if err != nil {
				break
			}
			if ret > 0 {
				fr.feed(buf[:ret], deliver)
			}
		}
		r.mu.Lock()
		r.h = 0
		r.mu.Unlock()
		_ = syscall.CloseHandle(h)
		if r.in.closed.Load() {
			return
		}
		fr = framer{}                      // port swap: never resume running status across it
		time.Sleep(300 * time.Millisecond) // config change recreated the port — re-resolve
	}
}
