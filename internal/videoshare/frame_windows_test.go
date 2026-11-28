//go:build windows

package videoshare

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestSenderFrameReadsSpoutSemaphore drives the exact sender protocol (create count 1, then
// Wait(0)+Release(2) per frame) and asserts the read is the count and never perturbs it.
func TestSenderFrameReadsSpoutSemaphore(t *testing.T) {
	const name = "rave-mate videoshare frame gate"
	if _, _, ok := SenderFrame(name); ok {
		t.Fatal("counter reported before the sender created it")
	}
	create := k32frame.NewProc("CreateSemaphoreW")
	release := k32frame.NewProc("ReleaseSemaphore")
	p, _ := windows.UTF16PtrFromString(name + "_Count_Semaphore")
	h, _, err := create.Call(0, 1, 0x7fffffff, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		t.Fatalf("CreateSemaphoreW: %v", err)
	}
	defer func() { _ = windows.CloseHandle(windows.Handle(h)) }()
	frame := func() {
		_, _ = windows.WaitForSingleObject(windows.Handle(h), 0)
		_, _, _ = release.Call(h, 2, 0)
	}
	for want := int64(1); want <= 4; want++ {
		for i := 0; i < 3; i++ { // repeated reads: read-only, the count must not move
			f, _, ok := SenderFrame(name)
			if !ok || f != want {
				t.Fatalf("read %d: frame=%d ok=%v, want %d", i, f, ok, want)
			}
		}
		frame()
	}
}
