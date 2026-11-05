package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AtomicWrite writes data to a sibling "<dest>.tmp" and renames it onto dest.
// A rename on the same filesystem is atomic, so a crash mid-write either
// leaves the previous file intact or lands the new one — never a half-written
// file. This mirrors src-tauri/src/storage/atomic.rs exactly.
func AtomicWrite(dest string, data []byte) error {
	if dir := filepath.Dir(dest); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, dest, err)
	}
	return nil
}

// WriteJSON pretty-prints v with two-space indentation and writes it
// atomically, matching serde_json::to_vec_pretty output.
func WriteJSON(dest string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", dest, err)
	}
	b = append(b, '\n')
	return AtomicWrite(dest, b)
}

func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return append(b, '\n')
}

// ReadJSON reads a JSON file. A missing file is reported as os.ErrNotExist so
// callers can create an in-memory default without overwriting anything.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// ReadOptionalJSON is ReadJSON but treats a missing file as "no data".
func ReadOptionalJSON(path string, v any) (bool, error) {
	err := ReadJSON(path, v)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ValidEntryID defends entries/<id>.hosts against path traversal. Mirrors
// validate_id in src-tauri/src/storage/entries.rs.
func ValidEntryID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\:`+"\x00") || strings.Contains(id, "..") {
		return fmt.Errorf("illegal entry id: %q", id)
	}
	return nil
}

// NormalizeToLF collapses CRLF and lone CR to LF.
func NormalizeToLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// ReadEntry returns a node's content, LF-normalized. A missing file yields an
// empty string, matching the Electron and Tauri builds.
func (p Paths) ReadEntry(id string) (string, error) {
	if err := ValidEntryID(id); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(p.EntriesDir, id+".hosts"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read entry %s: %w", id, err)
	}
	return NormalizeToLF(string(b)), nil
}

// WriteEntry stores a node's content as UTF-8 with LF line endings.
func (p Paths) WriteEntry(id, content string) error {
	if err := ValidEntryID(id); err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(p.EntriesDir, id+".hosts"), []byte(NormalizeToLF(content)))
}

// DeleteEntry removes a node's content file; a missing file is a no-op.
func (p Paths) DeleteEntry(id string) error {
	if err := ValidEntryID(id); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(p.EntriesDir, id+".hosts"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
