package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/switchhosts/go-native/internal/model"
	"github.com/switchhosts/go-native/internal/remote"
	"github.com/switchhosts/go-native/internal/store"
)

const scanInterval = 60 * time.Second

// RefreshOutcome is one node's refresh result. Status is the domain batch
// status ("complete" / "partial" / "failed") or empty for URL sources.
type RefreshOutcome struct {
	ID      string
	Changed bool
	Status  string
	Err     error
}

// ErrSourceChanged means the source or cached data changed while a request
// was in flight. Its old result was discarded without writing anything.
var ErrSourceChanged = errors.New("source_changed")

// RefreshOne refreshes a single remote node. Lock order is always
// per-node refresh lock -> service mutex, never the reverse, so a slow fetch
// never blocks unrelated nodes or the UI.
func (s *Service) RefreshOne(id string) (RefreshOutcome, error) {
	release := s.locks.Acquire(id)
	defer release()

	// Step 1: consistent snapshot under the lock, then release before I/O.
	s.mu.Lock()
	node := model.FindNode(s.store.Nodes, id)
	if node == nil {
		s.mu.Unlock()
		return RefreshOutcome{ID: id}, &remote.RefreshError{Code: "invalid_id", Message: "node not found"}
	}
	target, rerr := remote.TargetOf(node)
	if rerr != nil {
		s.mu.Unlock()
		return RefreshOutcome{ID: id}, rerr
	}
	if err := remote.CheckURLTarget(target); err != nil {
		s.mu.Unlock()
		return RefreshOutcome{ID: id}, err
	}
	snapshot := node.Clone()
	content, err := s.store.Paths.ReadEntry(id)
	cfg := s.store.Config
	client := s.client
	stateFile := s.store.Paths.StateFile
	s.mu.Unlock()
	if err != nil {
		return RefreshOutcome{ID: id}, &remote.RefreshError{Code: "storage_failed", Message: err.Error()}
	}

	// Step 2: fetch. Network I/O never holds the store lock.
	nowMs := time.Now().UnixMilli()
	stamp := remote.FormatTimestamp(nowMs)
	var cached map[string]remote.DomainResult
	if target.Kind == remote.TargetDomains {
		cached = remote.CachedDomainResults(
			snapshot.Extras["domain_results"], sourceOf(snapshot),
			target.Domains, content, snapshot.LastRefresh, snapshot.LastRefreshMs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	res, err := remote.Fetch(ctx, client, cfg, target, cached, stamp, nowMs)
	if err != nil {
		return RefreshOutcome{ID: id}, err
	}

	// Step 3: re-verify and commit under the same lock.
	s.mu.Lock()
	defer s.mu.Unlock()

	current := model.FindNode(s.store.Nodes, id)
	if current == nil {
		return RefreshOutcome{ID: id}, &remote.RefreshError{Code: "invalid_id", Message: "node not found"}
	}
	if !remote.SameTarget(current, target) {
		return RefreshOutcome{ID: id}, &remote.RefreshError{
			Code: "source_changed", Message: "Remote source changed during refresh; its old result was discarded."}
	}
	currentContent, err := s.store.Paths.ReadEntry(id)
	if err != nil {
		return RefreshOutcome{ID: id}, &remote.RefreshError{Code: "storage_failed", Message: err.Error()}
	}
	if currentContent != content || remote.CacheChanged(current, snapshot) {
		return RefreshOutcome{ID: id}, &remote.RefreshError{
			Code: "source_changed", Message: "Remote content or cache changed during refresh; its old result was discarded."}
	}

	newContent := store.NormalizeToLF(res.Content)
	changed := currentContent != newContent

	// Work on a clone so a failed save cannot leave stamped metadata behind.
	backupNodes := s.store.Nodes
	working := cloneNodes(backupNodes)
	stamped := model.FindNode(working, id)
	remote.Stamp(stamped, res, stamp, nowMs)

	// state.json is written before manifest.json by SaveManifest; snapshot it
	// so a failed manifest write can be fully compensated.
	previousState, stateReadErr := os.ReadFile(stateFile)
	if stateReadErr != nil && !os.IsNotExist(stateReadErr) {
		return RefreshOutcome{ID: id}, &remote.RefreshError{Code: "storage_failed", Message: stateReadErr.Error()}
	}

	rollback := func() {
		s.store.Nodes = backupNodes
		if changed {
			_ = s.store.Paths.WriteEntry(id, currentContent)
		}
		if previousState != nil {
			_ = store.AtomicWrite(stateFile, previousState)
		} else {
			_ = os.Remove(stateFile)
		}
	}

	if changed {
		if err := s.store.Paths.WriteEntry(id, newContent); err != nil {
			rollback()
			return RefreshOutcome{ID: id}, &remote.RefreshError{Code: "storage_failed", Message: err.Error()}
		}
	}
	s.store.Nodes = working
	if err := s.store.SaveManifest(); err != nil {
		rollback()
		return RefreshOutcome{ID: id}, &remote.RefreshError{Code: "storage_failed", Message: err.Error()}
	}

	// Status lines are emitted as translation keys, not English prose: the
	// native UI renders them in its active language. The keys are literals so
	// the i18n package can verify that every catalog covers them.
	if changed {
		switch res.Status {
		case "partial":
			s.emitKey("tree", "Some domains could not be resolved. Previous addresses were kept where available.")
		case "failed":
			s.emitKey("tree", "No domains could be resolved. Previous addresses were kept where available.")
		default:
			s.emitKey("tree", "Refreshed %s", stamped.Title)
		}
	} else {
		switch res.Status {
		case "partial":
			s.emitKey("tree", "Some domains could not be resolved. Previous addresses were kept where available. (no content change)")
		case "failed":
			s.emitKey("tree", "No domains could be resolved. Previous addresses were kept where available. (no content change)")
		default:
			s.emitKey("tree", "Refreshed %s (no content change)", stamped.Title)
		}
	}

	// A changed, enabled node must be re-applied to the OS hosts file.
	if changed && stamped.On {
		if err := s.applyLocked(); err != nil {
			return RefreshOutcome{ID: id, Changed: true, Status: res.Status}, err
		}
	}
	return RefreshOutcome{ID: id, Changed: changed, Status: res.Status}, nil
}

// RefreshAll refreshes every remote node sequentially and returns one result
// per node, so partial failures never hide successful updates.
func (s *Service) RefreshAll() []RefreshOutcome {
	ids := remote.RemoteIDs(s.Tree())
	out := make([]RefreshOutcome, 0, len(ids))
	for _, id := range ids {
		res, err := s.RefreshOne(id)
		res.Err = err
		out = append(out, res)
	}
	s.emitKey("tree", "Refreshed %d remote entries", len(out))
	return out
}

// ScanOnce refreshes remote nodes whose interval has elapsed.
func (s *Service) ScanOnce() []RefreshOutcome {
	ids := remote.DueIDs(s.Tree(), time.Now().UnixMilli())
	if len(ids) == 0 {
		return nil
	}
	out := make([]RefreshOutcome, 0, len(ids))
	for _, id := range ids {
		res, err := s.RefreshOne(id)
		res.Err = err
		out = append(out, res)
	}
	s.emitKey("tree", "Background refresh finished")
	return out
}

// StartScanner spawns the periodic scanner. The first tick is delayed so the
// UI's startup burst does not compete with a potentially blocking HTTP fan-out.
// The goroutine captures its own stop channel: StopScanner may then install a
// fresh one for a later restart without leaving the old goroutine waiting on
// the wrong channel.
func (s *Service) StartScanner() {
	s.mu.Lock()
	if s.scanStarted {
		s.mu.Unlock()
		return
	}
	select {
	case <-s.stopScan:
		s.stopScan = make(chan struct{})
	default:
	}
	stop := s.stopScan
	s.scanStarted = true
	refreshOnStartup := s.store.Config.RefreshRemoteHostsOnStartup
	s.mu.Unlock()

	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-stop:
			return
		}
		if refreshOnStartup {
			s.RefreshAll()
			timer.Reset(scanInterval)
			select {
			case <-timer.C:
			case <-stop:
				return
			}
		}
		ticker := time.NewTicker(scanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.ScanOnce()
			case <-stop:
				return
			}
		}
	}()
}

// StopScanner asks the background goroutine to exit on its next wake-up. It
// is safe to call more than once and safe to follow with another StartScanner.
func (s *Service) StopScanner() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stopScan:
		// already closed
	default:
		close(s.stopScan)
	}
	s.stopScan = make(chan struct{})
	s.scanStarted = false
}

// SummariseRefresh formats a batch result for a status line. It deliberately
// omits per-domain error details, which may contain imported private data.
//
// The optional translator has the i18n.F signature (format key + arguments).
// When it is supplied the summary is built from translation keys so the UI
// can show it in the active language; without one the default is plain
// English, which keeps this package usable standalone and in tests.
func SummariseRefresh(results []RefreshOutcome, translate ...func(string, ...any) string) string {
	tf := func(key string, args ...any) string { return fmt.Sprintf(key, args...) }
	if len(translate) > 0 && translate[0] != nil {
		tf = translate[0]
	}
	if len(results) == 0 {
		return tf("No remote entries to refresh")
	}
	changed, failed := 0, 0
	var codes []string
	for _, r := range results {
		if r.Changed {
			changed++
		}
		if r.Err != nil {
			failed++
			if e, ok := r.Err.(*remote.RefreshError); ok {
				codes = append(codes, e.Code)
			}
		}
	}
	msg := tf("Refreshed %d entries, %d changed", len(results), changed)
	if failed > 0 {
		msg += tf(", %d failed (%s)", failed, strings.Join(uniqueStrings(codes), ", "))
	}
	return msg
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
