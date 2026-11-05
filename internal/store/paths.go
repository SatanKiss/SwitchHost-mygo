// Package store reads and writes the SwitchHosts v5 on-disk layout.
//
// It is a pure-Go reimplementation of src-tauri/src/storage/*.rs. No migration
// is involved: the Go build opens the very same files an existing Tauri
// SwitchHosts 5.x installation uses, so installations can be switched back and
// forth without exporting anything.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DataDirName is the sub-directory created inside a user-picked folder.
const DataDirName = "SwitchHosts.data"

const (
	PointerDirName  = "net.oldj.switchhosts"
	PointerFileName = "data_dir.json"
	PointerFormat   = "switchhosts-data-dir-pointer"
)

// Paths is the absolute v5 filesystem layout. It is just a bundle of strings,
// safe to copy; no file handles are kept.
type Paths struct {
	Root          string
	ManifestFile  string
	TrashcanFile  string
	EntriesDir    string
	Internal      string
	ConfigFile    string
	StateFile     string
	HistoriesDir  string
	HistoryFile   string
	BackupsDir    string
	ElevationFlag string
}

// Under builds the layout rooted at root. Tests use it to point the layout at
// a temporary directory.
func Under(root string) Paths {
	return Paths{
		Root:          root,
		ManifestFile:  filepath.Join(root, "manifest.json"),
		TrashcanFile:  filepath.Join(root, "trashcan.json"),
		EntriesDir:    filepath.Join(root, "entries"),
		Internal:      filepath.Join(root, "internal"),
		ConfigFile:    filepath.Join(root, "internal", "config.json"),
		StateFile:     filepath.Join(root, "internal", "state.json"),
		HistoriesDir:  filepath.Join(root, "internal", "histories"),
		HistoryFile:   filepath.Join(root, "internal", "histories", "system-hosts.json"),
		BackupsDir:    filepath.Join(root, "internal", "backups"),
		ElevationFlag: filepath.Join(root, "internal", ".go-native-elevation"),
	}
}

// DefaultRoot is ~/.SwitchHosts, independent of any custom pointer.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, ".SwitchHosts"), nil
}

// PointerPath is where a custom data directory is recorded. It lives outside
// the data directory itself so it can be read before the layout is known.
// os.UserConfigDir matches dirs::config_dir() on all three desktop platforms.
func PointerPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config directory: %w", err)
	}
	return filepath.Join(cfg, PointerDirName, PointerFileName), nil
}

// Recovery explains why the app fell back to the default root. An absent
// pointer is the normal case and reports no recovery; a present-but-unusable
// pointer must not silently strand a user who had a custom directory.
type Recovery struct {
	Kind string // "missing" | "invalid"
	Path string
}

func (r *Recovery) String() string {
	if r == nil {
		return ""
	}
	switch r.Kind {
	case "missing":
		return "custom data directory is unavailable: " + r.Path
	default:
		return "data directory pointer is corrupt or unreadable"
	}
}

type pointerEnvelope struct {
	Format        string `json:"format"`
	SchemaVersion int    `json:"schemaVersion"`
	DataDir       string `json:"data_dir"`
}

// ReadPointer returns (path, present, valid). A dangling symlink counts as
// present but invalid, so a reset can still clear it.
func ReadPointer() (string, bool, bool) {
	path, err := PointerPath()
	if err != nil {
		return "", false, false
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, true
		}
		return "", true, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", true, false
	}
	var p pointerEnvelope
	if err := json.Unmarshal(data, &p); err != nil {
		return "", true, false
	}
	if !filepath.IsAbs(p.DataDir) || strings.TrimSpace(p.DataDir) == "" {
		return "", true, false
	}
	return p.DataDir, true, true
}

// WritePointer records a custom data root.
func WritePointer(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("data directory must be absolute: %q", root)
	}
	path, err := PointerPath()
	if err != nil {
		return err
	}
	return AtomicWrite(path, mustJSON(pointerEnvelope{
		Format:        PointerFormat,
		SchemaVersion: 1,
		DataDir:       root,
	}))
}

// ClearPointer removes the pointer; a missing pointer is success.
func ClearPointer() error {
	path, err := PointerPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Resolve picks the active data root, honouring the pointer, and reports a
// recovery condition when a custom directory cannot be used.
func Resolve() (Paths, *Recovery, error) {
	def, err := DefaultRoot()
	if err != nil {
		return Paths{}, nil, err
	}
	custom, present, valid := ReadPointer()
	if !present {
		return Under(def), nil, nil
	}
	if !valid {
		return Under(def), &Recovery{Kind: "invalid"}, nil
	}
	info, err := os.Stat(custom)
	if err != nil || !info.IsDir() {
		return Under(def), &Recovery{Kind: "missing", Path: custom}, nil
	}
	return Under(custom), nil, nil
}

// ResolveTargetDir maps a user-picked folder to a data directory. Picking the
// default root is a reset, never `<default>/SwitchHosts.data`.
func ResolveTargetDir(picked, def string) (string, bool) {
	if samePath(picked, def) {
		return "", true
	}
	if filepath.Base(picked) == DataDirName {
		return picked, false
	}
	return filepath.Join(picked, DataDirName), false
}

func samePath(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		aa = filepath.Clean(a)
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		bb = filepath.Clean(b)
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

// EnsureDirs creates every directory the layout owns. Does not touch files.
func (p Paths) EnsureDirs() error {
	for _, dir := range []string{p.Root, p.EntriesDir, p.Internal, p.HistoriesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// EnsureUsable also verifies each owned directory is writable. This catches
// read-only or blocked sub-directories before any data write is attempted.
func (p Paths) EnsureUsable() error {
	if err := p.EnsureDirs(); err != nil {
		return err
	}
	for _, dir := range []string{p.Root, p.EntriesDir, p.Internal, p.HistoriesDir} {
		probe, err := os.CreateTemp(dir, ".writable-*.tmp")
		if err != nil {
			return fmt.Errorf("%s is not writable: %w", dir, err)
		}
		name := probe.Name()
		_ = probe.Close()
		_ = os.Remove(name)
	}
	return nil
}

// CleanupTmpFiles removes leftover .tmp files from atomic writes that survived
// a crash. A leftover .tmp is always a partial write that never became the
// real file.
func (p Paths) CleanupTmpFiles() {
	for _, dir := range []string{p.Root, p.EntriesDir, p.Internal, p.HistoriesDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".tmp" {
				continue
			}
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
