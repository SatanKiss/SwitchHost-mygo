//go:build windows

package hosts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ElevationHelperArg marks this process as a UAC elevation helper. The
// elevated child receives it in argv[1] with the staged file in argv[2], and
// exits without ever creating a window.
const ElevationHelperArg = "--swh-elevated-apply-hosts"

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
	swHide                = 0
	coInitApartmentThread = 0x00000002
	waitInfinite          = 0xffffffff
	errorCancelled        = 1223
)

// SHELLEXECUTEINFOW. Field order and padding match the Win32 struct on both
// 32-bit and 64-bit Windows.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       uintptr
	lpFile       uintptr
	lpParameters uintptr
	lpDirectory  uintptr
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      uintptr
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     uintptr
}

// RunElevationHelper is called only in the elevated child process. It writes
// the staged bytes over the resolved system hosts path and exits.
func RunElevationHelper(src string) error {
	target, err := SystemHostsPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read staged hosts content: %w", err)
	}
	if err := ValidateSystemText(string(data)); err != nil {
		return err
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return fmt.Errorf("elevated write %s: %w", target, err)
	}
	return nil
}

// writePrivileged re-launches our own binary with the runas verb. Using our
// own binary (rather than cmd /c copy) avoids cmd.exe's %VAR% expansion of
// paths and lets the child resolve the hosts path itself, so no untrusted
// destination can be injected through the command line.
func writePrivileged(target string, content []byte) error {
	canonical, err := SystemHostsPath()
	if err != nil {
		return err
	}
	if !sameHostsPath(target, canonical) {
		return fmt.Errorf("refusing to elevate write to non-hosts target: %s", target)
	}

	src := filepath.Join(os.TempDir(), fmt.Sprintf("swh_apply_%d_%d.hosts", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(src, content, 0o600); err != nil {
		return fmt.Errorf("stage temp file: %w", err)
	}
	defer os.Remove(src)

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("current exe: %w", err)
	}
	params := ElevationHelperArg + ` "` + src + `"`

	// ShellExecuteExW needs an STA thread. Lock the goroutine to an OS thread
	// and initialise COM there, then wait for the elevated child.
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		result <- shellExecuteRunAs(exe, params)
	}()
	if err := <-result; err != nil {
		return err
	}

	// The exit code alone is not proof: verify the bytes actually landed.
	got, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("verify elevated write: %w", err)
	}
	if !bytes.Equal(got, content) {
		return errors.New("elevated write did not produce the expected hosts content")
	}
	return nil
}

func shellExecuteRunAs(exe, params string) error {
	ole32 := syscall.NewLazyDLL("ole32.dll")
	coInit := ole32.NewProc("CoInitializeEx")
	coUninit := ole32.NewProc("CoUninitialize")
	shell32 := syscall.NewLazyDLL("shell32.dll")
	shellExec := shell32.NewProc("ShellExecuteExW")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	wait := kernel32.NewProc("WaitForSingleObject")
	exitCode := kernel32.NewProc("GetExitCodeProcess")
	closeHandle := kernel32.NewProc("CloseHandle")

	coInit.Call(0, coInitApartmentThread)
	defer coUninit.Call()

	verb, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	paramPtr, err := syscall.UTF16PtrFromString(params)
	if err != nil {
		return err
	}

	sei := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		lpVerb:       uintptr(unsafe.Pointer(verb)),
		lpFile:       uintptr(unsafe.Pointer(file)),
		lpParameters: uintptr(unsafe.Pointer(paramPtr)),
		nShow:        swHide,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	runtime.KeepAlive(verb)
	runtime.KeepAlive(file)
	runtime.KeepAlive(paramPtr)

	ok, _, callErr := shellExec.Call(uintptr(unsafe.Pointer(&sei)))
	if ok == 0 {
		if errno, isErrno := callErr.(syscall.Errno); isErrno && uintptr(errno) == errorCancelled {
			return errors.New("elevation was cancelled by the user")
		}
		return fmt.Errorf("ShellExecuteExW(runas) failed: %w", callErr)
	}
	if sei.hProcess == 0 {
		return nil
	}
	defer closeHandle.Call(sei.hProcess)
	wait.Call(sei.hProcess, waitInfinite)
	var code uint32
	exitCode.Call(sei.hProcess, uintptr(unsafe.Pointer(&code)))
	if code != 0 {
		return fmt.Errorf("elevated helper exited with code %d", code)
	}
	return nil
}

func sameHostsPath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
