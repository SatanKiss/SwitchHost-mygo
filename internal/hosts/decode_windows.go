//go:build windows

package hosts

import (
	"fmt"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const mbErrInvalidChars = 0x00000008

// decodeLegacyEncoding decodes a Windows ANSI code page hosts file using
// GetACP + MultiByteToWideChar. MB_ERR_INVALID_CHARS makes truncated or
// invalid multibyte sequences fail instead of silently losing bytes.
//
// This uses only the Go standard library's syscall package (LoadLibrary /
// GetProcAddress), so the app stays pure Go with no cgo and no C toolchain.
func decodeLegacyEncoding(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getACP := kernel32.NewProc("GetACP")
	toWide := kernel32.NewProc("MultiByteToWideChar")

	cp, _, callErr := getACP.Call()
	if cp == 0 || cp > 0xffff {
		return "", fmt.Errorf("GetACP failed: %w", callErr)
	}
	src := uintptr(0)
	if len(data) > 0 {
		src = uintptr(unsafe.Pointer(&data[0]))
	}
	size, _, _ := toWide.Call(cp, mbErrInvalidChars, src, uintptr(len(data)), 0, 0)
	if size == 0 {
		return "", fmt.Errorf("cannot decode hosts file using Windows code page %d", cp)
	}
	wide := make([]uint16, int(size))
	written, _, _ := toWide.Call(cp, mbErrInvalidChars, src, uintptr(len(data)),
		uintptr(unsafe.Pointer(&wide[0])), size)
	runtime.KeepAlive(data)
	if written == 0 {
		return "", fmt.Errorf("cannot decode hosts file using Windows code page %d", cp)
	}
	return string(utf16.Decode(wide[:int(written)])), nil
}
