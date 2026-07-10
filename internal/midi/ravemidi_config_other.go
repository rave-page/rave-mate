//go:build !windows

package midi

// Managed-input config plane stubs: the ravemidi driver is Windows-only.

func SetDriverConfig([]DriverInputCfg) error { return ErrUnsupported }

func GetDriverConfig() ([]DriverInputCfg, error) { return nil, ErrUnsupported }

func QueryDriverInputs() ([]DriverInputStatus, error) { return nil, ErrUnsupported }

func ReloadDriverConfig() error { return ErrUnsupported }
