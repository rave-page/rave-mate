package transcode

import "fmt"

// Split decode→fx→encode pipeline halves for the vfx worker: ffmpeg decodes the
// trimmed+cropped clip to rawvideo RGBA on stdout, rave-mate-vfx filters it, a second
// ffmpeg consumes the filtered stream on stdin and encodes per preset (audio mapped
// from the source with the same trim).

// DecodeRawArgs builds the decode half: VF (crop) → scale to w×h, constant fps,
// RGBA rawvideo on stdout, audio dropped.
func (j Job) DecodeRawArgs(w, h int, fps float64) []string {
	a := []string{"-hide_banner", "-nostats", "-y"}
	if j.TrimStart > 0 {
		a = append(a, "-ss", ftoa(j.TrimStart))
	}
	a = append(a, "-i", j.Input)
	if j.TrimEnd > j.TrimStart {
		a = append(a, "-t", ftoa(j.TrimEnd-j.TrimStart))
	}
	vf := fmt.Sprintf("scale=%d:%d", w, h)
	if j.VF != "" {
		vf = j.VF + "," + vf
	}
	return append(a, "-vf", vf, "-r", ftoa(fps), "-an", "-f", "rawvideo", "-pix_fmt", "rgba", "-")
}

// EncodeRawArgs builds the encode half: rawvideo RGBA w×h@fps on stdin (pipe:0) +
// audio from j.Input (same trim window), preset video/audio args (scaling and VF are
// decode-side, so the preset's geometry is suppressed here).
func (j Job) EncodeRawArgs(w, h int, fps float64) []string {
	a := []string{"-hide_banner", "-nostats", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", w, h),
		"-framerate", ftoa(fps), "-i", "pipe:0"}
	if j.TrimStart > 0 {
		a = append(a, "-ss", ftoa(j.TrimStart))
	}
	a = append(a, "-i", j.Input)
	if j.TrimEnd > j.TrimStart {
		a = append(a, "-t", ftoa(j.TrimEnd-j.TrimStart))
	}
	a = append(a, "-map", "0:v:0", "-map", "1:a:0?")

	jv := j
	jv.VF = ""
	jv.Preset.Width, jv.Preset.Height, jv.Preset.Deinterlace = 0, 0, false
	a = append(a, jv.videoArgs()...)
	a = append(a, j.audioArgs()...)
	if c := j.Preset.Container; c == "mp4" || c == "m4a" || c == "aac" {
		a = append(a, "-movflags", "+faststart")
	}
	return append(a, j.Output)
}
