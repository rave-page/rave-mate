//go:build windows && cgo

package mfenc

// testtex_windows.go - GATE-ONLY shared-texture factory. See mf_shim.h's long note: the encoder
// child's IDXGIKeyedMutex branch (risk R3) and its TYPELESS/exotic-format refusal (risk R4) cannot
// be reached with any Spout sender on any rig available here, and the child resolves its handle
// through a Go callback, so a texture created HERE is the only instrument that executes them.
//
// It lives in a non-test file because cgo is not available in _test.go, the same reason
// videoshare's rave_spout_flip_rows parity seam does. Nothing in the product calls it.

// #include <stdlib.h>
// #include "mf_shim.h"
import "C"

import (
	"errors"
	"unsafe"
)

// DXGI formats the gates need by name (the child's allowlist is what is under test).
const (
	dxgiB8G8R8A8UNorm    = 87
	dxgiR8G8B8A8UNorm    = 28
	dxgiB8G8R8A8Typeless = 90
	dxgiR10G10B10A2UNorm = 24
)

// testTexture is a shared D3D11 texture created for a gate, plus the handle a consumer opens.
type testTexture struct {
	h     unsafe.Pointer
	Share uint64
}

// newTestTexture creates a w*h shared texture of DXGI format fmt on adapterLUID (0 = default).
// keyed selects MISC_SHARED_KEYEDMUTEX over MISC_SHARED. pixels (may be nil) must already be in
// fmt's byte order, w*h*4 bytes; it is uploaded under the keyed mutex when there is one and then
// flushed, so a foreign device really sees content - which is what lets the gate assert PIXELS.
func newTestTexture(adapterLUID int64, w, h int, fmt uint32, keyed bool, pixels []byte) (*testTexture, error) {
	var share C.ulonglong
	errbuf := make([]byte, 160)
	var px *C.uint8_t
	if len(pixels) >= w*h*4 {
		px = (*C.uint8_t)(unsafe.Pointer(&pixels[0]))
	}
	k := C.int(0)
	if keyed {
		k = 1
	}
	p := C.mf_testtex_create(C.int64_t(adapterLUID), C.int(w), C.int(h), C.uint(fmt), k, px,
		&share, (*C.char)(unsafe.Pointer(&errbuf[0])), C.int(len(errbuf)))
	if p == nil {
		return nil, errors.New("mfenc: test texture: " + cstr(errbuf))
	}
	return &testTexture{h: p, Share: uint64(share)}, nil
}

// Close releases the texture and the device that owns it.
func (t *testTexture) Close() {
	if t != nil && t.h != nil {
		C.mf_testtex_release(t.h)
		t.h = nil
	}
}

// cstr trims a C string out of a Go byte buffer.
func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
