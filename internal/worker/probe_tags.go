package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dhowden/tag"

	"rave.page/mate/internal/musiclib"
)

// tagsHandler reads embedded tags (ID3v1/2, MP4, FLAC, Ogg) from a loose audio file and
// merges codec/bitrate/duration from ffprobe, returning a musiclib.Track. Used by the
// library "Music" mode for files that aren't in an imported DJ collection.
func tagsHandler(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var p pathParams
	if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	t := musiclib.Track{Path: p.Path}

	if f, err := os.Open(p.Path); err == nil {
		if m, err := tag.ReadFrom(f); err == nil {
			t.Title = m.Title()
			t.Artist = m.Artist()
			t.Album = m.Album()
			t.Genre = m.Genre()
			t.Comment = m.Comment()
			if y := m.Year(); y > 0 {
				t.ReleaseDate = strconv.Itoa(y)
			}
			raw := m.Raw()
			t.BPM = rawFloat(raw, "TBPM", "tmpo", "bpm")
			t.Key = rawString(raw, "TKEY", "key", "initialkey")
		}
		_ = f.Close()
	}

	// Codec/bitrate/duration via ffprobe (format section).
	if out, err := ffprobe("-show_format", "-of", "json", p.Path); err == nil {
		var probed struct {
			Format struct {
				Duration string `json:"duration"`
				BitRate  string `json:"bit_rate"`
				Size     string `json:"size"`
			} `json:"format"`
		}
		if json.Unmarshal([]byte(out), &probed) == nil {
			if d, err := strconv.ParseFloat(probed.Format.Duration, 64); err == nil {
				t.DurationSec = d
			}
			if br, err := strconv.Atoi(probed.Format.BitRate); err == nil {
				t.BitrateBps = br
			}
			if sz, err := strconv.Atoi(probed.Format.Size); err == nil {
				t.FileSizeKB = sz / 1024
			}
		}
	}
	return json.Marshal(t)
}

// artworkHandler returns the embedded cover picture of an audio file ({mime, data} - data is
// base64 over the wire via []byte JSON encoding; both empty when no art is embedded).
func artworkHandler(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var p pathParams
	if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	f, err := os.Open(p.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return json.Marshal(map[string]any{"mime": "", "data": []byte(nil)}) // no tags = no art
	}
	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return json.Marshal(map[string]any{"mime": "", "data": []byte(nil)})
	}
	return json.Marshal(map[string]any{"mime": pic.MIMEType, "data": pic.Data})
}

// rawString returns the first non-empty raw tag value among keys (case-insensitive frame
// IDs vary by container).
func rawString(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
				return s
			}
		}
	}
	return ""
}

// rawFloat parses the first numeric raw tag value among keys.
func rawFloat(raw map[string]any, keys ...string) float64 {
	s := rawString(raw, keys...)
	if s == "" {
		return 0
	}
	// MP4 "tmpo" is an int; ID3 TBPM is a string. Strip any non-numeric suffix.
	if f, err := strconv.ParseFloat(strings.Fields(s)[0], 64); err == nil {
		return f
	}
	return 0
}
