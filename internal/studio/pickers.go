package studio

import (
	"context"
	"fmt"
	"time"
)

// pickerCall serves localMedia.pickDirectory/pickFile/chooseSavePath via the native-dialog
// provider (the UI). Runs in a goroutine (the dialog blocks on the user). Cancel → null
// result (no error), matching the Electron preload contract.
func (s *session) pickerCall(method, reqID string, p map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			s.srv.log.Warn(source, "picker panic", map[string]any{"method": method, "panic": fmt.Sprint(r)})
			s.sendErr(reqID, errInternal, "internal error")
		}
	}()
	pk := s.srv.getPicker()
	if pk == nil {
		s.sendErr(reqID, errInternal, "no desktop UI for native dialog")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var path string
	var err error
	switch method {
	case "localMedia.pickDirectory":
		path, err = pk.PickDirectory(ctx)
	case "localMedia.pickFile":
		path, err = pk.PickFile(ctx)
	case "localMedia.chooseSavePath":
		path, err = pk.ChooseSavePath(ctx, asString(p["defaultPath"]), asString(p["container"]))
	}
	if err != nil {
		s.sendErr(reqID, errFS, err.Error())
		return
	}
	var result any // null on cancel ("")
	if path != "" {
		result = path
	}
	s.send(map[string]any{"t": "res", "id": reqID, "ok": true, "result": result})
}
