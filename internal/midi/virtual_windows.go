//go:build windows

package midi

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

// teVirtualMIDI one-way virtual port (the kernel driver loopMIDI installs; DLL lives in
// System32). Why: a loopMIDI/LoopBe cable is BIDIRECTIONAL - a DJ app opens both ends, and
// rekordbox auto-sends every indicator function's LED code back out the same-named output
// ("the same code as the MIDI IN will be sent automatically to the MIDI OUT", MIDI LEARN
// guide) - so on a cable the app hears its own echo and reacts to it (play flicker).
// TE_VM_FLAGS_INSTANTIATE_TX_ONLY creates ONLY the "midi-in" half: other apps see a MIDI
// INPUT port named VirtualDJPortName with NO matching output endpoint, so the echo has
// nowhere to loop. rave-mate injects data via virtualMIDISendData.
//
// Runtime-optional: the DLL is loaded from System32 at first use and NEVER redistributed
// (the user installs it with loopMIDI; absent driver = the option is hidden). NOTE the
// virtualMIDI SDK license requires clearance from Tobias Erichsen before DISTRIBUTING
// software that integrates it - see docs/MIDI_MAPPING.md.
var (
	vmOnce   sync.Once
	vmErr    error
	vmCreate *syscall.Proc // virtualMIDICreatePortEx2
	vmSend   *syscall.Proc // virtualMIDISendData
	vmClose  *syscall.Proc // virtualMIDIClosePort
)

const (
	teVMFlagParseTX           = 2 // driver validates the commands we inject
	teVMFlagInstantiateTXOnly = 8 // create only the "midi-in" half (apps see an input port)
	vmMaxSysex                = 65535
)

// vmDLLPath is the absolute System32 path (no search-path DLL planting).
func vmDLLPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "teVirtualMIDI64.dll")
}

// VirtualAvailable reports whether the teVirtualMIDI driver DLL is installed (ships with
// loopMIDI). Cheap stat - safe for UI gating in the daemon.
func VirtualAvailable() bool {
	_, err := os.Stat(vmDLLPath())
	return err == nil
}

func vmLoad() error {
	vmOnce.Do(func() {
		dll, err := syscall.LoadDLL(vmDLLPath())
		if err != nil {
			vmErr = fmt.Errorf("teVirtualMIDI driver not installed (install loopMIDI): %w", err)
			return
		}
		for _, p := range []struct {
			name string
			dst  **syscall.Proc
		}{
			{"virtualMIDICreatePortEx2", &vmCreate},
			{"virtualMIDISendData", &vmSend},
			{"virtualMIDIClosePort", &vmClose},
		} {
			proc, err := dll.FindProc(p.name)
			if err != nil {
				vmErr = fmt.Errorf("teVirtualMIDI: %w", err)
				return
			}
			*p.dst = proc
		}
	})
	return vmErr
}

// VirtualOut is a TX-only virtual MIDI port: other apps see it as an INPUT device; only
// rave-mate can inject data. Satisfies OutPort.
type VirtualOut struct {
	name   string
	mu     sync.Mutex
	handle uintptr
	closed bool
}

// OpenVirtualOut creates the one-way port. Fails when the driver is absent or the name is
// already claimed (another rave-mate process created it - use distinct names per process).
func OpenVirtualOut(name string) (*VirtualOut, error) {
	if err := vmLoad(); err != nil {
		return nil, err
	}
	pn, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, _, callErr := vmCreate.Call(
		uintptr(unsafe.Pointer(pn)),
		0, // no callback: TX-only, we never receive
		0,
		vmMaxSysex,
		teVMFlagParseTX|teVMFlagInstantiateTXOnly,
	)
	if h == 0 {
		return nil, fmt.Errorf("midi: virtualMIDICreatePortEx2(%q) failed: %v", name, callErr)
	}
	return &VirtualOut{name: name, handle: h}, nil
}

// Send injects one short message, sized by status (PARSE_TX rejects malformed commands).
func (v *VirtualOut) Send(status, data1, data2 byte) {
	buf := [3]byte{status, data1, data2}
	n := uintptr(3)
	switch {
	case status >= 0xF8 || status == 0xF6: // realtime / tune request
		n = 1
	case status&0xF0 == 0xC0 || status&0xF0 == 0xD0 || status == 0xF1 || status == 0xF3:
		n = 2
	}
	v.mu.Lock()
	if !v.closed {
		_, _, _ = vmSend.Call(v.handle, uintptr(unsafe.Pointer(&buf[0])), n)
	}
	v.mu.Unlock()
}

// Close destroys the port (idempotent - THRU + bridge may share one instance).
func (v *VirtualOut) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	v.closed = true
	_, _, _ = vmClose.Call(v.handle)
}

// PortName implements OutPort.
func (v *VirtualOut) PortName() string { return v.name }
