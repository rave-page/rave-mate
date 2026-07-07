package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/transcode"
)

// Transcode streaming wiring: maps the web `TranscodeJob` (electron/src/shared/transcodeTypes.ts)
// onto a transcode.Preset, drives the job through the jobs.Hub, and translates worker
// progress frames into the web `ProgressUpdate` stream contract (transcode-progress notify
// + stream-end + terminal res). Frame shapes are byte-exact with studioWsServer.ts.

// startTranscode begins a job and streams progress to this subscriber. params: {jobId, job}.
func (s *session) startTranscode(reqID string, p map[string]any) {
	if s.srv.hub == nil {
		s.sendErr(reqID, errInternal, "transcode unavailable (no worker supervisor)")
		return
	}
	jobID := asString(p["jobId"])
	if jobID == "" {
		s.sendErr(reqID, errBadRequest, "missing jobId")
		return
	}
	runParams, err := mapTranscodeJob(asMap(p["job"]))
	if err != nil {
		s.sendErr(reqID, errBadRequest, err.Error())
		return
	}
	h := s.srv.hub.Start(jobID, runParams, s.onTcProgress(reqID), s.onTcEnd(reqID, jobID))
	s.registerSub(reqID, &subRec{handle: h, jobID: jobID})
}

// attachTranscode re-subscribes to a running/just-finished job, replaying buffered progress.
// params: {jobId}.
func (s *session) attachTranscode(reqID string, p map[string]any) {
	if s.srv.hub == nil {
		s.sendErr(reqID, errInternal, "transcode unavailable (no worker supervisor)")
		return
	}
	jobID := asString(p["jobId"])
	att := s.srv.hub.Attach(jobID, s.onTcProgress(reqID), s.onTcEnd(reqID, jobID))
	if !att.Found {
		s.sendErr(reqID, errNotFound, "Job no longer available")
		return
	}
	s.registerSub(reqID, &subRec{handle: att.Handle, jobID: jobID})
	onProgress := s.onTcProgress(reqID)
	for _, f := range att.Buffer {
		onProgress(f.Event, f.Data)
	}
	if att.Done != nil { // already finished - the hub won't re-fire onEnd for a late sub
		s.endStream(reqID, jobID, *att.Done)
	}
}

// onTcProgress translates one worker "progress" frame into a transcode-progress notify.
func (s *session) onTcProgress(reqID string) jobs.Progress {
	return func(event string, data json.RawMessage) {
		if event != "progress" {
			return
		}
		var w struct {
			Percent *float64 `json:"percent"`
			TimeSec *float64 `json:"timeSec"`
		}
		_ = json.Unmarshal(data, &w)
		pu := map[string]any{"state": "running"}
		if w.Percent != nil {
			pu["percent"] = *w.Percent
		}
		if w.TimeSec != nil {
			pu["time"] = *w.TimeSec
		}
		s.notifyStream(reqID, "transcode-progress", pu)
	}
}

// onTcEnd fires once when the hub job terminates (completion / error / cancel).
func (s *session) onTcEnd(reqID, jobID string) jobs.End {
	return func(r jobs.EndResult) { s.endStream(reqID, jobID, r) }
}

// endStream emits the terminal stream-end notify + res/err, then drops the subscription.
func (s *session) endStream(reqID, jobID string, r jobs.EndResult) {
	if !s.takeSub(reqID) { // already ended (idempotent across cancel + onEnd races)
		return
	}
	state := "completed"
	switch {
	case r.Canceled:
		state = "cancelled"
	case !r.OK:
		state = "error"
	}
	endPayload := map[string]any{"state": state}
	if r.Error != "" {
		endPayload["message"] = r.Error
	}
	s.notifyStream(reqID, "stream-end", endPayload)
	switch {
	case r.OK:
		s.send(map[string]any{"t": "res", "id": reqID, "ok": true, "result": map[string]any{"jobId": jobID}})
	case r.Canceled:
		s.sendErr(reqID, errCancelled, "canceled")
	default:
		msg := r.Error
		if msg == "" {
			msg = "failed"
		}
		s.sendErr(reqID, errFFmpeg, msg)
	}
}

// ── unary worker calls (encoder detect / duration probe) ─────────────────────

// detectedEnc is one entry of the worker transcode.detect result (decoded wire-only, no
// cross-package type coupling).
type detectedEnc struct {
	Name    string `json:"name"`
	Codec   string `json:"codec"` // h264|hevc|av1|aac|opus
	Kind    string `json:"kind"`  // sw|hw
	Vendor  string `json:"vendor"`
	Audio   bool   `json:"audio"`
	Working bool   `json:"working"` // a real test encode succeeded
}

// transcodeUnary serves the non-streaming transcode methods off the read loop.
func (s *session) transcodeUnary(method, reqID string, p map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			s.srv.log.Warn(source, "transcode unary panic", map[string]any{"method": method, "panic": fmt.Sprint(r)})
			s.sendErr(reqID, errInternal, "internal error")
		}
	}()
	if s.srv.runner == nil {
		s.sendErr(reqID, errInternal, "transcode unavailable (no worker supervisor)")
		return
	}
	switch method {
	case "transcode.probeDuration":
		secs, err := s.probeDurationSecs(asString(p["path"]))
		if err != nil {
			s.sendErr(reqID, errFFmpeg, err.Error())
			return
		}
		s.send(map[string]any{"t": "res", "id": reqID, "ok": true, "result": secs})
	case "transcode.listEncoders":
		cat, err := s.detectCatalog()
		if err != nil {
			s.sendErr(reqID, errFFmpeg, err.Error())
			return
		}
		s.send(map[string]any{"t": "res", "id": reqID, "ok": true, "result": cat["video"]})
	case "transcode.encoderCatalog":
		cat, err := s.detectCatalog()
		if err != nil {
			s.sendErr(reqID, errFFmpeg, err.Error())
			return
		}
		s.send(map[string]any{"t": "res", "id": reqID, "ok": true, "result": cat})
	}
}

func (s *session) callWorker(typ, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return s.srv.runner.RunStream(ctx, typ, method, params, nil)
}

func (s *session) probeDurationSecs(path string) (float64, error) {
	if path == "" {
		return 0, errMissing("path")
	}
	raw, err := s.callWorker("probe", "probe.duration", map[string]any{"path": path})
	if err != nil {
		return 0, err
	}
	var r struct {
		DurationSeconds *float64 `json:"durationSeconds"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.DurationSeconds == nil {
		return 0, nil
	}
	return *r.DurationSeconds, nil
}

// detectCatalog runs the worker test-encode detection and shapes it into the web
// EncoderCatalog. `available` reflects the REAL test-encode result (not just build
// presence) - the reliability win over the Electron parse-only path.
func (s *session) detectCatalog() (map[string]any, error) {
	raw, err := s.callWorker("transcode", "transcode.detect", nil)
	if err != nil {
		return nil, err
	}
	var d struct {
		Encoders []detectedEnc `json:"encoders"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return buildCatalog(d.Encoders), nil
}

// buildCatalog maps worker detect output → web EncoderCatalog (transcodeTypes.ts).
func buildCatalog(encs []detectedEnc) map[string]any {
	video := []map[string]any{}
	audio := []map[string]any{}
	recHW := map[string]string{}
	recSW := map[string]string{}
	for _, e := range encs {
		if e.Audio {
			audio = append(audio, map[string]any{
				"name": e.Name, "description": encDescription(e),
				"codec": audioCodecOf(e.Codec), "available": e.Working,
			})
			continue
		}
		fam := familyOf(e.Codec)
		video = append(video, map[string]any{
			"name": e.Name, "description": encDescription(e), "family": fam,
			"kind": kindOf(e.Kind), "vendor": vendorOf(e.Vendor), "available": e.Working,
		})
		if e.Working && fam != "other" {
			if e.Kind == "hw" {
				if recHW[fam] == "" {
					recHW[fam] = e.Name
				}
			} else if recSW[fam] == "" {
				recSW[fam] = e.Name
			}
		}
	}
	recommended := map[string]any{}
	for _, fam := range []string{"h264", "h265", "av1", "vp9"} {
		if recHW[fam] != "" { // prefer a working hardware encoder per family
			recommended[fam] = recHW[fam]
		} else if recSW[fam] != "" {
			recommended[fam] = recSW[fam]
		}
	}
	return map[string]any{"recommended": recommended, "video": video, "audio": audio, "platform": platformOf()}
}

func familyOf(codec string) string {
	switch codec {
	case "h264":
		return "h264"
	case "hevc":
		return "h265"
	case "av1":
		return "av1"
	case "vp9":
		return "vp9"
	default:
		return "other"
	}
}

func audioCodecOf(codec string) string {
	switch codec {
	case "aac", "opus", "mp3":
		return codec
	default:
		return "aac"
	}
}

func kindOf(kind string) string {
	if kind == "hw" {
		return "hardware"
	}
	return "software"
}

func vendorOf(v string) string {
	switch v {
	case "nvidia", "intel", "amd", "apple", "vaapi":
		return v
	case "":
		return "cpu"
	default:
		return "other"
	}
}

func encDescription(e detectedEnc) string {
	kind := "software"
	if e.Kind == "hw" {
		kind = "hardware"
	}
	if e.Vendor != "" {
		return fmt.Sprintf("%s %s (%s)", e.Vendor, e.Codec, kind)
	}
	return fmt.Sprintf("%s (%s)", e.Codec, kind)
}

// platformOf maps Go's GOOS to the Node platform string the web client expects.
func platformOf() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS // darwin | linux
}

// ── sub registry ─────────────────────────────────────────────────────────────

func (s *session) registerSub(reqID string, rec *subRec) {
	s.subsMu.Lock()
	if s.subs == nil {
		s.subs = map[string]*subRec{}
	}
	s.subs[reqID] = rec
	s.subsMu.Unlock()
}

// takeSub removes + detaches the sub for reqID, returning true if it was present (so the
// terminal frames fire exactly once).
func (s *session) takeSub(reqID string) bool {
	s.subsMu.Lock()
	rec, ok := s.subs[reqID]
	if ok {
		delete(s.subs, reqID)
	}
	s.subsMu.Unlock()
	if ok {
		rec.release()
	}
	return ok
}

// release detaches the underlying job (if any) and runs the non-job unsub (if any).
func (r *subRec) release() {
	if r.handle != nil {
		r.handle.Detach()
	}
	if r.unsub != nil {
		r.unsub()
	}
}

// cancelSub handles a `cancel` frame: detach + cancel the underlying job (mirrors the
// Electron sub.cancel). The job's onEnd then drives the terminal frames.
func (s *session) cancelSub(reqID string) {
	s.subsMu.Lock()
	rec, ok := s.subs[reqID]
	s.subsMu.Unlock()
	if !ok {
		return
	}
	rec.release()
	if rec.jobID != "" && s.srv.hub != nil {
		s.srv.hub.Cancel(rec.jobID)
	}
}

// detachAllSubs releases every subscription on session teardown so orphaned jobs hit the
// hub's orphan-grace cancel.
func (s *session) detachAllSubs() {
	s.subsMu.Lock()
	recs := make([]*subRec, 0, len(s.subs))
	for _, r := range s.subs {
		recs = append(recs, r)
	}
	s.subs = nil
	s.subsMu.Unlock()
	for _, r := range recs {
		r.release()
	}
}

// mapTranscodeJob converts the web TranscodeJob into worker transcode.run params
// ({input, output, preset, trimStart, trimEnd}). Output is always the client-chosen path
// (a new file); the worker never modifies the input.
func mapTranscodeJob(job map[string]any) (map[string]any, error) {
	input := asString(job["inputPath"])
	output := asString(job["outputPath"])
	if input == "" || output == "" {
		return nil, errMissing("inputPath/outputPath")
	}
	video := asMap(job["video"])
	audio := asMap(job["audio"])

	p := transcode.Preset{
		ID:        "studio-builder",
		Label:     "Local Studio",
		Container: asString(job["container"]),
	}
	if p.Container == "" {
		p.Container = "mp4"
	}

	// video
	if asBool2(video["enabled"], true) {
		p.VideoCodec = asString(video["codec"])
		p.EncoderOverride = asString(video["encoder"])
		p.Accel = mapHwAccel(asString(video["hwaccel"]))
		if crf, ok := asInt(video["crf"]); ok {
			p.CRF = crf
		}
		if asBool(video["forceBitrate"]) {
			if bk, ok := asInt(video["bitrateK"]); ok {
				p.RateMode, p.BitrateK = "bitrate", bk
			}
		}
		if w, ok := asInt(video["width"]); ok {
			p.Width = w
		}
		if h, ok := asInt(video["height"]); ok {
			p.Height = h
		}
		if f, ok := asFloat(video["fps"]); ok {
			p.FPS = f
		}
		if g, ok := asFloat(video["keyintSeconds"]); ok {
			p.GOPSeconds = g
		}
		if t := asString(video["tune"]); t != "" && t != "none" {
			p.Tune = t
		}
		p.SpeedPreset = asString(video["speedPreset"])
	} else {
		p.VideoCodec = "none"
	}
	if asBool(job["deinterlace"]) {
		p.Deinterlace = true
	}

	// audio
	if asBool2(audio["enabled"], true) {
		if asBool(audio["passthrough"]) {
			p.AudioCodec = "copy"
		} else {
			p.AudioCodec = asString(audio["codec"])
			if bk, ok := asInt(audio["bitrateK"]); ok {
				p.AudioBitrateK = bk
			}
			if ch, ok := asInt(audio["channels"]); ok {
				p.Channels = ch
			}
			if sr, ok := asInt(audio["sampleRate"]); ok {
				p.SampleRate = sr
			}
			p.Loudness = mapLoudness(asString(audio["loudnessProfile"]))
		}
	} else {
		p.AudioCodec = "none"
	}

	out := map[string]any{"input": input, "output": output, "preset": p}
	trim := asMap(job["trim"])
	if asString(trim["mode"]) != "none" {
		if st, ok := asFloat(trim["start"]); ok {
			out["trimStart"] = st
		}
		if en, ok := asFloat(trim["end"]); ok {
			out["trimEnd"] = en
		}
	}
	return out, nil
}

// mapHwAccel maps the web HwAccel enum to the rave-mate Accel field ("off" → software).
func mapHwAccel(h string) string {
	switch h {
	case "", "auto":
		return "auto"
	case "off":
		return "software"
	default:
		return h // nvenc|qsv|vaapi|amf|videotoolbox
	}
}

// mapLoudness maps the web loudnessProfile enum to the rave-mate Loudness field.
func mapLoudness(profile string) string {
	switch profile {
	case "streaming":
		return "music-stream"
	case "master":
		return "music-master"
	case "broadcast":
		return "broadcast"
	case "speech":
		return "speech"
	default:
		return "off"
	}
}

type missingParamErr string

func (e missingParamErr) Error() string { return "missing " + string(e) }
func errMissing(field string) error     { return missingParamErr(field) }

// ── param coercion (wire numbers arrive as json.Number; bools/strings as-is) ──

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			f, ferr := n.Float64()
			if ferr != nil {
				return 0, false
			}
			return int(f), true
		}
		return int(i), true
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

// asBool2 returns the bool value of v, or def when v is absent/non-bool.
func asBool2(v any, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}
