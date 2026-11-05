package hosts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// systemWriteLock serialises read/write pairs, including compensation, across
// every entry point (UI apply, tray toggle, remote refresh).
var systemWriteLock sync.Mutex

// ApplyOutcome is the result of one apply. PreviousBytes is the exact
// pre-write snapshot (including encoding, BOM and newlines) so compensation
// can restore a legacy-encoded file byte for byte.
type ApplyOutcome struct {
	PreviousContent string
	PreviousBytes   []byte
	NewContent      string
	Unchanged       bool
}

// Apply writes aggregatedContent to the real system hosts file using
// writeMode ("append" or anything else = overwrite).
func Apply(aggregatedContent, writeMode, backupDir string) (ApplyOutcome, error) {
	systemWriteLock.Lock()
	defer systemWriteLock.Unlock()
	target, err := SystemHostsPath()
	if err != nil {
		return ApplyOutcome{}, err
	}
	return applyAt(target, aggregatedContent, writeMode, backupDir)
}

// ApplyAt is Apply against an explicit target. Exposed for tests so they
// never touch the real system hosts file.
func ApplyAt(target, aggregatedContent, writeMode, backupDir string) (ApplyOutcome, error) {
	systemWriteLock.Lock()
	defer systemWriteLock.Unlock()
	return applyAt(target, aggregatedContent, writeMode, backupDir)
}

func applyAt(target, aggregatedContent, writeMode, backupDir string) (ApplyOutcome, error) {
	if err := ValidateSystemText(aggregatedContent); err != nil {
		return ApplyOutcome{}, fmt.Errorf("invalid hosts content: %w", err)
	}
	contentLF := Normalize(aggregatedContent)

	// Compensation needs a trustworthy pre-write snapshot. An unreadable file
	// must not be mistaken for an empty file and later "restored" to empty.
	previousBytes, err := readSystemHostsBytes(target)
	if err != nil {
		return ApplyOutcome{}, err
	}
	previousRaw, err := DecodeSystem(previousBytes)
	if err != nil {
		return ApplyOutcome{}, fmt.Errorf("read %s: %w", target, err)
	}
	previousLF := NormalizeLineEndings(previousRaw)

	var finalContentLF string
	if writeMode == "append" {
		finalContentLF = MakeAppendContent(Normalize(previousLF), contentLF)
	} else {
		finalContentLF = contentLF
	}
	diskContent := RestoreLineEndings(finalContentLF)
	diskBytes := []byte(diskContent)

	outcome := ApplyOutcome{
		PreviousContent: previousLF,
		PreviousBytes:   previousBytes,
		NewContent:      finalContentLF,
	}
	if bytes.Equal(previousBytes, diskBytes) {
		outcome.Unchanged = true
		return outcome, nil
	}

	// A text-only history cannot recover the original encoding. Persist an
	// exact snapshot before converting a legacy/UTF-16 file, even when
	// history is disabled. If the backup fails, leave system hosts untouched.
	if !bytes.Equal(previousBytes, []byte(previousRaw)) {
		if err := backupBeforeConversion(backupDir, previousBytes); err != nil {
			return ApplyOutcome{}, err
		}
	}

	err = os.WriteFile(target, diskBytes, 0o644)
	if err != nil {
		if isPermissionDenied(err) {
			// Fall back to OS-native elevation. The user gets the OS UAC /
			// pkexec / macOS auth dialog; we never collect a password ourselves.
			err = writePrivileged(target, diskBytes)
		}
		if err != nil {
			return ApplyOutcome{}, fmt.Errorf("write %s: %w", target, err)
		}
	}
	return outcome, nil
}

// Restore puts the exact pre-apply file back, but only while our write is
// still current. A later external write must never be overwritten.
func Restore(previous, expected string, previousBytes []byte) error {
	systemWriteLock.Lock()
	defer systemWriteLock.Unlock()
	target, err := SystemHostsPath()
	if err != nil {
		return err
	}
	return restoreAt(target, previous, expected, previousBytes)
}

// RestoreAt is Restore against an explicit target (tests).
func RestoreAt(target, previous, expected string, previousBytes []byte) error {
	systemWriteLock.Lock()
	defer systemWriteLock.Unlock()
	return restoreAt(target, previous, expected, previousBytes)
}

var errContentChanged = errors.New("system hosts changed since the last apply; restore refused")

// ErrContentChanged is returned by Restore when an external tool wrote the
// hosts file after our apply.
var ErrContentChanged = errContentChanged

func restoreAt(target, previous, expected string, previousBytes []byte) error {
	current, err := readSystemHostsBytes(target)
	if err != nil {
		return err
	}
	if !hostsBytesMatch(current, expected, nil) {
		return ErrContentChanged
	}
	fallback := []byte(RestoreLineEndings(NormalizeLineEndings(previous)))
	content := previousBytes
	if content == nil {
		content = fallback
	}
	decoded, err := DecodeSystem(content)
	if err != nil {
		return fmt.Errorf("read restore snapshot: %w", err)
	}
	if NormalizeLineEndings(decoded) != NormalizeLineEndings(previous) {
		return errors.New("restore snapshot does not match previous hosts content")
	}
	if bytes.Equal(current, content) {
		return nil
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		if isPermissionDenied(err) {
			return writePrivileged(target, content)
		}
		return fmt.Errorf("restore %s: %w", target, err)
	}
	return nil
}

// SystemHostsMatches reports whether the current on-disk bytes are exactly
// what we last applied. Decoding before comparing would hide external
// encoding-only changes.
func SystemHostsMatches(expected string, snapshot []byte) bool {
	target, err := SystemHostsPath()
	if err != nil {
		return false
	}
	return SystemHostsMatchesAt(target, expected, snapshot)
}

// SystemHostsMatchesAt is SystemHostsMatches against an explicit file. It
// lets a sandboxed target be checked without reading the OS hosts file.
func SystemHostsMatchesAt(target, expected string, snapshot []byte) bool {
	systemWriteLock.Lock()
	defer systemWriteLock.Unlock()
	current, err := readSystemHostsBytes(target)
	if err != nil {
		return false
	}
	return hostsBytesMatch(current, expected, snapshot)
}

func hostsBytesMatch(current []byte, expected string, snapshot []byte) bool {
	if snapshot != nil {
		return bytes.Equal(current, snapshot)
	}
	return bytes.Equal(current, []byte(RestoreLineEndings(NormalizeLineEndings(expected))))
}

// ReadSystemHosts decodes the current system hosts file as text with LF
// normalisation, for display in the editor.
func ReadSystemHosts() (string, error) {
	target, err := SystemHostsPath()
	if err != nil {
		return "", err
	}
	return ReadSystemHostsAt(target)
}

// ReadSystemHostsAt decodes an explicit hosts file the same way
// ReadSystemHosts decodes the OS one. Tests use it with a sandboxed target.
func ReadSystemHostsAt(target string) (string, error) {
	b, err := readSystemHostsBytes(target)
	if err != nil {
		return "", err
	}
	text, err := DecodeSystem(b)
	if err != nil {
		return "", err
	}
	return NormalizeLineEndings(text), nil
}

func readSystemHostsBytes(target string) ([]byte, error) {
	b, err := os.ReadFile(target)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", target, err)
	}
	return b, nil
}

var backupSequence atomic.Uint64

// backupBeforeConversion stores a byte-exact snapshot before we replace a
// UTF-16 / ANSI / BOM-encoded hosts file with UTF-8.
func backupBeforeConversion(dir string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	name := fmt.Sprintf("hosts-before-utf8-%d-%d-%d.bin",
		time.Now().UnixNano(), os.Getpid(), backupSequence.Add(1))
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("backup hosts before encoding conversion %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("backup hosts before encoding conversion %s: %w", path, err)
	}
	return nil
}

func isPermissionDenied(err error) bool {
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	// Windows reports ERROR_ACCESS_DENIED with a message rather than a typed
	// error in some paths; match it so the UAC fallback still triggers.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "operation not permitted")
}

// RunCommandAfterApply is a helper for the optional cmd_after_hosts_apply
// preference. It is intentionally separate so callers can decide whether to
// run it; the Go build runs it in a goroutine, never on the UI thread.
func RunCommandAfterApply(script string) error {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	return runShellCommand(script)
}
