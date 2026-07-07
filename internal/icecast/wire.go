package icecast

import (
	"io"
	"net/url"
	"strconv"
	"strings"
)

// parseRequestLine splits "<METHOD> <target> <proto>" (the proto token is ignored - it may
// be HTTP/1.0, HTTP/1.1 or the legacy ICE/1.0).
func parseRequestLine(line string) (method, target string, ok bool) {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// mountPath extracts the mount path from a request target (strips any query/host).
func mountPath(target string) string {
	if u, err := url.Parse(target); err == nil && u.Path != "" {
		return u.Path
	}
	before, _, _ := strings.Cut(target, "?")
	return before
}

// mountSlug renders a mount path as a filename-safe suffix ("/stream" → "_stream"; "" → "").
func mountSlug(mount string) string {
	m := strings.Trim(mount, "/")
	if m == "" {
		return ""
	}
	var b strings.Builder
	b.WriteByte('_')
	for _, c := range m {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// formatFromContentType maps a broadcast Content-Type to a capture file extension.
func formatFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "application/ogg", "audio/ogg", "audio/vorbis":
		return "ogg"
	case "audio/mpeg", "audio/mp3", "audio/mpeg3":
		return "mp3"
	case "audio/aac", "audio/aacp", "audio/mp4", "audio/x-aac":
		return "aac"
	default:
		return "bin"
	}
}

// parseSong splits an Icecast "song" metadata string into artist/title. The convention is
// "Artist - Title"; with no separator the whole string is the title.
func parseSong(song string) Meta {
	song = strings.TrimSpace(song)
	if artist, title, ok := strings.Cut(song, " - "); ok {
		return Meta{Artist: strings.TrimSpace(artist), Title: strings.TrimSpace(title)}
	}
	return Meta{Title: song}
}

// humanByteCount formats a byte count compactly (e.g. "4.2 MB") for status/log lines.
func humanByteCount(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string("KMGT"[exp]) + "B"
}

// writeStatus writes a minimal HTTP/1.0 response with a short body and closes the exchange.
func writeStatus(w io.Writer, code int, msg string) {
	body := msg + "\n"
	_, _ = io.WriteString(w, "HTTP/1.0 "+strconv.Itoa(code)+" "+msg+"\r\n"+
		"Content-Type: text/plain\r\n"+
		"Content-Length: "+strconv.Itoa(len(body))+"\r\n"+
		"Connection: close\r\n\r\n"+body)
}
