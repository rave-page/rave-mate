//go:build windows

package senderkeeper

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"

	"rave.page/mate/internal/appdir"
)

// ExeName is the keeper sidecar beside rave-mate.exe.
const ExeName = "rave-mate-keeper.exe"

// PipeName is the keeper's control pipe for this logon session (the keeper derives the same name).
func PipeName() string {
	var sid uint32
	_ = windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid)
	return fmt.Sprintf(`\\.\pipe\rave-mate-keeper-%d`, sid)
}

// DialPipe connects to the running keeper (no start).
func DialPipe() (Conn, error) {
	p, err := windows.UTF16PtrFromString(PipeName())
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	return NewConn(os.NewFile(uintptr(h), PipeName())), nil
}

// StartDetached spawns the keeper so it OUTLIVES this process: detached, own process group, no window,
// broken away from any job (falls back without breakaway when the job forbids it - then the keeper
// only lives as long as that job, which is why only the daemon - not in a job - should call this).
func StartDetached() error {
	exe := appdir.SidecarPath(ExeName)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("keeper sidecar missing: %w", err)
	}
	const detached, breakaway = 0x00000008, 0x01000000 // DETACHED_PROCESS, CREATE_BREAKAWAY_FROM_JOB
	try := func(flags uint32) error {
		cmd := exec.Command(exe)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true,
			CreationFlags: flags | windows.CREATE_NEW_PROCESS_GROUP}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release() // never waited: the keeper is not our child in any lifecycle sense
	}
	if err := try(detached | breakaway); err == nil {
		return nil
	}
	return try(detached)
}
