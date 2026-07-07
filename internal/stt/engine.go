package stt

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Options configure one dictation session.
type Options struct {
	Device      string        // dshow input device name ("" = default)
	Model       string        // ggml model file ("" = default)
	AutoSilence time.Duration // >0: auto-finish after this much trailing silence (VAD); 0: manual stop only
	Threshold   float64       // VAD RMS threshold (0 → 0.015 default)
}

// Result is a finished session's outcome.
type Result struct {
	Text      string
	Err       error
	Discarded bool // finished via Discard (no transcription)
}

// Session is one in-flight dictation: capture → (VAD) → transcribe. It finishes when Stop or
// Discard is called, or - if AutoSilence>0 - when the VAD detects end of speech. The outcome is
// delivered exactly once on Done.
type Session struct {
	opts     Options
	cap      *Capture
	done     chan Result
	stopOnce sync.Once
	discard  atomic.Bool
	stopReq  atomic.Bool
}

// StartSession begins capturing from the configured device and returns immediately. Read the
// outcome from Done(). ctx cancellation aborts the session with an error.
func StartSession(ctx context.Context, opts Options) (*Session, error) {
	c, err := StartCapture(ctx, opts.Device)
	if err != nil {
		return nil, err
	}
	s := &Session{opts: opts, cap: c, done: make(chan Result, 1)}
	go s.run(ctx)
	return s, nil
}

// Stop ends recording and transcribes what was captured.
func (s *Session) Stop() { s.stopReq.Store(true) }

// Discard ends recording and drops it (no transcription).
func (s *Session) Discard() { s.discard.Store(true); s.stopReq.Store(true) }

// Done delivers the outcome once.
func (s *Session) Done() <-chan Result { return s.done }

func (s *Session) finish(r Result) { s.stopOnce.Do(func() { s.done <- r }) }

func (s *Session) run(ctx context.Context) {
	defer func() { _ = s.cap.Stop() }()

	threshold := s.opts.Threshold
	if threshold <= 0 {
		threshold = 0.015
	}
	var vad *VAD
	if s.opts.AutoSilence > 0 {
		vad = NewVAD(SampleRate, threshold, s.opts.AutoSilence)
	}

	var buf bytes.Buffer
	chunk := make([]byte, 3200) // 1600 samples @16kHz = 0.1s
	for {
		if s.stopReq.Load() {
			break
		}
		if ctx.Err() != nil {
			s.finish(Result{Err: ctx.Err()})
			return
		}
		n, err := s.cap.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if vad != nil && vad.Feed(chunk[:n]) {
				break // end of utterance
			}
		}
		if err != nil { // ffmpeg exited / pipe closed
			break
		}
	}

	if s.discard.Load() {
		s.finish(Result{Discarded: true})
		return
	}
	text, err := s.transcribe(ctx, buf.Bytes())
	s.finish(Result{Text: text, Err: err})
}

// transcribe writes the captured PCM to a temp WAV and runs whisper-cli.
func (s *Session) transcribe(ctx context.Context, pcm []byte) (string, error) {
	if len(pcm) == 0 {
		return "", fmt.Errorf("no audio captured")
	}
	tmp, err := os.CreateTemp("", "stt-*.wav")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	defer func() { _ = os.Remove(path) }()
	if err := WriteWAV(tmp, pcm, SampleRate); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return Transcribe(ctx, path, s.opts.Model)
}
