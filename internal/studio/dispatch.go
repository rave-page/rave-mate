package studio

import (
	"fmt"

	"rave.page/mate/internal/localmedia"
)

// dispatchUnary handles a non-streaming method. Returns (result, errCode, err). localMedia
// browse uses the shared internal/localmedia impl (byte-exact with the web contract + reused
// by the LAN peer-control RPC). Streaming transcode (start/attach/cancel) is wired via the job
// hub in session.go; unwired methods return an explicit error rather than a silent stub.
func dispatchUnary(method string, params any) (any, errorCode, error) {
	p := asMap(params)
	switch method {
	case "localMedia.getDefaults":
		return localmedia.Defaults(), "", nil
	case "localMedia.listDirectory":
		return localmedia.ListDirectory(asString(p["path"]), asBool(p["includeHidden"])), "", nil
	default:
		return nil, errInternal, fmt.Errorf("method %q not yet implemented on rave-mate", method)
	}
}

// ── param coercion (wire params are `unknown`) ───────────────────────────────

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// intOf coerces a wire number to int, discarding the ok flag; 0 on absence/mismatch.
func intOf(v any) int { i, _ := asInt(v); return i }

// asStringSlice coerces a wire array of strings; nil on mismatch, [] preserved.
func asStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
