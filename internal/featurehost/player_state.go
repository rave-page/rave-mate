package featurehost

// State is the player engine snapshot: the child marshals it over the wire and the daemon proxy
// keeps it as the live mirror. Was internal/audioengine.State before the beep engine was retired.
type State struct {
	Path    string  `json:"path"`
	Playing bool    `json:"playing"`
	Paused  bool    `json:"paused"`
	Cur     float64 `json:"cur"`
	Total   float64 `json:"total"`
}
