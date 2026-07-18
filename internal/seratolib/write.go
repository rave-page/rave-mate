package seratolib

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/sysactivity"
)

// seratoRunning reports whether a Serato DJ process is running. Overridable in tests.
// Fails open when the platform can't enumerate processes (the temp+verify+rename write is
// still atomic; Serato may just not see the change until a rescan).
var seratoRunning = func() bool {
	set, ok := sysactivity.New().RunningProcesses()
	if !ok {
		return false
	}
	// Prefix survives version drift ("Serato DJ Pro 3.x" exe renames); exact names kept
	// for the un-suffixed legacy exes.
	return sysactivity.RunningPrefix(set, "serato dj") ||
		sysactivity.Running(set, "seratodj") || sysactivity.Running(set, "serato")
}

// WriteBeatgrid writes a CONSTANT beatgrid (single terminal marker at startMs with bpm) into
// the file's Serato tag (MP3 GEOB / FLAC vorbis comment). Refuses while Serato is running.
func WriteBeatgrid(path string, bpm, startMs float64) error {
	if seratoRunning() {
		return ErrSeratoRunning
	}
	return writeBeatgridFile(path, []musiclib.GridMarker{{PositionMs: startMs, BPM: bpm}})
}

// ReadBeatgrid reads the file's Serato beatgrid (found=false when the file has none).
func ReadBeatgrid(path string) ([]musiclib.GridMarker, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	payload, found, err := readBeatgridBytes(path, data)
	if err != nil || !found {
		return nil, false, err
	}
	markers, err := decodeBeatgrid(payload)
	if err != nil {
		return nil, false, err
	}
	return markers, true, nil
}

// readBeatgridBytes dispatches beatgrid extraction by extension.
func readBeatgridBytes(path string, data []byte) ([]byte, bool, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return readID3Beatgrid(data)
	case ".flac":
		return readFLACBeatgrid(data)
	}
	return nil, false, ErrUnsupported
}

// writeBeatgridFile splices markers into path's Serato tag. Integrity: build the whole new
// file in memory, semantically verify it (grid round-trips; untouched frames/blocks + audio
// byte-identical), write to a same-dir temp, fsync, read back + compare byte-for-byte, then
// rename over the original. The original is never modified in place.
func writeBeatgridFile(path string, markers []musiclib.GridMarker) error {
	payload, err := encodeBeatgrid(markers)
	if err != nil {
		return err
	}
	orig, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var built []byte
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		built, err = spliceID3Beatgrid(orig, payload)
	case ".flac":
		built, err = spliceFLACBeatgrid(orig, payload)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupported, filepath.Ext(path))
	}
	if err != nil {
		return err
	}
	if err := verifySplice(path, orig, built, markers); err != nil {
		return fmt.Errorf("seratolib: verify %s: %w", filepath.Base(path), err)
	}
	return commitAtomic(path, built)
}

// verifySplice proves built is orig + exactly the beatgrid change before anything hits disk.
func verifySplice(path string, orig, built []byte, want []musiclib.GridMarker) error {
	payload, found, err := readBeatgridBytes(path, built)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("beatgrid missing after splice")
	}
	got, err := decodeBeatgrid(payload)
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return fmt.Errorf("marker count %d != %d", len(got), len(want))
	}
	for i := range want {
		// Positions/BPM survive a float32 round-trip; compare against the f32-quantized value.
		if !f32Close(got[i].PositionMs/1000, want[i].PositionMs/1000) || !f32Close(got[i].BPM, want[i].BPM) {
			return fmt.Errorf("marker %d mismatch: got %+v want %+v", i, got[i], want[i])
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return verifyID3Untouched(orig, built, map[string]bool{beatgridDesc: true})
	case ".flac":
		return verifyFLACUntouched(orig, built, []string{"SERATO_BEATGRID="})
	}
	return nil
}

// f32Close reports a==b after float32 quantization (relative 1e-6 slack for BPM reconstruction).
func f32Close(a, b float64) bool {
	fa, fb := float64(float32(a)), float64(float32(b))
	return math.Abs(fa-fb) <= 1e-6*math.Max(1, math.Abs(fb))
}

// verifyID3Untouched checks every unmanaged frame and the audio region carried over
// (managed = GEOB descriptions the splice is allowed to rewrite or drop).
func verifyID3Untouched(orig, built []byte, managed map[string]bool) error {
	ot, oAudio, err := parseID3(orig)
	if err != nil {
		return err
	}
	bt, bAudio, err := parseID3(built)
	if err != nil {
		return err
	}
	if bt == nil {
		return errors.New("built file lost its ID3 tag")
	}
	if !bytes.Equal(orig[oAudio:], built[bAudio:]) {
		return errors.New("audio region changed")
	}
	var oFrames []id3Frame
	if ot != nil {
		for _, f := range ot.frames {
			if f.id == "GEOB" && managed[geobDescription(ot.major, f)] {
				continue
			}
			oFrames = append(oFrames, f)
		}
	}
	var bFrames []id3Frame
	for _, f := range bt.frames {
		if f.id == "GEOB" && managed[geobDescription(bt.major, f)] {
			continue
		}
		bFrames = append(bFrames, f)
	}
	if len(oFrames) != len(bFrames) {
		return fmt.Errorf("frame count changed: %d -> %d", len(oFrames), len(bFrames))
	}
	for i := range oFrames {
		if oFrames[i].id != bFrames[i].id || !bytes.Equal(oFrames[i].raw, bFrames[i].raw) {
			return fmt.Errorf("frame %s no longer byte-identical", oFrames[i].id)
		}
	}
	return nil
}

// verifyFLACUntouched checks every non-vorbis block, unmanaged comments, and the audio
// (managedKeys = "KEY=" prefixes the splice is allowed to rewrite).
func verifyFLACUntouched(orig, built []byte, managedKeys []string) error {
	oBlocks, oAudio, err := parseFLAC(orig)
	if err != nil {
		return err
	}
	bBlocks, bAudio, err := parseFLAC(built)
	if err != nil {
		return err
	}
	if !bytes.Equal(orig[oAudio:], built[bAudio:]) {
		return errors.New("audio region changed")
	}
	isManaged := func(c string) bool {
		u := strings.ToUpper(c)
		for _, k := range managedKeys {
			if strings.HasPrefix(u, k) {
				return true
			}
		}
		return false
	}
	filter := func(blocks []flacBlock) (rest []flacBlock, comments []string, err error) {
		for _, b := range blocks {
			if b.typ != flacVorbisType {
				rest = append(rest, b)
				continue
			}
			_, cs, verr := vorbisComments(b.body)
			if verr != nil {
				return nil, nil, verr
			}
			for _, c := range cs {
				if !isManaged(c) {
					comments = append(comments, c)
				}
			}
		}
		return rest, comments, nil
	}
	oRest, oComments, err := filter(oBlocks)
	if err != nil {
		return err
	}
	bRest, bComments, err := filter(bBlocks)
	if err != nil {
		return err
	}
	if len(oRest) != len(bRest) {
		return fmt.Errorf("metadata block count changed: %d -> %d", len(oRest), len(bRest))
	}
	for i := range oRest {
		if oRest[i].typ != bRest[i].typ || !bytes.Equal(oRest[i].body, bRest[i].body) {
			return fmt.Errorf("metadata block type %d no longer byte-identical", oRest[i].typ)
		}
	}
	if len(oComments) != len(bComments) {
		return fmt.Errorf("vorbis comment count changed: %d -> %d", len(oComments), len(bComments))
	}
	for i := range oComments {
		if oComments[i] != bComments[i] {
			return errors.New("vorbis comment no longer identical")
		}
	}
	return nil
}

// commitAtomic writes built to a same-dir temp, fsyncs, reads it back byte-for-byte, restores
// the original mode, then renames over path.
func commitAtomic(path string, built []byte) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".rmgrid-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func(e error) error {
		_ = os.Remove(tmpName)
		return e
	}
	if _, err := tmp.Write(built); err != nil {
		_ = tmp.Close()
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	back, err := os.ReadFile(tmpName)
	if err != nil {
		return cleanup(err)
	}
	if !bytes.Equal(back, built) {
		return cleanup(errors.New("seratolib: temp file readback mismatch"))
	}
	if err := os.Chmod(tmpName, st.Mode().Perm()); err != nil {
		return cleanup(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return cleanup(err)
	}
	return nil
}
