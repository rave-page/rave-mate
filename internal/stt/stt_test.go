package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"
)

// newTestController builds a controller with no live sessions for unit-testing the transcript
// buffer + clipboard/send paths (onText records the last sent text).
func newTestController(onText func(string)) *Controller {
	return NewController(context.Background(), func() Options { return Options{} }, onText, nil)
}

func TestControllerLastTranscriptAndClipboard(t *testing.T) {
	c := newTestController(func(string) {})
	if c.LastTranscript() != "" {
		t.Fatalf("LastTranscript should start empty, got %q", c.LastTranscript())
	}
	// No setter + nothing recorded → copy fails.
	if c.CopyToClipboard() {
		t.Fatal("CopyToClipboard should fail with nothing recorded / no setter")
	}

	var clip string
	c.SetClipboard(func(s string) { clip = s })
	if c.CopyToClipboard() {
		t.Fatal("CopyToClipboard should fail with nothing recorded")
	}

	c.setLast("hello world")
	if c.LastTranscript() != "hello world" {
		t.Fatalf("LastTranscript = %q, want %q", c.LastTranscript(), "hello world")
	}
	if !c.CopyToClipboard() {
		t.Fatal("CopyToClipboard should succeed once a transcript is recorded")
	}
	if clip != "hello world" {
		t.Fatalf("clipboard = %q, want %q", clip, "hello world")
	}
}

func TestControllerOnUpdateAndClear(t *testing.T) {
	c := newTestController(func(string) {})
	var got string
	var fired int
	c.SetOnUpdate(func(s string) { got = s; fired++ })

	c.setLast("first")
	if got != "first" || fired != 1 {
		t.Fatalf("onUpdate after setLast: got=%q fired=%d", got, fired)
	}
	// Clear empties the buffer and fires onUpdate("").
	c.Clear()
	if c.LastTranscript() != "" {
		t.Fatalf("LastTranscript after Clear = %q, want empty", c.LastTranscript())
	}
	if got != "" || fired != 2 {
		t.Fatalf("onUpdate after Clear: got=%q fired=%d", got, fired)
	}
}

func TestControllerSendLast(t *testing.T) {
	var sent string
	c := newTestController(func(s string) { sent = s })

	c.SendLast() // empty → no send
	if sent != "" {
		t.Fatalf("SendLast with empty transcript sent %q", sent)
	}
	c.setLast("yo chat")
	c.SendLast()
	if sent != "yo chat" {
		t.Fatalf("SendLast sent %q, want %q", sent, "yo chat")
	}
}

// pcmTone builds n s16le mono samples of a sine at amp (0..1).
func pcmTone(n int, amp float64) []byte {
	b := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(amp * 32767 * math.Sin(float64(i)*0.1))
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func TestWriteWAVHeader(t *testing.T) {
	var buf bytes.Buffer
	pcm := pcmTone(100, 0.5)
	if err := WriteWAV(&buf, pcm, SampleRate); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if len(out) != 44+len(pcm) {
		t.Fatalf("len = %d, want %d", len(out), 44+len(pcm))
	}
	if string(out[0:4]) != "RIFF" || string(out[8:12]) != "WAVE" || string(out[36:40]) != "data" {
		t.Error("bad RIFF/WAVE/data tags")
	}
	if r := binary.LittleEndian.Uint32(out[24:28]); r != SampleRate {
		t.Errorf("sample rate = %d, want %d", r, SampleRate)
	}
	if ch := binary.LittleEndian.Uint16(out[22:24]); ch != 1 {
		t.Errorf("channels = %d, want 1", ch)
	}
	if b := binary.LittleEndian.Uint16(out[34:36]); b != 16 {
		t.Errorf("bits = %d, want 16", b)
	}
	if dl := binary.LittleEndian.Uint32(out[40:44]); int(dl) != len(pcm) {
		t.Errorf("data len = %d, want %d", dl, len(pcm))
	}
}

func TestRMS(t *testing.T) {
	if r := RMS(nil); r != 0 {
		t.Errorf("RMS(nil) = %v, want 0", r)
	}
	loud := RMS(pcmTone(1000, 0.8))
	quiet := RMS(pcmTone(1000, 0.02))
	if loud <= quiet {
		t.Errorf("loud (%v) should exceed quiet (%v)", loud, quiet)
	}
	if loud > 1.0 {
		t.Errorf("normalized RMS should be <=1, got %v", loud)
	}
}

func TestVADEndOfUtterance(t *testing.T) {
	// 1s silence limit at 16kHz; chunks of 1600 samples (0.1s).
	v := NewVAD(SampleRate, 0.05, time.Second)
	speech := pcmTone(1600, 0.5)
	silence := pcmTone(1600, 0.001)

	// leading silence → never ends (no speech yet)
	for i := 0; i < 20; i++ {
		if v.Feed(silence) {
			t.Fatal("ended during leading silence")
		}
	}
	if v.HadSpeech() {
		t.Fatal("HadSpeech true before any speech")
	}
	// speech
	if v.Feed(speech) || !v.HadSpeech() {
		t.Fatal("speech chunk should register (and not end)")
	}
	// trailing silence: 10 chunks = 1.0s → ends on the chunk that reaches the limit
	ended := false
	for i := 0; i < 10; i++ {
		if v.Feed(silence) {
			ended = true
			break
		}
	}
	if !ended {
		t.Error("expected end-of-utterance after 1s trailing silence")
	}
}

func TestVADReset(t *testing.T) {
	v := NewVAD(SampleRate, 0.05, 100*time.Millisecond)
	v.Feed(pcmTone(1600, 0.5))
	v.Reset()
	if v.HadSpeech() {
		t.Error("Reset should clear hadSpeech")
	}
}
