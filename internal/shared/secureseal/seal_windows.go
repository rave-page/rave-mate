//go:build windows

package secureseal

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows DPAPI (CurrentUser scope): the OS derives the key from the user's login
// credentials, so the sealed blob is only decryptable by this user on this machine -
// the same guarantee Electron's safeStorage relies on. UI-forbidden = never prompt.

const available = true

var (
	crypt32                = windows.NewLazySystemDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

const cryptprotectUIForbidden = 0x1

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(d []byte) dataBlob {
	if len(d) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(d)), pbData: &d[0]}
}

func (b dataBlob) bytes() []byte {
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

// Seal encrypts plain via DPAPI (CurrentUser).
func Seal(plain []byte) ([]byte, error) {
	in := newBlob(plain)
	var out dataBlob
	r, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		cryptprotectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer func() { _, _, _ = procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData))) }()
	return out.bytes(), nil
}

// Unseal decrypts a blob produced by Seal.
func Unseal(sealed []byte) ([]byte, error) {
	in := newBlob(sealed)
	var out dataBlob
	r, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		cryptprotectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer func() { _, _, _ = procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData))) }()
	return out.bytes(), nil
}
