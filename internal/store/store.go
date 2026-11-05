package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/switchhosts/go-native/internal/model"
)

// HistoryItem is one entry of internal/histories/system-hosts.json. The file
// is a bare JSON array (no format envelope), matching the Tauri build.
type HistoryItem struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	AddTimeMs int64  `json:"add_time_ms"`
	Label     string `json:"label,omitempty"`
}

// Store owns the in-memory view of the v5 data. It deliberately does no
// locking: internal/app.Service serialises all access with one mutex, which
// keeps multi-step operations (toggle -> save -> apply) atomic.
type Store struct {
	Paths Paths

	Nodes     []*model.Node
	Config    model.Config
	cfgExtras map[string]any

	// state is the whole internal/state.json object. Unknown sub-fields written
	// by another SwitchHosts version are preserved verbatim on save.
	state map[string]any

	Trash   []map[string]any
	History []HistoryItem
}

// OpenAt opens the v5 layout rooted at root.
func OpenAt(root string) (*Store, error) {
	p := Under(root)
	if err := p.EnsureUsable(); err != nil {
		return nil, err
	}
	s := &Store{
		Paths:     p,
		Config:    model.DefaultConfig(),
		cfgExtras: map[string]any{},
		state:     map[string]any{},
		Trash:     []map[string]any{},
		History:   []HistoryItem{},
	}
	p.CleanupTmpFiles()
	if err := s.LoadAll(); err != nil {
		return nil, err
	}
	return s, nil
}

// Open resolves the active root (honouring the data-dir pointer) and opens it.
func Open() (*Store, *Recovery, error) {
	paths, recovery, err := Resolve()
	if err != nil {
		return nil, nil, err
	}
	s, err := OpenAt(paths.Root)
	if err != nil {
		return nil, recovery, err
	}
	return s, recovery, nil
}

// LoadAll reads every v5 file into memory. Missing files produce in-memory
// defaults; corrupt files are left on disk and reported so the UI can show a
// warning rather than silently overwriting user data.
func (s *Store) LoadAll() error {
	if err := s.loadManifest(); err != nil {
		return err
	}
	if err := s.loadState(); err != nil {
		return err
	}
	if err := s.loadConfig(); err != nil {
		return err
	}
	if err := s.loadTrashcan(); err != nil {
		return err
	}
	if err := s.loadHistory(); err != nil {
		return err
	}
	return nil
}

func (s *Store) loadManifest() error {
	var raw map[string]any
	found, err := ReadOptionalJSON(s.Paths.ManifestFile, &raw)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if !found {
		// A fresh installation has no manifest. Seed the standard system-hosts
		// node so the UI has something sensible, without running a migration.
		s.Nodes = []*model.Node{{
			ID: model.SystemNodeID, Type: model.TypeLocal,
			Title: "System Hosts", On: true, IsSys: true,
			Extras: map[string]any{},
		}}
		return nil
	}
	var items []any
	if v, ok := raw["root"]; ok {
		items, _ = v.([]any)
	}
	// Tolerate a legacy bare-array manifest too.
	if items == nil {
		if arr, ok := raw["root"].([]any); ok {
			items = arr
		}
	}
	s.Nodes = model.NodesFromMaps(items)
	return nil
}

func (s *Store) loadState() error {
	found, err := ReadOptionalJSON(s.Paths.StateFile, &s.state)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if !found || s.state == nil {
		s.state = map[string]any{}
	}
	tree, _ := s.state["tree"].(map[string]any)
	if tree == nil {
		return nil
	}
	ids, _ := tree["collapsedNodeIds"].([]any)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if v, ok := id.(string); ok {
			out = append(out, v)
		}
	}
	model.ApplyCollapsedIDs(s.Nodes, out)
	return nil
}

func (s *Store) loadConfig() error {
	b, err := os.ReadFile(s.Paths.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg, extras := model.ParseConfig(b)
	s.Config, s.cfgExtras = cfg, extras
	return nil
}

func (s *Store) loadTrashcan() error {
	var raw map[string]any
	found, err := ReadOptionalJSON(s.Paths.TrashcanFile, &raw)
	if err != nil {
		return fmt.Errorf("trashcan: %w", err)
	}
	if !found {
		return nil
	}
	items, _ := raw["items"].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			s.Trash = append(s.Trash, m)
		}
	}
	return nil
}

func (s *Store) loadHistory() error {
	b, err := os.ReadFile(s.Paths.HistoryFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	// Never silently turn a corrupt journal into an empty one: the next write
	// would destroy the user's originals.
	if err := json.Unmarshal(b, &s.History); err != nil {
		return fmt.Errorf("parse history: %w", err)
	}
	return nil
}

// SaveManifest writes internal/state.json first (collapse state), then
// manifest.json. A crash between the two leaves a slightly stale collapse
// state rather than an out-of-date tree.
func (s *Store) SaveManifest() error {
	tree, _ := s.state["tree"].(map[string]any)
	if tree == nil {
		tree = map[string]any{}
	}
	tree["collapsedNodeIds"] = model.CollapsedIDs(s.Nodes)
	s.state["tree"] = tree
	s.state["format"] = model.StateFormat
	s.state["schemaVersion"] = model.StateSchemaVersion
	if err := WriteJSON(s.Paths.StateFile, s.state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	envelope := map[string]any{
		"format":        model.ManifestFormat,
		"schemaVersion": model.ManifestSchemaVersion,
		"root":          model.NodesToMaps(s.Nodes),
	}
	if err := WriteJSON(s.Paths.ManifestFile, envelope); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}
	return nil
}

func (s *Store) SaveConfig() error {
	return WriteJSON(s.Paths.ConfigFile, model.ConfigToMap(s.Config, s.cfgExtras))
}

func (s *Store) SaveTrashcan() error {
	return WriteJSON(s.Paths.TrashcanFile, map[string]any{
		"format":        model.TrashcanFormat,
		"schemaVersion": model.TrashcanSchemaVersion,
		"items":         s.Trash,
	})
}

func (s *Store) SaveHistory() error {
	return WriteJSON(s.Paths.HistoryFile, s.History)
}

var historyCounter atomic.Uint64

// RecordChange appends the pre-apply and post-apply snapshots to the history
// journal, skipping a repeated trailing snapshot and trimming to the
// configured limit (zero means unlimited).
func (s *Store) RecordChange(previous, next string) error {
	if !s.Config.HistoryEnabled || previous == next {
		return nil
	}
	now := time.Now().UnixMilli()
	for _, content := range []string{previous, next} {
		if len(s.History) > 0 && s.History[len(s.History)-1].Content == content {
			continue
		}
		s.History = append(s.History, HistoryItem{
			ID:        fmt.Sprintf("apply_%d_%d_%d", now, os.Getpid(), historyCounter.Add(1)),
			Content:   content,
			AddTimeMs: now,
		})
	}
	limit := s.Config.HistoryLimit
	if limit > 0 && len(s.History) > limit {
		s.History = s.History[len(s.History)-limit:]
	}
	return s.SaveHistory()
}

// TrashItems returns the raw trashcan items. Each item has
// {data: node, add_time_ms: number, parent_id: string|null}.
func (s *Store) TrashItems() []map[string]any {
	out := make([]map[string]any, len(s.Trash))
	copy(out, s.Trash)
	return out
}

func trashItemID(item map[string]any) string {
	data, _ := item["data"].(map[string]any)
	id, _ := data["id"].(string)
	return id
}

// AddTrash moves a detached node into the trashcan.
func (s *Store) AddTrash(node *model.Node, parentID string) error {
	var parent any
	if parentID != "" {
		parent = parentID
	}
	s.Trash = append(s.Trash, map[string]any{
		"data":        node.ToMap(),
		"add_time_ms": time.Now().UnixMilli(),
		"parent_id":   parent,
	})
	return s.SaveTrashcan()
}

// TrashIndex finds an item by the nested data.id.
func (s *Store) TrashIndex(id string) int {
	for i, item := range s.Trash {
		if trashItemID(item) == id {
			return i
		}
	}
	return -1
}

// TakeTrash removes and returns a trash item.
func (s *Store) TakeTrash(id string) (map[string]any, bool) {
	i := s.TrashIndex(id)
	if i < 0 {
		return nil, false
	}
	item := s.Trash[i]
	s.Trash = append(s.Trash[:i:i], s.Trash[i+1:]...)
	return item, true
}
