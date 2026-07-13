// Package audio is rave-mate's native decode + low-latency playback engine, replacing the
// ffmpeg-subprocess + beep path for the player. Decoders normalize every format to interleaved
// float32 in [-1,1] at the file's native rate; the engine resamples to the device rate. All
// seeks are sample-accurate via an index (SEEKTABLE / frame scan / sample tables) — never a
// full-file rescan. Runs inside the `player` featurehost child (a codec fault kills only it).
package audio

import "time"

// Format describes a PCM stream. Frame = one sample per channel (interleaved).
type Format struct {
	SampleRate int // Hz (native to the file)
	Channels   int // 1=mono, 2=stereo (higher folded to stereo by the engine)
}

// FrameToDuration converts a frame index at this rate to wall time.
func (f Format) FrameToDuration(frame int64) time.Duration {
	if f.SampleRate <= 0 {
		return 0
	}
	return time.Duration(frame) * time.Second / time.Duration(f.SampleRate)
}

// DurationToFrame converts wall time to a frame index (floor).
func (f Format) DurationToFrame(d time.Duration) int64 {
	if f.SampleRate <= 0 {
		return 0
	}
	return int64(d) * int64(f.SampleRate) / int64(time.Second)
}

// SecondsToFrame converts a second offset to a frame index (floor).
func (f Format) SecondsToFrame(sec float64) int64 {
	if sec < 0 {
		sec = 0
	}
	return int64(sec * float64(f.SampleRate))
}

// FrameToSeconds converts a frame index to seconds.
func (f Format) FrameToSeconds(frame int64) float64 {
	if f.SampleRate <= 0 {
		return 0
	}
	return float64(frame) / float64(f.SampleRate)
}

// clampf clamps v to [lo,hi].
func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
