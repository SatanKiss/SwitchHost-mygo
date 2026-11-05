// Package app is the single-threaded-by-contract service layer between the
// persistent v5 store and the MyGo native UI.
//
// All access to store.Store is serialised by Service.mu. Operations that
// perform network or privileged filesystem I/O take a consistent snapshot,
// release the lock, do the slow work, then re-acquire and verify before
// committing. The UI never mutates model nodes directly: it calls EditNode /
// Toggle / SetContent, which clone the tree so an in-flight refresh can never
// observe a half-written edit.
package app

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/switchhosts/go-native/internal/hosts"
	"github.com/switchhosts/go-native/internal/model"
	"github.com/switchhosts/go-native/internal/remote"
	"github.com/switchhosts/go-native/internal/store"
)

// Event is a non-blocking notification for the UI. The UI drains Events() on a
// goroutine and schedules a Window.Update; the service never calls into UI
// code while holding its lock.
//
// Message is the final English text (it also backs Service.Status()). Key and
// Args are the localisation payload: when Key is set the UI renders
// translate(Key, Args...) in its active language instead of Message. The
// service itself has no dependency on i18n or on the UI.
type Event struct {
	Kind    string // "tree", "config", "status", "content", "trash"
	Message string
	Key     string
	Args    []any
}

type Service struct {
	mu       sync.Mutex
	statusMu sync.Mutex
	store    *store.Store

	locks  *remote.Locks
	client *http.Client
	events chan Event
	status string

	lastApply   *hosts.ApplyOutcome
	scanStarted bool
	stopScan    chan struct{}

	// hostOps are injectable so tests never touch the real OS hosts file.
	applyFn   func(content, writeMode, backupDir string) (hosts.ApplyOutcome, error)
	restoreFn func(previous, expected string, previousBytes []byte) error
	matchesFn func(expected string, snapshot []byte) bool
	readSysFn func() (string, error)
}

func defaultHostOps(s *Service) {
	s.applyFn = hosts.Apply
	s.restoreFn = hosts.Restore
	s.matchesFn = hosts.SystemHostsMatches
	s.readSysFn = hosts.ReadSystemHosts
}

// UseSandboxHostsFile redirects every privileged/system hosts operation to
// an explicit file. It is for tests and sandboxed embeddings; production
// always uses the OS hosts file. Call it before the first mutation.
func (s *Service) UseSandboxHostsFile(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyFn = func(content, writeMode, backupDir string) (hosts.ApplyOutcome, error) {
		return hosts.ApplyAt(path, content, writeMode, backupDir)
	}
	s.restoreFn = func(previous, expected string, previousBytes []byte) error {
		return hosts.RestoreAt(path, previous, expected, previousBytes)
	}
	s.matchesFn = func(expected string, snapshot []byte) bool {
		return hosts.SystemHostsMatchesAt(path, expected, snapshot)
	}
	s.readSysFn = func() (string, error) { return hosts.ReadSystemHostsAt(path) }
}

// Open resolves the active v5 data directory and loads it.
func Open() (*Service, *store.Recovery, error) {
	st, recovery, err := store.Open()
	if err != nil {
		return nil, recovery, err
	}
	s, err := New(st)
	return s, recovery, err
}

// OpenAt opens an explicit root (used by tests and by --data-dir).
func OpenAt(root string) (*Service, error) {
	st, err := store.OpenAt(root)
	if err != nil {
		return nil, err
	}
	return New(st)
}

func New(st *store.Store) (*Service, error) {
	client, err := remote.NewClient(st.Config)
	if err != nil {
		// A broken proxy config must not make the whole app unopenable: the
		// user can fix it in Preferences. Fall back to a direct client.
		client = &http.Client{Timeout: 30 * time.Second}
	}
	s := &Service{
		store:    st,
		locks:    remote.NewLocks(),
		client:   client,
		events:   make(chan Event, 64),
		status:   "Ready",
		stopScan: make(chan struct{}),
	}
	defaultHostOps(s)
	return s, nil
}

func (s *Service) Events() <-chan Event { return s.events }

func (s *Service) emit(kind, message string) {
	s.emitEvent(Event{Kind: kind, Message: message})
}

// emitKey sends a status line that the UI can translate. The English key is
// itself a fmt format string, so Message stays a plain, human-readable line
// for Status() and for callers that do not localise.
func (s *Service) emitKey(kind, key string, args ...any) {
	s.emitEvent(Event{
		Kind:    kind,
		Message: fmt.Sprintf(key, args...),
		Key:     key,
		Args:    args,
	})
}

func (s *Service) emitEvent(ev Event) {
	s.statusMu.Lock()
	s.status = ev.Message
	s.statusMu.Unlock()
	select {
	case s.events <- ev:
	default: // the UI is behind; it always re-reads full state on the next tick
	}
}

// Status returns the most recent human-readable status line.
func (s *Service) Status() string {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.status
}

func (s *Service) Paths() store.Paths {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Paths
}

func (s *Service) DataDir() string { return s.Paths().Root }

// ---------------------------------------------------------------------------
// Read accessors (always return deep copies)
// ---------------------------------------------------------------------------

func (s *Service) Tree() []*model.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneNodes(s.store.Nodes)
}

func (s *Service) Find(id string) *model.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	return model.FindNode(s.store.Nodes, id).Clone()
}

func (s *Service) Config() model.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Config
}

func (s *Service) Trash() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.TrashItems()
}

func (s *Service) History() []store.HistoryItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.HistoryItem, len(s.store.History))
	copy(out, s.store.History)
	return out
}

func (s *Service) Content(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == model.SystemNodeID {
		return s.readSysFn()
	}
	return s.store.Paths.ReadEntry(id)
}

func (s *Service) SystemHosts() (string, error) { return s.readSysFn() }

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// UpdateConfig mutates preferences, persists config.json, and rebuilds the
// HTTP client so proxy changes take effect immediately.
func (s *Service) UpdateConfig(fn func(*model.Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.store.Config)
	if err := s.store.SaveConfig(); err != nil {
		return err
	}
	if client, err := remote.NewClient(s.store.Config); err == nil {
		s.client = client
	} else {
		return err
	}
	s.emitKey("config", "Preferences saved")
	return nil
}

// EditNode applies an arbitrary edit on a cloned tree, persists manifest +
// state, and re-applies hosts. If anything fails the in-memory tree is rolled
// back to the pre-edit snapshot.
func (s *Service) EditNode(id string, fn func(*model.Node)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	backup := s.store.Nodes
	working := cloneNodes(backup)
	n := model.FindNode(working, id)
	if n == nil {
		return fmt.Errorf("node %q not found", id)
	}
	fn(n)
	s.store.Nodes = working
	if err := s.store.SaveManifest(); err != nil {
		s.store.Nodes = backup
		return err
	}
	s.emitKey("tree", "Saved %s", n.Title)
	return s.applyLocked()
}

// SetCollapsed changes only folder expansion state, which lives in
// internal/state.json. It deliberately does NOT re-apply the system hosts
// file: opening a folder in the UI must never trigger an elevation prompt.
func (s *Service) SetCollapsed(id string, collapsed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	backup := s.store.Nodes
	working := cloneNodes(backup)
	n := model.FindNode(working, id)
	if n == nil || n.Type != model.TypeFolder {
		return fmt.Errorf("folder %q not found", id)
	}
	n.Collapsed = collapsed
	s.store.Nodes = working
	if err := s.store.SaveManifest(); err != nil {
		s.store.Nodes = backup
		return err
	}
	s.emitKey("tree", "Folder state saved")
	return nil
}

// ToggleNode implements SwitchHosts folder choice-mode semantics before
// saving. It is separate from EditNode because SetOnStateOfItem must operate
// on the whole root forest, not just one node.
func (s *Service) ToggleNode(id string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	backup := s.store.Nodes
	working := cloneNodes(backup)
	model.SetOnStateOfItem(working, id, on,
		s.store.Config.ChoiceMode, s.store.Config.MultiChoseFolderSwitchAll)
	s.store.Nodes = working
	if err := s.store.SaveManifest(); err != nil {
		s.store.Nodes = backup
		return err
	}
	s.emitKey("tree", "Hosts selection updated")
	return s.applyLocked()
}

// SetContent writes an entry file. The system node is written straight to the
// OS hosts file (overwrite), matching how SwitchHosts exposes it.
func (s *Service) SetContent(id, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == model.SystemNodeID {
		if _, err := s.applyFn(content, "overwrite", s.store.Paths.BackupsDir); err != nil {
			return err
		}
		s.emitKey("content", "System hosts updated")
		return nil
	}
	if n := model.FindNode(s.store.Nodes, id); n == nil {
		return fmt.Errorf("node %q not found", id)
	}
	if err := s.store.Paths.WriteEntry(id, content); err != nil {
		return err
	}
	s.emitKey("content", "Content saved")
	return s.applyLocked()
}

// AddNode inserts a node at the root or into a folder and persists it.
func (s *Service) AddNode(n *model.Node, parentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	backup := s.store.Nodes
	working := cloneNodes(backup)
	if parentID == "" {
		working = append(working, n.Clone())
	} else {
		parent := model.FindNode(working, parentID)
		if parent == nil || parent.Type != model.TypeFolder {
			return fmt.Errorf("parent folder %q not found", parentID)
		}
		parent.Children = append(parent.Children, n.Clone())
	}
	s.store.Nodes = working
	if err := s.store.SaveManifest(); err != nil {
		s.store.Nodes = backup
		return err
	}
	s.emitKey("tree", "Added %s", n.Title)
	return s.applyLocked()
}

// DeleteNode detaches a node and puts it in trashcan.json.
func (s *Service) DeleteNode(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == model.SystemNodeID {
		return errors.New("the system hosts entry cannot be deleted")
	}
	backup := s.store.Nodes
	working := cloneNodes(backup)
	removed, parent, remaining := model.RemoveNode(working, id)
	if removed == nil {
		return fmt.Errorf("node %q not found", id)
	}
	parentID := ""
	if parent != nil {
		parentID = parent.ID
	}
	s.store.Nodes = remaining
	if err := s.store.AddTrash(removed, parentID); err != nil {
		s.store.Nodes = backup
		return err
	}
	if err := s.store.SaveManifest(); err != nil {
		s.store.Nodes = backup
		return err
	}
	s.emitKey("trash", "Moved %s to trash", removed.Title)
	return s.applyLocked()
}

// RestoreTrash puts a deleted node back under its former parent.
func (s *Service) RestoreTrash(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.store.TakeTrash(id)
	if !ok {
		return fmt.Errorf("trash item %q not found", id)
	}
	data, _ := item["data"].(map[string]any)
	node := model.NodeFromMap(data)
	parentID, _ := item["parent_id"].(string)
	backup := s.store.Nodes
	working := cloneNodes(backup)
	if parentID == "" {
		working = append(working, node)
	} else {
		parent := model.FindNode(working, parentID)
		if parent == nil {
			working = append(working, node)
		} else {
			parent.Children = append(parent.Children, node)
		}
	}
	s.store.Nodes = working
	if err := s.store.SaveTrashcan(); err != nil {
		s.store.Nodes = backup
		s.store.Trash = append(s.store.Trash, item)
		return err
	}
	if err := s.store.SaveManifest(); err != nil {
		s.store.Nodes = backup
		s.store.Trash = append(s.store.Trash, item)
		return err
	}
	s.emitKey("trash", "Restored %s", node.Title)
	return s.applyLocked()
}

// RestoreHistory writes a historical system-hosts snapshot back. This is a
// destructive operation on the OS hosts file; the UI must confirm first.
func (s *Service) RestoreHistory(content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.applyFn(content, "overwrite", s.store.Paths.BackupsDir); err != nil {
		return err
	}
	s.emitKey("status", "History snapshot restored")
	return nil
}

// ---------------------------------------------------------------------------
// Applying hosts
// ---------------------------------------------------------------------------

// Apply aggregates every selected entry and writes the system hosts file.
func (s *Service) Apply() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked()
}

// applyLocked must be called with s.mu held.
func (s *Service) applyLocked() error {
	content, err := hosts.AggregateSelected(
		s.store.Nodes, s.store.Paths.ReadEntry, s.store.Config.RemoveDuplicateRecords)
	if err != nil {
		return err
	}
	out, err := s.applyFn(content, s.store.Config.WriteMode, s.store.Paths.BackupsDir)
	if err != nil {
		s.emitKey("status", "Apply failed: %s", err.Error())
		return err
	}
	if out.Unchanged {
		return nil
	}
	if err := s.store.RecordChange(out.PreviousContent, out.NewContent); err != nil {
		return err
	}
	s.lastApply = &out
	if cmd := s.store.Config.CmdAfterHostsApply; cmd != "" {
		go func() { _ = hosts.RunCommandAfterApply(cmd) }()
	}
	s.emitKey("status", "Hosts applied")
	return nil
}

// Undo restores the exact pre-apply system hosts snapshot, but refuses if an
// external tool has written the file since our last apply.
func (s *Service) Undo() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastApply == nil {
		return errors.New("nothing to undo")
	}
	if err := s.restoreFn(s.lastApply.PreviousContent, s.lastApply.NewContent, s.lastApply.PreviousBytes); err != nil {
		return err
	}
	s.emitKey("status", "Restored previous system hosts")
	s.lastApply = nil
	return nil
}

// IsApplied reports whether the OS hosts file still contains exactly what we
// last wrote. The UI uses it to warn about external edits.
func (s *Service) IsApplied() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastApply == nil {
		return true
	}
	return s.matchesFn(s.lastApply.NewContent, nil)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func cloneNodes(nodes []*model.Node) []*model.Node {
	out := make([]*model.Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Clone())
	}
	return out
}

func sourceOf(n *model.Node) string {
	s, _ := n.Extras["source"].(string)
	return s
}

// NewID produces a SwitchHosts-compatible opaque node id.
func NewID() string {
	return fmt.Sprintf("sh_%d_%d", os.Getpid(), time.Now().UnixNano())
}
