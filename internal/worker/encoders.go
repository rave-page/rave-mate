package worker

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
)

// EncoderInfo describes one encoder + whether it ACTUALLY works on this system.
type EncoderInfo struct {
	Name    string `json:"name"`    // e.g. h264_nvenc
	Codec   string `json:"codec"`   // h264|hevc|av1|aac|opus|vorbis
	Kind    string `json:"kind"`    // sw|hw
	Vendor  string `json:"vendor"`  // nvidia|intel|amd|apple|""
	Audio   bool   `json:"audio"`   // audio encoder
	InBuild bool   `json:"inBuild"` // present in `ffmpeg -encoders`
	Working bool   `json:"working"` // a real test encode succeeded
}

// candidateVideo are the video encoders we probe. Parsing `-encoders` only proves the
// build supports an encoder - a HW encoder can be listed yet fail at runtime (no GPU /
// driver / busy). So we additionally TEST-ENCODE each one; Working is the truth.
var candidateVideo = []EncoderInfo{
	{Name: "libx264", Codec: "h264", Kind: "sw"},
	{Name: "libx265", Codec: "hevc", Kind: "sw"},
	{Name: "libsvtav1", Codec: "av1", Kind: "sw"},
	{Name: "h264_nvenc", Codec: "h264", Kind: "hw", Vendor: "nvidia"},
	{Name: "hevc_nvenc", Codec: "hevc", Kind: "hw", Vendor: "nvidia"},
	{Name: "av1_nvenc", Codec: "av1", Kind: "hw", Vendor: "nvidia"},
	{Name: "h264_qsv", Codec: "h264", Kind: "hw", Vendor: "intel"},
	{Name: "hevc_qsv", Codec: "hevc", Kind: "hw", Vendor: "intel"},
	{Name: "av1_qsv", Codec: "av1", Kind: "hw", Vendor: "intel"},
	{Name: "h264_amf", Codec: "h264", Kind: "hw", Vendor: "amd"},
	{Name: "hevc_amf", Codec: "hevc", Kind: "hw", Vendor: "amd"},
	{Name: "av1_amf", Codec: "av1", Kind: "hw", Vendor: "amd"},
	{Name: "h264_videotoolbox", Codec: "h264", Kind: "hw", Vendor: "apple"},
	{Name: "hevc_videotoolbox", Codec: "hevc", Kind: "hw", Vendor: "apple"},
}

var candidateAudio = []EncoderInfo{
	{Name: "aac", Codec: "aac", Kind: "sw", Audio: true},
	{Name: "libfdk_aac", Codec: "aac", Kind: "sw", Audio: true},
	{Name: "libopus", Codec: "opus", Kind: "sw", Audio: true},
	{Name: "libvorbis", Codec: "vorbis", Kind: "sw", Audio: true},
}

// tcDetect returns the encoders that genuinely work on this machine (build-present AND a
// real test encode succeeded). Tests run in parallel, bounded, with a per-test timeout.
func tcDetect(_ json.RawMessage, emit EmitFunc) (json.RawMessage, error) {
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	inBuild := buildEncoderSet(bin)

	all := append(append([]EncoderInfo{}, candidateVideo...), candidateAudio...)
	results := make([]EncoderInfo, len(all))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	var done int
	var mu sync.Mutex
	for i, e := range all {
		e.InBuild = inBuild[e.Name]
		results[i] = e
		if !e.InBuild {
			continue // skip the test if the build doesn't even have it
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, enc EncoderInfo) {
			defer debuglog.Recover(nil, source, false) // nil bus: detect runs without a bus handle
			defer wg.Done()
			defer func() { <-sem }()
			ok := testEncode(bin, enc.Name, enc.Audio)
			mu.Lock()
			results[idx].Working = ok
			done++
			n := done
			mu.Unlock()
			if emit != nil {
				emit("progress", map[string]any{"tested": enc.Name, "working": ok, "count": n})
			}
		}(i, e)
	}
	wg.Wait()
	return json.Marshal(map[string]any{"encoders": results})
}

// testEncode runs a tiny throwaway encode and reports whether it succeeded - the only
// reliable signal that a (HW) encoder actually functions on this system.
func testEncode(bin, name string, audio bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var args []string
	if audio {
		args = []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", "0.3", "-c:a", name, "-f", "null", "-"}
	} else {
		args = []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=black:s=160x120:r=10", "-frames:v", "3", "-pix_fmt", "yuv420p", "-c:v", name, "-f", "null", "-"}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	prepareCmd(cmd)
	return cmd.Run() == nil
}

// buildEncoderSet parses `ffmpeg -encoders` for which candidate names the build contains.
func buildEncoderSet(bin string) map[string]bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-encoders")
	prepareCmd(cmd)
	out, err := cmd.Output()
	set := map[string]bool{}
	if err != nil {
		return set
	}
	s := string(out)
	for _, e := range append(append([]EncoderInfo{}, candidateVideo...), candidateAudio...) {
		set[e.Name] = strings.Contains(s, " "+e.Name+" ")
	}
	return set
}
