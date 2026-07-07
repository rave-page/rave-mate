// Package mediaplayer is the native ffmpeg-backed media player for rave-mate: it decodes audio AND
// video by shelling out to the managed ffmpeg/ffprobe (no libav/libmpv cgo, per the supply-chain
// rules) and exposes frames + an A/V-synced transport the Fyne UI draws. ffmpeg is the crash
// boundary (a separate process); the Go side only reads pipes and draws. See docs/VIDEO_PLAYER_DESIGN.md.
package mediaplayer

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

// Info is the probed shape of a media file (ffprobe).
type Info struct {
	Duration float64 // seconds (0 if unknown)
	HasVideo bool
	HasAudio bool
	Width    int // video pixel size (0 if audio-only)
	Height   int
	FPS      float64 // video frame rate (0 if unknown / audio-only)
}

// Probe runs ffprobe on file and returns its media Info.
func Probe(ctx context.Context, file string) (Info, error) {
	bin, ok := mediatools.Resolve("ffprobe")
	if !ok {
		return Info{}, fmt.Errorf("ffprobe not found (install FFmpeg in Settings)")
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin,
		"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", file)
	sysexec.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe %s: %w", file, err)
	}
	return parseProbe(out)
}

// ffprobe JSON subset.
type probeJSON struct {
	Streams []struct {
		CodecType    string `json:"codec_type"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		RFrameRate   string `json:"r_frame_rate"`   // "30000/1001"
		AvgFrameRate string `json:"avg_frame_rate"` // fallback
		Duration     string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// parseProbe maps ffprobe JSON → Info. Pure (no exec) → unit-tested.
func parseProbe(b []byte) (Info, error) {
	var p probeJSON
	if err := json.Unmarshal(b, &p); err != nil {
		return Info{}, fmt.Errorf("parse ffprobe json: %w", err)
	}
	var in Info
	in.Duration = atof(p.Format.Duration)
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			// Skip cover-art "video" streams (still images embedded in audio files): they have a
			// frame rate of 0/0 and we don't want them treated as a video track.
			fps := ratio(s.RFrameRate)
			if fps == 0 {
				fps = ratio(s.AvgFrameRate)
			}
			if fps == 0 {
				continue // attached picture, not a real video stream
			}
			in.HasVideo = true
			in.Width, in.Height, in.FPS = s.Width, s.Height, fps
			if in.Duration == 0 {
				in.Duration = atof(s.Duration)
			}
		case "audio":
			in.HasAudio = true
			if in.Duration == 0 {
				in.Duration = atof(s.Duration)
			}
		}
	}
	return in, nil
}

// ratio parses an ffmpeg "num/den" rational (e.g. "30000/1001") to a float; 0 on bad/zero input.
func ratio(s string) float64 {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			num := atof(s[:i])
			den := atof(s[i+1:])
			if den == 0 {
				return 0
			}
			return num / den
		}
	}
	return atof(s)
}

func atof(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
