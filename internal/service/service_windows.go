//go:build windows

package service

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"rave.page/mate/internal/debuglog"
)

// Install registers the Windows Service (auto-start, runs `<exe> --service`). Needs an
// elevated/admin context - the SCM rejects CreateService otherwise.
func Install() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM (run elevated?): %w", err)
	}
	defer func() { _ = m.Disconnect() }()
	if s, err := m.OpenService(Name); err == nil {
		_ = s.Close()
		return fmt.Errorf("service %q already installed", Name)
	}
	s, err := m.CreateService(Name, exe, mgr.Config{
		DisplayName:      DisplayName,
		Description:      Description,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}, "--service")
	if err != nil {
		return err
	}
	defer s.Close()
	// Start now - without this the service sits "stopped" until the next boot, which reads as
	// a broken install. NOTE: the service and the desktop app share the single-instance port;
	// if the desktop app is running the service start exits immediately (by design - one of
	// them owns the machine). Surface that instead of failing the install.
	if err := s.Start(); err != nil {
		return fmt.Errorf("installed, but start failed (is the desktop app running? service + desktop app can't run at once): %w", err)
	}
	return nil
}

// Uninstall deletes the Windows Service.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM (run elevated?): %w", err)
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(Name)
	if err != nil {
		return fmt.Errorf("service %q not installed", Name)
	}
	defer s.Close()
	return s.Delete()
}

// Status reports the SCM service state. Uses a read-only SCM handle so it works without
// elevation (install/uninstall still need admin - they modify a machine service).
func Status() (string, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseServiceHandle(scm) }()
	namePtr, err := windows.UTF16PtrFromString(Name)
	if err != nil {
		return "", err
	}
	h, err := windows.OpenService(scm, namePtr, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return "not installed", nil
	}
	s := &mgr.Service{Name: Name, Handle: h}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "", err
	}
	return stateString(st.State), nil
}

func stateString(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("state(%d)", s)
	}
}

// scmHandler bridges the SCM control protocol to the daemon run func: it runs and stops
// when the SCM sends Stop/Shutdown (Windows services aren't signalled with SIGTERM). The
// run func is injected (main passes app.RunCtx) so this package stays free of the app dep.
type scmHandler struct {
	run func(ctx context.Context) error
}

func (h scmHandler) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		// close(done) deferred first so it runs even if h.run panics; recover keeps the panic
		// off the SCM dispatcher goroutine.
		defer close(done)
		defer debuglog.Recover(nil, "service", false)
		_ = h.run(ctx)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
				}
				return false, 0
			}
		case <-done:
			return false, 0
		}
	}
}

// RunWindowsServiceIfNeeded runs the SCM handler when the process was launched by the
// Service Control Manager, using run as the daemon body. Reports whether it handled the
// run; returns false for a normal (interactive) launch so main continues.
func RunWindowsServiceIfNeeded(run func(ctx context.Context) error) bool {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return false
	}
	_ = svc.Run(Name, scmHandler{run: run})
	return true
}
