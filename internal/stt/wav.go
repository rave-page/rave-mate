// Package stt is local speech-to-text: capture mic audio (reusing the ffmpeg dshow path), detect
// end-of-utterance, transcribe via a whisper.cpp subprocess, and hand the text to the caller (→
// Twitch chat). whisper.cpp expects 16 kHz mono 16-bit PCM. Pure-Go helpers (WAV, VAD) are
// unit-tested; capture + transcription shell out to ffmpeg + whisper-cli.
package stt

import (
	"encoding/binary"
	"io"
)

// SampleRate is whisper.cpp's required input rate.
const SampleRate = 16000

// WriteWAV writes s16le mono PCM as a canonical 16-bit PCM WAV (44-byte header) at rate Hz.
func WriteWAV(w io.Writer, pcm []byte, rate int) error {
	const (
		channels = 1
		bits     = 16
	)
	byteRate := rate * channels * bits / 8
	blockAlign := channels * bits / 8
	dataLen := len(pcm)
	riffLen := 36 + dataLen

	var hdr [44]byte
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(riffLen))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16) // PCM fmt chunk size
	binary.LittleEndian.PutUint16(hdr[20:22], 1)  // PCM
	binary.LittleEndian.PutUint16(hdr[22:24], channels)
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(hdr[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(hdr[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(hdr[34:36], bits)
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], uint32(dataLen))

	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(pcm)
	return err
}
