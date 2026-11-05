//go:build windows

package i18n

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// LOCALE_NAME_MAX_LENGTH from winnls.h.
const localeNameMaxLength = 85

// detectOSLocale asks Windows for the *user's* display language, not the
// system install language. Both GetUserDefaultLocaleName and
// GetSystemDefaultLocaleName live in kernel32.dll and are called through Go's
// syscall.LazyProc: no cgo, no C compiler, no additional DLL dependency.
//
// The env fallbacks matter for CI and for shells that export LANG (Git Bash,
// MSYS), where the Win32 API would otherwise report the OS install language.
func detectOSLocale() string {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	for _, name := range []string{"GetUserDefaultLocaleName", "GetSystemDefaultLocaleName"} {
		proc := kernel32.NewProc(name)
		if err := proc.Find(); err != nil {
			continue
		}
		buf := make([]uint16, localeNameMaxLength)
		n, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 || int(n) > len(buf) {
			continue
		}
		runtime.KeepAlive(buf)
		if s := syscall.UTF16ToString(buf[:n-1]); s != "" {
			return s
		}
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		if s := os.Getenv(k); s != "" {
			return s
		}
	}
	return ""
}
