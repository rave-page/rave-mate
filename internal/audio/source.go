package audio

import (
	"encoding/binary"
	"io"
	"math"
	"sync"
)

// Device output format. One physical device => one rate/channel count. f32le matches
// oto.FormatFloat32LE, so Read hands bytes straight through with no conversion.
const (
	deviceRate     = 48000
	deviceChannels = 2
	deviceBytes    = 4 // float32
)

// preloadMaxBytes caps the RAM-preload buffer (bytes). A track whose decoded device-rate PCM
// exceeds this streams from disk instead (indexed seek), so preload never blows RAM.
// 512MiB ≈ 46 min stereo f32 — every normal track preloads; only huge sets stream.
const preloadMaxBytes = 512 << 20

// streamReadFrames is the streaming pull granularity from the decoder (bounded; one read never
// asks for more, so the transient decode buffer is capped at streamReadFrames*channels*4 bytes).
const streamReadFrames = 4096

// source is the io.Reader oto pulls: it yields interleaved float32-LE device bytes from either a
// fully-decoded RAM buffer (cue-edit: instant seek, 0-latency Space) or an indexed streaming
// decoder (huge files). It owns the frame read-cursor; Position math lets the engine subtract
// oto's buffered slack to report the AUDIBLE frame. Not safe for concurrent Read + control; the
// engine serializes control (SeekTo/Play state) with reads via mu.
type source struct {
	mu    sync.Mutex
	total int64 // device-rate frames

	// preload path (ram != nil): interleaved deviceChannels float32 at deviceRate.
	ram []float32

	// streaming path (dec != nil): decoder + on-the-fly resample to the device rate.
	dec     Decoder
	src     Format // decoder's native format
	rs      *resampler
	decBuf  []float32 // transient decode scratch (bounded: streamReadFrames*src.Channels)
	pending []float32 // resampled-but-unread device frames (bounded by resampler output)
	decEOF  bool

	pos     int64 // next device frame to hand to oto (the read cursor)
	stopAt  int64 // hard stop frame (-1 = play to end); preview stop, loop-out, etc.
	scratch []byte
}

// newRAMSource decodes the whole file into a device-rate RAM buffer. Caller checked the size cap.
func newRAMSource(dec Decoder) (*source, error) {
	sf := dec.Format()
	s := &source{stopAt: -1}
	ch := sf.Channels
	if ch < 1 {
		ch = 1
	}
	// Decode fully into native interleaved f32.
	var native []float32
	buf := make([]float32, streamReadFrames*ch)
	for {
		n, err := dec.ReadFrames(buf)
		if n > 0 {
			native = append(native, buf[:n*ch]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = dec.Close()
			return nil, err
		}
	}
	_ = dec.Close()
	// Fold to stereo + resample to device rate into RAM.
	s.ram = toDeviceRAM(native, sf)
	s.total = int64(len(s.ram) / deviceChannels)
	return s, nil
}

// newStreamSource wraps a decoder for on-the-fly device-rate output (large files).
func newStreamSource(dec Decoder) *source {
	sf := dec.Format()
	ch := sf.Channels
	if ch < 1 {
		ch = 1
	}
	s := &source{
		dec:    dec,
		src:    sf,
		stopAt: -1,
		decBuf: make([]float32, streamReadFrames*ch),
	}
	if sf.SampleRate != deviceRate {
		s.rs = newResampler(sf.SampleRate, deviceRate, deviceChannels)
	}
	if tf := dec.TotalFrames(); tf > 0 {
		s.total = tf * int64(deviceRate) / int64(sf.SampleRate) // device-rate frame estimate
	} else {
		s.total = -1
	}
	return s
}

// Total returns device-rate frame count (-1 if unknown, streaming only).
func (s *source) Total() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.total }

// Pos returns the current read cursor (device frames handed toward oto).
func (s *source) Pos() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.pos }

// SeekTo repositions the read cursor to a device frame (sample-accurate). RAM = index move
// (0 cost); streaming = decoder SeekTo (index-backed) + resampler reset.
func (s *source) SeekTo(frame int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seekLocked(frame)
}

func (s *source) seekLocked(frame int64) error {
	if frame < 0 {
		frame = 0
	}
	if s.total >= 0 && frame > s.total {
		frame = s.total
	}
	s.pos = frame
	if s.ram != nil {
		return nil
	}
	// streaming: map device frame -> native frame, seek decoder, reset resampler + pending.
	native := frame * int64(s.src.SampleRate) / int64(deviceRate)
	s.pending = s.pending[:0]
	s.decEOF = false
	if s.rs != nil {
		s.rs.reset()
	}
	return s.dec.SeekTo(native)
}

// setStopAt bounds playback: Read returns EOF once pos reaches frame (-1 = no bound).
func (s *source) setStopAt(frame int64) {
	s.mu.Lock()
	s.stopAt = frame
	s.mu.Unlock()
}

// Read fills p with float32-LE device bytes from the current cursor (io.Reader for oto).
func (s *source) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	frames := len(p) / (deviceBytes * deviceChannels)
	if frames == 0 {
		return 0, nil
	}
	if s.stopAt >= 0 && s.pos >= s.stopAt {
		return 0, io.EOF
	}
	if s.stopAt >= 0 {
		if r := s.stopAt - s.pos; int64(frames) > r {
			frames = int(r)
		}
	}
	var got int
	if s.ram != nil {
		got = s.readRAM(frames)
		s.writeBytes(p, s.ram[s.pos*deviceChannels:(s.pos+int64(got))*deviceChannels])
	} else {
		if cap(s.scratch) < frames*deviceChannels {
			s.scratch = make([]byte, 0, frames*deviceChannels)
		}
		dev := s.readStream(frames)
		got = len(dev) / deviceChannels
		s.writeBytes(p, dev)
	}
	s.pos += int64(got)
	if got == 0 {
		return 0, io.EOF
	}
	return got * deviceBytes * deviceChannels, nil
}

func (s *source) readRAM(frames int) int {
	if remain := s.total - s.pos; int64(frames) > remain {
		frames = int(remain)
	}
	if frames < 0 {
		frames = 0
	}
	return frames
}

// writeBytes serializes interleaved float32 device samples to little-endian bytes.
func (s *source) writeBytes(p []byte, samples []float32) {
	for i, v := range samples {
		binary.LittleEndian.PutUint32(p[i*4:], math.Float32bits(v))
	}
}

// readStream pulls up to `frames` device frames, decoding + resampling as needed. Returns the
// interleaved device samples (len = frames*deviceChannels or fewer at EOF).
func (s *source) readStream(frames int) []float32 {
	need := frames * deviceChannels
	out := make([]float32, 0, need)
	// drain pending resampled frames first
	for len(out) < need {
		if len(s.pending) > 0 {
			take := need - len(out)
			if take > len(s.pending) {
				take = len(s.pending)
			}
			out = append(out, s.pending[:take]...)
			s.pending = s.pending[take:]
			continue
		}
		if s.decEOF {
			break
		}
		n, err := s.dec.ReadFrames(s.decBuf)
		if n > 0 {
			native := s.decBuf[:n*s.src.Channels]
			dev := toDeviceStereo(native, s.src.Channels)
			if s.rs != nil {
				dev = s.rs.process(dev)
			}
			s.pending = append(s.pending, dev...)
		}
		if err == io.EOF || (err == nil && n == 0) {
			s.decEOF = true
			if s.rs != nil {
				s.pending = append(s.pending, s.rs.flush()...)
			}
		} else if err != nil {
			s.decEOF = true
		}
	}
	return out
}

// Close releases the streaming decoder (RAM source owns nothing after decode).
func (s *source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dec != nil {
		err := s.dec.Close()
		s.dec = nil
		return err
	}
	return nil
}

// toDeviceRAM folds native interleaved f32 to stereo and resamples to the device rate.
func toDeviceRAM(native []float32, sf Format) []float32 {
	stereo := toDeviceStereo(native, sf.Channels)
	if sf.SampleRate == deviceRate {
		return stereo
	}
	rs := newResampler(sf.SampleRate, deviceRate, deviceChannels)
	out := rs.process(stereo)
	return append(out, rs.flush()...)
}

// toDeviceStereo folds an interleaved buffer of `ch` channels to interleaved stereo. Mono
// duplicates; >2 channels take the first two (downmix is a later refinement).
func toDeviceStereo(in []float32, ch int) []float32 {
	if ch == deviceChannels {
		return in
	}
	frames := len(in) / ch
	out := make([]float32, frames*deviceChannels)
	for i := 0; i < frames; i++ {
		if ch == 1 {
			v := in[i]
			out[i*2], out[i*2+1] = v, v
		} else {
			out[i*2], out[i*2+1] = in[i*ch], in[i*ch+1]
		}
	}
	return out
}
