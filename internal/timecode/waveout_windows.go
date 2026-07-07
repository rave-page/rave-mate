//go:build windows

package timecode

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// WinMM waveOut backend (stdlib syscall, no cgo - mirrors internal/midi). oto/beep can't target a
// specific device; waveOut can (deviceID), so the user routes LTC into a chosen virtual audio
// cable. Double-buffered: N headers are queued; each time one drains we refill it from the LTC
// generator and re-queue, so the DAC never underruns.
var (
	winmm                 = syscall.NewLazyDLL("winmm.dll")
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procWaveOutGetNumDevs = winmm.NewProc("waveOutGetNumDevs")
	procWaveOutGetDevCaps = winmm.NewProc("waveOutGetDevCapsW")
	procWaveOutOpen       = winmm.NewProc("waveOutOpen")
	procWaveOutPrepare    = winmm.NewProc("waveOutPrepareHeader")
	procWaveOutWrite      = winmm.NewProc("waveOutWrite")
	procWaveOutUnprepare  = winmm.NewProc("waveOutUnprepareHeader")
	procWaveOutReset      = winmm.NewProc("waveOutReset")
	procWaveOutClose      = winmm.NewProc("waveOutClose")
	procCreateEvent       = kernel32.NewProc("CreateEventW")
	procWaitForSingle     = kernel32.NewProc("WaitForSingleObject")
	procCloseHandle       = kernel32.NewProc("CloseHandle")
)

const (
	waveMapper      = 0xFFFFFFFF
	waveFormatPCM   = 1
	callbackEvent   = 0x00050000
	whdrDone        = 0x00000001
	whdrPrepared    = 0x00000002
	woNumBuffers    = 4
	woBufferSamples = 1600 // ~33ms @48k (≈ one 30fps frame)
	maxProductName  = 32
	waitTimeoutMs   = 50
)

type waveFormatEx struct {
	wFormatTag      uint16
	nChannels       uint16
	nSamplesPerSec  uint32
	nAvgBytesPerSec uint32
	nBlockAlign     uint16
	wBitsPerSample  uint16
	cbSize          uint16
}

type waveHdr struct {
	lpData          uintptr
	dwBufferLength  uint32
	dwBytesRecorded uint32
	dwUser          uintptr
	dwFlags         uint32
	dwLoops         uint32
	lpNext          uintptr
	reserved        uintptr
}

type waveOutCaps struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [maxProductName]uint16
	dwFormats      uint32
	wChannels      uint16
	wReserved1     uint16
	dwSupport      uint32
}

func waveOutDeviceName(i uintptr) (string, bool) {
	var caps waveOutCaps
	r, _, _ := procWaveOutGetDevCaps.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
	if r != 0 {
		return "", false
	}
	return syscall.UTF16ToString(caps.szPname[:]), true
}

// WaveOutDevices lists the OS audio-output device names.
func WaveOutDevices() ([]string, error) {
	n, _, _ := procWaveOutGetNumDevs.Call()
	out := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		if name, ok := waveOutDeviceName(i); ok {
			out = append(out, name)
		}
	}
	return out, nil
}

// resolveWaveOutID maps a device-name substring to a deviceID; "" (or no match) = WAVE_MAPPER.
func resolveWaveOutID(substr string) uintptr {
	if strings.TrimSpace(substr) == "" {
		return waveMapper
	}
	want := strings.ToLower(substr)
	n, _, _ := procWaveOutGetNumDevs.Call()
	for i := uintptr(0); i < n; i++ {
		if name, ok := waveOutDeviceName(i); ok && strings.Contains(strings.ToLower(name), want) {
			return i
		}
	}
	return waveMapper
}

// playLTC opens the audio device and streams mono int16 PCM pulled from fill until ctx is done or a
// fatal WinMM error occurs. deviceName "" = system default. fill must fully populate each block.
func playLTC(ctx context.Context, deviceName string, sampleRate int, fill func([]int16)) error {
	dev := resolveWaveOutID(deviceName)
	ev, _, _ := procCreateEvent.Call(0, 0, 0, 0) // auto-reset, initially non-signaled
	if ev == 0 {
		return fmt.Errorf("timecode: CreateEvent failed")
	}
	defer func() { _, _, _ = procCloseHandle.Call(ev) }()

	fmtEx := waveFormatEx{
		wFormatTag:      waveFormatPCM,
		nChannels:       1,
		nSamplesPerSec:  uint32(sampleRate),
		nAvgBytesPerSec: uint32(sampleRate * 2),
		nBlockAlign:     2,
		wBitsPerSample:  16,
	}
	var handle uintptr
	if r, _, _ := procWaveOutOpen.Call(uintptr(unsafe.Pointer(&handle)), dev,
		uintptr(unsafe.Pointer(&fmtEx)), ev, 0, callbackEvent); r != 0 {
		return fmt.Errorf("timecode: waveOutOpen failed: mmresult=%d", r)
	}

	bufs := make([][]int16, woNumBuffers)
	hdrs := make([]*waveHdr, woNumBuffers)
	var mu sync.Mutex // fill must be serialized (single ltcGen)
	queue := func(i int) error {
		mu.Lock()
		fill(bufs[i])
		mu.Unlock()
		if r, _, _ := procWaveOutWrite.Call(handle, uintptr(unsafe.Pointer(hdrs[i])), unsafe.Sizeof(*hdrs[i])); r != 0 {
			return fmt.Errorf("timecode: waveOutWrite failed: mmresult=%d", r)
		}
		return nil
	}

	// Prepare + prime all buffers.
	for i := range bufs {
		bufs[i] = make([]int16, woBufferSamples)
		hdrs[i] = &waveHdr{lpData: uintptr(unsafe.Pointer(&bufs[i][0])), dwBufferLength: uint32(woBufferSamples * 2)}
		if r, _, _ := procWaveOutPrepare.Call(handle, uintptr(unsafe.Pointer(hdrs[i])), unsafe.Sizeof(*hdrs[i])); r != 0 {
			cleanupWaveOut(handle, hdrs)
			return fmt.Errorf("timecode: waveOutPrepareHeader failed: mmresult=%d", r)
		}
	}
	for i := range bufs {
		if err := queue(i); err != nil {
			cleanupWaveOut(handle, hdrs)
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			cleanupWaveOut(handle, hdrs)
			return nil
		default:
		}
		_, _, _ = procWaitForSingle.Call(ev, waitTimeoutMs)
		for i := range hdrs {
			if hdrs[i].dwFlags&whdrDone == 0 {
				continue
			}
			hdrs[i].dwFlags &^= whdrDone
			if err := queue(i); err != nil {
				cleanupWaveOut(handle, hdrs)
				return err
			}
		}
	}
}

// cleanupWaveOut resets (returns queued buffers), unprepares, and closes - best-effort teardown.
func cleanupWaveOut(handle uintptr, hdrs []*waveHdr) {
	_, _, _ = procWaveOutReset.Call(handle)
	for _, h := range hdrs {
		if h != nil && h.dwFlags&whdrPrepared != 0 {
			_, _, _ = procWaveOutUnprepare.Call(handle, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
		}
	}
	_, _, _ = procWaveOutClose.Call(handle)
}
