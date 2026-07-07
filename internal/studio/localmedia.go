package studio

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/store"
)

// localMedia methods that touch IO/persistence (probe, move, favorites, presets) - routed
// off the read loop via localMediaCall. Return shapes are byte-exact with the web preload
// contract in app/src/types/global.d.ts (RemoteStudioProxy passes results straight through):
// favorites/presets mutators return the FULL updated list; probe returns {durationSeconds};
// moveTo returns a {ok,...} union (never an error frame); rememberRecent returns string[].

const maxRecents = 20

func (s *session) localMediaCall(method, reqID string, p map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			s.srv.log.Warn(source, "localMedia panic", map[string]any{"method": method, "panic": fmt.Sprint(r)})
			s.sendErr(reqID, errInternal, "internal error")
		}
	}()
	switch method {
	case "localMedia.probe":
		s.reply(reqID, s.probeDuration(asString(p["path"])), "", nil)
	case "localMedia.probeStreams":
		probe, err := s.probeStreams(asString(p["path"]))
		s.reply(reqID, probe, errFFmpeg, err)
	case "localMedia.moveTo":
		// Returns a {ok:true,destPath} | {ok:false,error} union - no error frame.
		s.reply(reqID, moveTo(asString(p["src"]), asString(p["dest"]), asBool(p["overwrite"])), "", nil)
	case "localMedia.rememberRecent":
		s.reply(reqID, s.rememberRecent(asString(p["path"])), "", nil)
	case "localMedia.listFavorites":
		s.reply(reqID, s.listFavorites(), "", nil)
	case "localMedia.addFavorite":
		s.reply(reqID, s.addFavorite(p), "", nil)
	case "localMedia.removeFavorite":
		s.reply(reqID, s.removeFavorite(asString(p["id"])), "", nil)
	case "localMedia.listPresets":
		s.reply(reqID, map[string]any{"builtin": []any{}, "custom": s.listPresets()}, "", nil)
	case "localMedia.savePreset":
		s.savePreset(p)
		s.reply(reqID, s.listPresets(), "", nil)
	case "localMedia.deletePreset":
		if s.srv.store != nil && asString(p["id"]) != "" {
			_ = s.srv.store.Delete(store.BucketStudioPre, asString(p["id"]))
		}
		s.reply(reqID, s.listPresets(), "", nil)
	default:
		s.sendErr(reqID, errInternal, "unhandled localMedia method "+method)
	}
}

func (s *session) reply(reqID string, result any, code errorCode, err error) {
	if err != nil {
		s.sendErr(reqID, code, err.Error())
		return
	}
	s.send(map[string]any{"t": "res", "id": reqID, "ok": true, "result": result})
}

// ── probe ────────────────────────────────────────────────────────────────────

// probeDuration returns {durationSeconds: number|null} (web localMedia.probe contract).
func (s *session) probeDuration(path string) map[string]any {
	if path == "" || s.srv.runner == nil {
		return map[string]any{"durationSeconds": nil}
	}
	secs, err := s.probeDurationSecs(path)
	if err != nil || secs <= 0 {
		return map[string]any{"durationSeconds": nil}
	}
	return map[string]any{"durationSeconds": secs}
}

type ffStream struct {
	Index         int               `json:"index"`
	CodecType     string            `json:"codec_type"`
	CodecName     string            `json:"codec_name"`
	CodecLong     string            `json:"codec_long_name"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	RFrameRate    string            `json:"r_frame_rate"`
	BitRate       string            `json:"bit_rate"`
	PixFmt        string            `json:"pix_fmt"`
	Profile       string            `json:"profile"`
	Channels      int               `json:"channels"`
	ChannelLayout string            `json:"channel_layout"`
	SampleRate    string            `json:"sample_rate"`
	Tags          map[string]string `json:"tags"`
}

type ffFormat struct {
	FormatName string            `json:"format_name"`
	FormatLong string            `json:"format_long_name"`
	Duration   string            `json:"duration"`
	Size       string            `json:"size"`
	BitRate    string            `json:"bit_rate"`
	Tags       map[string]string `json:"tags"`
}

// probeStreams returns the web MediaProbe (format + typed streams), or an error.
func (s *session) probeStreams(path string) (map[string]any, error) {
	if path == "" {
		return nil, errMissing("path")
	}
	if s.srv.runner == nil {
		return nil, fmt.Errorf("probe unavailable (no worker supervisor)")
	}
	raw, err := s.callWorker("probe", "probe.streams", map[string]any{"path": path})
	if err != nil {
		return nil, err
	}
	var pr struct {
		Streams []ffStream `json:"streams"`
		Format  ffFormat   `json:"format"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("probe parse: %w", err)
	}
	format := map[string]any{"container": pr.Format.FormatName}
	putStr(format, "containerLong", pr.Format.FormatLong)
	putFloat(format, "durationSeconds", pr.Format.Duration)
	putInt(format, "sizeBytes", pr.Format.Size)
	putInt(format, "bitrateBps", pr.Format.BitRate)
	if len(pr.Format.Tags) > 0 {
		format["tags"] = pr.Format.Tags
	}
	streams := make([]map[string]any, 0, len(pr.Streams))
	for _, st := range pr.Streams {
		streams = append(streams, mapStream(st))
	}
	return map[string]any{"format": format, "streams": streams}, nil
}

func mapStream(st ffStream) map[string]any {
	out := map[string]any{"index": st.Index, "codec": st.CodecName}
	putStr(out, "codecLong", st.CodecLong)
	switch st.CodecType {
	case "video":
		out["type"] = "video"
		if st.Width > 0 {
			out["width"] = st.Width
		}
		if st.Height > 0 {
			out["height"] = st.Height
		}
		if fr := parseRate(st.RFrameRate); fr > 0 {
			out["frameRate"] = fr
		}
		putInt(out, "bitrateBps", st.BitRate)
		putStr(out, "pixelFormat", st.PixFmt)
		putStr(out, "profile", st.Profile)
	case "audio":
		out["type"] = "audio"
		if st.Channels > 0 {
			out["channels"] = st.Channels
		}
		putStr(out, "channelLayout", st.ChannelLayout)
		putInt(out, "sampleRate", st.SampleRate)
		putInt(out, "bitrateBps", st.BitRate)
	case "subtitle", "data", "attachment":
		out["type"] = st.CodecType
	default:
		out["type"] = "unknown"
	}
	putStr(out, "language", st.Tags["language"])
	putStr(out, "title", st.Tags["title"])
	return out
}

// ── moveTo (discriminated-union result, no error frame) ──────────────────────

func moveTo(src, dest string, overwrite bool) map[string]any {
	fail := func(msg string) map[string]any { return map[string]any{"ok": false, "error": msg} }
	if src == "" || dest == "" {
		return fail("missing src/dest")
	}
	if _, err := os.Stat(dest); err == nil && !overwrite {
		return fail("destination exists")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fail(err.Error())
	}
	if err := os.Rename(src, dest); err != nil {
		if cerr := copyFile(src, dest); cerr != nil { // cross-device (EXDEV) fallback
			return fail(cerr.Error())
		}
		_ = os.Remove(src)
	}
	return map[string]any{"ok": true, "destPath": dest}
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// ── favorites / recents / presets (store-backed; mutators return full lists) ─

type recentRec struct {
	Path string `json:"path"`
	At   string `json:"at"`
}

// rememberRecent records a path and returns the recent-path list (newest first, capped).
func (s *session) rememberRecent(path string) []string {
	if s.srv.store == nil || strings.TrimSpace(path) == "" {
		return []string{}
	}
	_ = s.srv.store.PutJSON(store.BucketStudioRec, filepath.Clean(path),
		recentRec{Path: path, At: time.Now().UTC().Format(time.RFC3339)})
	rows, _ := s.srv.store.ListJSON(store.BucketStudioRec)
	recs := make([]recentRec, 0, len(rows))
	for _, r := range rows {
		var rr recentRec
		if json.Unmarshal(r, &rr) == nil {
			recs = append(recs, rr)
		}
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].At > recs[j].At })
	out := make([]string, 0, maxRecents)
	for _, r := range recs {
		if len(out) >= maxRecents {
			break
		}
		out = append(out, r.Path)
	}
	return out
}

func (s *session) listFavorites() []map[string]any {
	out := []map[string]any{}
	if s.srv.store == nil {
		return out
	}
	rows, _ := s.srv.store.ListJSON(store.BucketStudioFav)
	for _, r := range rows {
		var m map[string]any
		if json.Unmarshal(r, &m) == nil {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return asString(out[i]["addedAt"]) > asString(out[j]["addedAt"]) })
	return out
}

func (s *session) addFavorite(p map[string]any) []map[string]any {
	path := asString(p["path"])
	if s.srv.store == nil || path == "" {
		return s.listFavorites()
	}
	label := asString(p["label"])
	if label == "" {
		label = filepath.Base(path)
	}
	id := newID()
	_ = s.srv.store.PutJSON(store.BucketStudioFav, id, map[string]any{
		"id": id, "path": path, "label": label,
		"isDirectory": asBool(p["isDirectory"]), "addedAt": time.Now().UTC().Format(time.RFC3339),
	})
	return s.listFavorites()
}

func (s *session) removeFavorite(id string) []map[string]any {
	if s.srv.store != nil && id != "" {
		_ = s.srv.store.Delete(store.BucketStudioFav, id)
	}
	return s.listFavorites()
}

func (s *session) listPresets() []map[string]any {
	out := []map[string]any{}
	if s.srv.store == nil {
		return out
	}
	rows, _ := s.srv.store.ListJSON(store.BucketStudioPre)
	for _, r := range rows {
		var m map[string]any
		if json.Unmarshal(r, &m) == nil {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return asString(out[i]["label"]) < asString(out[j]["label"]) })
	return out
}

func (s *session) savePreset(p map[string]any) {
	if s.srv.store == nil {
		return
	}
	id := asString(p["id"])
	if id == "" {
		id = newID()
	}
	created := asString(p["createdAt"])
	if created == "" {
		created = time.Now().UTC().Format(time.RFC3339)
	}
	template := p["template"]
	if template == nil {
		template = map[string]any{}
	}
	_ = s.srv.store.PutJSON(store.BucketStudioPre, id, map[string]any{
		"id": id, "label": asString(p["label"]), "description": asString(p["description"]),
		"template": template, "isCustom": true, "createdAt": created,
	})
}

// ── small helpers ────────────────────────────────────────────────────────────

func newID() string { return encB64url(randomBytes(8)) }

func putStr(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func putInt(m map[string]any, k, v string) {
	if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
		m[k] = n
	}
}

func putFloat(m map[string]any, k, v string) {
	if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
		m[k] = f
	}
}

// parseRate parses ffprobe's "num/den" frame-rate string into fps (0 on failure/zero den).
func parseRate(s string) float64 {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	num, e1 := strconv.ParseFloat(parts[0], 64)
	den, e2 := strconv.ParseFloat(parts[1], 64)
	if e1 != nil || e2 != nil || den == 0 {
		return 0
	}
	return num / den
}
