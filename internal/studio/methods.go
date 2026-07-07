package studio

// studioMethods is the RPC registry (1:1 with the Electron preload surface / methods.ts).
// Advertised verbatim in handshake-ok.capabilities so the web client's method gating is
// identical regardless of which desktop (Electron or rave-mate) it connects to.
var studioMethods = []string{
	// localMedia
	"localMedia.listDirectory",
	"localMedia.getDefaults",
	"localMedia.probe",
	"localMedia.probeStreams",
	"localMedia.moveTo",
	"localMedia.rememberRecent",
	"localMedia.listFavorites",
	"localMedia.addFavorite",
	"localMedia.removeFavorite",
	"localMedia.listPresets",
	"localMedia.savePreset",
	"localMedia.deletePreset",
	"localMedia.chooseSavePath",
	"localMedia.pickDirectory",
	"localMedia.pickFile",
	// transcode
	"transcode.listEncoders",
	"transcode.encoderCatalog",
	"transcode.probeDuration",
	"transcode.start",  // streaming
	"transcode.attach", // streaming
	"transcode.cancel",
	// automations
	"automations.list",
	"automations.create",
	"automations.update",
	"automations.delete",
	"automations.setEnabled",
	"automations.setBackgroundCredentials",
	"automations.runOnce",   // streaming
	"automations.runManual", // streaming
	"automations.commitStep",
	"automations.skipStep",
	"automations.abortRun",
	"automations.probeSilence",
	"automations.listEvents",
	"automations.subscribe", // streaming
}

// streamingMethods open a server→client notification stream.
var streamingMethods = map[string]bool{
	"transcode.start":       true,
	"transcode.attach":      true,
	"automations.runOnce":   true,
	"automations.runManual": true,
	"automations.subscribe": true,
}

var methodSet = func() map[string]bool {
	m := make(map[string]bool, len(studioMethods))
	for _, s := range studioMethods {
		m[s] = true
	}
	return m
}()

func isStudioMethod(v string) bool { return methodSet[v] }
