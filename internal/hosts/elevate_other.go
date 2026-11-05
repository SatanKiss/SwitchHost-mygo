//go:build !windows

package hosts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ElevationHelperArg is unused on macOS/Linux; the platform helpers copy a
// staged file directly instead of re-launching this binary.
const ElevationHelperArg = ""

// RunElevationHelper is only meaningful on Windows.
func RunElevationHelper(string) error {
	return errors.New("elevation helper is only used on Windows")
}

// writePrivileged stages the content in a temp file (no privilege required)
// and asks the OS to copy it over the hosts file:
//
//   - Linux: pkexec /bin/cp
//   - macOS: osascript "do shell script ... with administrator privileges"
//
// Both show the OS-native credential dialog; the app never asks for a
// password in its own UI. No cgo and no browser/webview is involved.
func writePrivileged(target string, content []byte) error {
	canonical, err := SystemHostsPath()
	if err != nil {
		return err
	}
	if filepath.Clean(target) != filepath.Clean(canonical) {
		return fmt.Errorf("refusing to elevate write to non-hosts target: %s", target)
	}
	src := filepath.Join(os.TempDir(), fmt.Sprintf("swh_apply_%d_%d.hosts", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(src, content, 0o600); err != nil {
		return fmt.Errorf("stage temp file: %w", err)
	}
	defer os.Remove(src)

	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		shell := "cp -f " + shellQuote(src) + " " + shellQuote(target)
		script := `do shell script "` + appleScriptQuote(shell) + `" with administrator privileges`
		cmd = exec.Command("osascript", "-e", script)
	} else {
		cmd = exec.Command("pkexec", "/bin/cp", "-f", src, target)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("privileged hosts write failed: %w", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("verify privileged write: %w", err)
	}
	if !bytes.Equal(got, content) {
		return errors.New("privileged write did not produce the expected hosts content")
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
