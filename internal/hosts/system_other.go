//go:build !windows

package hosts

import (
	"os/exec"
	"strings"
)

// SystemHostsPath is /etc/hosts on macOS and Linux.
func SystemHostsPath() (string, error) {
	return "/etc/hosts", nil
}

func runShellCommand(script string) error {
	return exec.Command("sh", "-c", strings.TrimSpace(script)).Run()
}
