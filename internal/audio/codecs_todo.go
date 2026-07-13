package audio

import (
	"fmt"
	"io"
)

// Placeholder constructors for the compressed codecs. Implemented in follow-up commits
// (flac.go / mp3.go / vorbis.go) with index-backed sample-accurate seek. Until then Open
// returns ErrUnsupported for these so the caller keeps the ffmpeg fallback (no regression).

func newFLACDecoder(r io.ReadSeekCloser) (Decoder, error) {
	_ = r.Close()
	return nil, fmt.Errorf("%w: flac (native decoder pending)", ErrUnsupported)
}

func newMP3Decoder(r io.ReadSeekCloser) (Decoder, error) {
	_ = r.Close()
	return nil, fmt.Errorf("%w: mp3 (native decoder pending)", ErrUnsupported)
}

func newVorbisDecoder(r io.ReadSeekCloser) (Decoder, error) {
	_ = r.Close()
	return nil, fmt.Errorf("%w: ogg/vorbis (native decoder pending)", ErrUnsupported)
}
