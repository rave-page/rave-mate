//go:build windows

package midi

import (
	"syscall"
	"testing"
)

// rmSeamReset swaps in scripted open/ioctl/close seams and restores them after the test.
func rmSeamReset(t *testing.T) (*int, *[]error) {
	t.Helper()
	origOpen, origClose, origIoctl := rmOpenCtl, rmCloseCtl, rmDevIoctl
	origH := rmCtlH
	rmCtlH = syscall.InvalidHandle
	t.Cleanup(func() {
		rmOpenCtl, rmCloseCtl, rmDevIoctl = origOpen, origClose, origIoctl
		rmCtlH = origH
	})
	opens := 0
	var script []error // popped per ioctl call; empty = success
	rmOpenCtl = func() (syscall.Handle, error) {
		opens++
		return syscall.Handle(0x10 + opens), nil
	}
	rmCloseCtl = func(syscall.Handle) {}
	rmDevIoctl = func(_ syscall.Handle, _ uint32, _, _ []byte) error {
		if len(script) == 0 {
			return nil
		}
		e := script[0]
		script = script[1:]
		return e
	}
	return &opens, &script
}

// TestRmIoctlCachesHandle proves the WP-8 fix: repeated ioctls reuse ONE open (no per-call
// open/close), and protocol errors (NO_MORE_ITEMS from QueryDriverInputs' probe loop) do
// NOT churn the handle.
func TestRmIoctlCachesHandle(t *testing.T) {
	opens, script := rmSeamReset(t)
	for i := 0; i < 10; i++ {
		if err := rmIoctl(0x1, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if *opens != 1 {
		t.Errorf("10 ioctls used %d opens, want 1", *opens)
	}
	// protocol error (ERROR_NO_MORE_ITEMS): surfaced, handle kept
	*script = []error{syscall.Errno(259)}
	if err := rmIoctl(0x1, nil, nil); err == nil {
		t.Fatal("protocol error must surface")
	}
	if err := rmIoctl(0x1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if *opens != 1 {
		t.Errorf("protocol error churned the handle: %d opens, want 1", *opens)
	}
}

// TestRmIoctlReopensDeadHandle proves reopen-once-and-retry on a dead handle
// (driver restart) and bounded failure when the fresh handle dies too.
func TestRmIoctlReopensDeadHandle(t *testing.T) {
	opens, script := rmSeamReset(t)
	if err := rmIoctl(0x1, nil, nil); err != nil {
		t.Fatal(err)
	}
	// dead (DEVICE_NOT_CONNECTED) then success on the fresh handle → transparent recovery
	*script = []error{syscall.Errno(1167)}
	if err := rmIoctl(0x1, nil, nil); err != nil {
		t.Fatalf("expected transparent reopen, got %v", err)
	}
	if *opens != 2 {
		t.Errorf("reopen count %d, want 2", *opens)
	}
	// dead twice in a row → bounded: surfaces the error, no retry storm
	*script = []error{syscall.Errno(6), syscall.Errno(6)}
	if err := rmIoctl(0x1, nil, nil); err == nil {
		t.Fatal("persistent dead handle must surface an error")
	}
	if *opens != 3 {
		t.Errorf("open count %d, want 3 (one reopen per call, not a loop)", *opens)
	}
}
