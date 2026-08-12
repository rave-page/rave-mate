package transcode

import "fmt"

// Split decode→fx→encode pipeline halves for the vfx worker: ffmpeg decodes the
// trimmed+cropped clip to rawvideo RGBA on stdout, rave-mate-vfx filters it, a second
// ffmpeg consumes the filtered stream on stdin and encodes per preset (audio mapped
// from the source with the same trim).

// DecodeRawArgs builds the decode half: VF (crop) → scale to w×h, constant fps,
// RGBA rawvideo on stdout, audio dropped.
func (j Job) DecodeRawArgs(w, h int, fps float64) []string {
	return j.decodeRawArgs(w, h, fps, "", false)
}

// DecodeRawArgsPost is DecodeRawArgs with a post-scale filter suffix (e.g. the
// fit layout's background blur - after the downscale, where it is cheap).
func (j Job) DecodeRawArgsPost(w, h int, fps float64, post string) []string {
	return j.decodeRawArgs(w, h, fps, post, false)
}

// DecodeRawArgsRT is DecodeRawArgsPost paced at native frame rate (-re): the
// realtime preview pipeline stays ~1× realtime instead of racing through the
// whole remainder of the source at full speed.
func (j Job) DecodeRawArgsRT(w, h int, fps float64, post string) []string {
	return j.decodeRawArgs(w, h, fps, post, true)
}

func (j Job) decodeRawArgs(w, h int, fps float64, post string, realtime bool) []string {
	a := []string{"-hide_banner", "-nostats", "-y"}
	if realtime {
		a = append(a, "-re")
	}
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
	if post != "" {
		vf += "," + post
	}
	return append(a, "-vf", vf, "-r", ftoa(fps), "-an", "-f", "rawvideo", "-pix_fmt", "rgba", "-")
}

// EncodeRawArgs builds the encode half: rawvideo RGBA w×h@fps on stdin (pipe:0) +
// audio from j.Input (same trim window), preset video/audio args (scaling and VF are
// decode-side, so the preset's geometry is suppressed here).
func (j Job) EncodeRawArgs(w, h int, fps float64) []string {
	return j.encodeRawArgs(w, h, fps, false)
}

// EncodeRawOverlayArgs is the fit-layout encode half: the piped raw stream is the
// styled BACKGROUND; the untouched source (input 1, same trim) is scaled to fit
// inside w×h and overlaid centered - the foreground never passes through the
// effect chain.
func (j Job) EncodeRawOverlayArgs(w, h int, fps float64) []string {
	return j.encodeRawArgs(w, h, fps, true)
}

// StreamMime is the MSE codec string EncodeStreamArgs produces (H.264 High@4.2 + AAC-LC).
const StreamMime = `video/mp4; codecs="avc1.64002a,mp4a.40.2"`

// EncodeStreamArgs is the realtime-preview encode half: piped raw video + audio
// from j.Input (same trim), x264 ultrafast/zerolatency + AAC into a GROWING
// MSE-appendable fragmented MP4 (empty_moov + default_base_moof, 0.5 s GOP
// fragments). overlay = fit layout (piped raw is the styled background).
func (j Job) EncodeStreamArgs(w, h int, fps float64, overlay bool) []string {
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
	if overlay {
		fc := fmt.Sprintf("[1:v]fps=%s,scale=%d:%d:force_original_aspect_ratio=decrease[fg];"+
			"[0:v][fg]overlay=(W-w)/2:(H-h)/2[vout]", ftoa(fps), w, h)
		a = append(a, "-filter_complex", fc, "-map", "[vout]", "-map", "1:a:0?")
	} else {
		a = append(a, "-map", "0:v:0", "-map", "1:a:0?")
	}
	g := int(fps/2 + 0.5)
	if g < 1 {
		g = 1
	}
	return append(a,
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-profile:v", "high", "-level:v", "4.2", "-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", g), "-crf", "26",
		"-c:a", "aac", "-b:a", "128k", "-ar", "48000",
		"-f", "mp4", "-movflags", "+frag_keyframe+empty_moov+default_base_moof",
		j.Output)
}

func (j Job) encodeRawArgs(w, h int, fps float64, overlay bool) []string {
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
	if overlay {
		fc := fmt.Sprintf("[1:v]fps=%s,scale=%d:%d:force_original_aspect_ratio=decrease[fg];"+
			"[0:v][fg]overlay=(W-w)/2:(H-h)/2[vout]", ftoa(fps), w, h)
		a = append(a, "-filter_complex", fc, "-map", "[vout]", "-map", "1:a:0?")
	} else {
		a = append(a, "-map", "0:v:0", "-map", "1:a:0?")
	}

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
