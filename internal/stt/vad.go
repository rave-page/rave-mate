package stt

import (
	"math"
	"time"
)

// RMS returns the root-mean-square level of s16le mono PCM, normalized to 0..1 (1 = full scale).
func RMS(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		s := float64(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		sum += s * s
	}
	return math.Sqrt(sum/float64(n)) / 32768.0
}

// VAD is a simple energy-threshold end-of-utterance detector: once speech has been heard, a
// continuous trailing silence of at least SilenceDur ends the utterance. Stateful; Feed it the
// captured PCM as it arrives. Not a neural VAD - good enough for push-to-talk dictation.
type VAD struct {
	threshold    float64 // normalized RMS above which a chunk counts as speech
	rate         int     // sample rate (Hz)
	silenceLimit int     // samples of trailing silence that end an utterance

	hadSpeech bool
	silence   int // consecutive silent samples since the last speech
}

// NewVAD builds a detector. threshold is a normalized RMS (e.g. 0.015); silence is the trailing
// quiet that ends an utterance (e.g. 1s). rate<=0 → SampleRate.
func NewVAD(rate int, threshold float64, silence time.Duration) *VAD {
	if rate <= 0 {
		rate = SampleRate
	}
	return &VAD{
		threshold:    threshold,
		rate:         rate,
		silenceLimit: int(float64(rate) * silence.Seconds()),
	}
}

// Feed processes one chunk of s16le mono PCM. Returns true exactly once: when speech has occurred
// and trailing silence has reached the limit (end of utterance). After that, Reset to reuse.
func (v *VAD) Feed(pcm []byte) bool {
	samples := len(pcm) / 2
	if samples == 0 {
		return false
	}
	if RMS(pcm) >= v.threshold {
		v.hadSpeech = true
		v.silence = 0
		return false
	}
	if !v.hadSpeech {
		return false // leading silence - keep waiting for onset
	}
	v.silence += samples
	return v.silence >= v.silenceLimit
}

// HadSpeech reports whether any speech has been detected since the last Reset.
func (v *VAD) HadSpeech() bool { return v.hadSpeech }

// Reset clears the detector for a new utterance.
func (v *VAD) Reset() { v.hadSpeech, v.silence = false, 0 }
