package midi

// Managed-input config types for the ravemidi driver (portable; the Windows wire
// codec lives in ravemidi_config_windows.go).

// DriverInputCfg mirrors RAVEMIDI_INPUT_CFG.
type DriverInputCfg struct {
	ID          string
	Name        string
	SourceMatch string // substring vs device FriendlyName
	SourceIface string // exact KS symlink ("" = use SourceMatch)
	Thru        bool   // device capture → out ports
	Feedback    bool   // reserved-port app-writes → device render pin
	OutNames    []string
}

// DriverInputStatus mirrors RAVEMIDI_INPUT_STATUS.
type DriverInputStatus struct {
	ID, Name       string
	Bound          bool
	FeedbackBound  bool
	RetryCount     uint32
	BoundIface     string
	ReservedPortID uint32
	OutPortIDs     []uint32
}
