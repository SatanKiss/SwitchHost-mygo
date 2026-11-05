//go:build windows

package hosts

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// SystemHostsPath resolves the real hosts path from the system Windows
// directory, rather than assuming C:\Windows (which is wrong on systems
// installed on another drive).
func SystemHostsPath() (string, error) {
	dir, err := systemWindowsDir()
	if err != nil {
		return "", err
	}
	// "D:" is drive-relative; make it rooted before joining.
	if len(dir) == 2 && isASCIIAlpha(dir[0]) && dir[1] == ':' {
		dir += `\`
	}
	return filepath.Join(dir, "System32", "drivers", "etc", "hosts"), nil
}

func isASCIIAlpha(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func systemWindowsDir() (string, error) {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemWindowsDirectoryW")
	buf := make([]uint16, 260)
	for {
		ret, _, callErr := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if ret == 0 {
			return "", fmt.Errorf("GetSystemWindowsDirectoryW failed: %w", callErr)
		}
		if int(ret) < len(buf) {
			return syscall.UTF16ToString(buf[:int(ret)]), nil
		}
		buf = make([]uint16, int(ret)+1)
	}
}

func runShellCommand(script string) error {
	cmd := exec.Command("cmd.exe", "/c", strings.TrimSpace(script))
	return cmd.Run()
}
