package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/switchhosts/go-native/internal/hosts"
	"github.com/switchhosts/go-native/internal/model"
	"github.com/switchhosts/go-native/internal/remote"
)

// fakeHosts routes every OS-level write to a temp file via hosts.ApplyAt, so
// no test ever touches the real system hosts file.
type fakeHosts struct {
	path string
}

func (f *fakeHosts) install(t *testing.T, s *Service) {
	t.Helper()
	s.applyFn = func(content, mode, backup string) (hosts.ApplyOutcome, error) {
		return hosts.ApplyAt(f.path, content, mode, backup)
	}
	s.restoreFn = func(previous, expected string, previousBytes []byte) error {
		return hosts.RestoreAt(f.path, previous, expected, previousBytes)
	}
	s.matchesFn = func(expected string, snapshot []byte) bool {
		b, err := os.ReadFile(f.path)
		if err != nil {
			return false
		}
		if snapshot != nil {
			return string(b) == string(snapshot)
		}
		return string(b) == hosts.RestoreLineEndings(hosts.NormalizeLineEndings(expected))
	}
	s.readSysFn = func() (string, error) {
		b, err := os.ReadFile(f.path)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		text, err := hosts.DecodeSystem(b)
		if err != nil {
			return "", err
		}
		return hosts.NormalizeLineEndings(text), nil
	}
}

func newService(t *testing.T) (*Service, *fakeHosts) {
	t.Helper()
	root := t.TempDir()
	s, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeHosts{path: filepath.Join(root, "fake-system-hosts")}
	if err := os.WriteFile(f.path, []byte("# original system hosts\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.install(t, s)
	return s, f
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOpenSeedsSystemNodeAndWritesV5Layout(t *testing.T) {
	s, _ := newService(t)
	tree := s.Tree()
	if len(tree) != 1 || tree[0].ID != model.SystemNodeID || !tree[0].IsSys {
		t.Fatalf("unexpected initial tree: %+v", tree)
	}
	p := s.Paths()
	// Like the Tauri build, missing files produce in-memory defaults and are
	// created lazily on the first mutation, never on read.
	for _, file := range []string{p.ManifestFile, p.ConfigFile, p.TrashcanFile} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Errorf("%s should not be written just by opening the app", file)
		}
	}
	if err := s.AddNode(&model.Node{ID: NewID(), Type: model.TypeLocal,
		Title: "First entry", Extras: map[string]any{}}, ""); err != nil {
		t.Fatal(err)
	}
	m := readJSONFile(t, p.ManifestFile)
	if m["format"] != model.ManifestFormat {
		t.Fatalf("manifest envelope missing: %+v", m)
	}
	state := readJSONFile(t, p.StateFile)
	if state["format"] != model.StateFormat {
		t.Fatalf("state envelope missing: %+v", state)
	}
	if !strings.Contains(string(mustRead(t, p.StateFile)), "collapsedNodeIds") {
		t.Fatal("state.json must carry collapsedNodeIds")
	}
	root, _ := m["root"].([]any)
	if len(root) != 2 {
		t.Fatalf("root = %+v", root)
	}
	sys, _ := root[0].(map[string]any)
	if sys["isSys"] != true || sys["contentFile"] != nil {
		t.Fatalf("system node shape = %+v", sys)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestToggleAggregatesSelectedContentIntoAppendSection(t *testing.T) {
	s, f := newService(t)
	id := NewID()
	n := &model.Node{ID: id, Type: model.TypeLocal, Title: "My Hosts", On: false,
		Extras: map[string]any{}}
	if err := s.AddNode(n, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetContent(id, "1.2.3.4 a.test"); err != nil {
		t.Fatal(err)
	}
	if err := s.ToggleNode(id, true); err != nil {
		t.Fatal(err)
	}
	got := string(mustRead(t, f.path))
	for _, want := range []string{
		"# original system hosts", hosts.ContentStartMarker,
		"1.2.3.4 a.test", hosts.ContentEndMarker,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("system hosts missing %q:\n%s", want, got)
		}
	}
	// The on-disk manifest must be in the v5 nested shape.
	manifest := readJSONFile(t, s.Paths().ManifestFile)
	root, _ := manifest["root"].([]any)
	if len(root) != 2 {
		t.Fatalf("root = %+v", root)
	}
	second, _ := root[1].(map[string]any)
	if second["contentFile"] != "entries/"+id+".hosts" {
		t.Fatalf("contentFile = %v", second["contentFile"])
	}
}

func TestSingleChoiceModeTurnsOffSiblings(t *testing.T) {
	s, _ := newService(t)
	if err := s.UpdateConfig(func(c *model.Config) { c.ChoiceMode = 1 }); err != nil {
		t.Fatal(err)
	}
	a, b := NewID()+"a", NewID()+"b"
	for _, id := range []string{a, b} {
		if err := s.AddNode(&model.Node{ID: id, Type: model.TypeLocal, Title: id,
			Extras: map[string]any{}}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ToggleNode(a, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ToggleNode(b, true); err != nil {
		t.Fatal(err)
	}
	tree := s.Tree()
	var on []string
	for _, n := range tree {
		if n.On && n.ID != model.SystemNodeID {
			on = append(on, n.ID)
		}
	}
	if len(on) != 1 || on[0] != b {
		t.Fatalf("single-choice mode left %v enabled", on)
	}
}

func TestDeleteAndRestoreTrash(t *testing.T) {
	s, _ := newService(t)
	id := NewID()
	if err := s.AddNode(&model.Node{ID: id, Type: model.TypeLocal, Title: "To delete",
		Extras: map[string]any{}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode(id); err != nil {
		t.Fatal(err)
	}
	if s.Find(id) != nil {
		t.Fatal("node still in tree")
	}
	trash := s.Trash()
	if len(trash) != 1 {
		t.Fatalf("trash = %+v", trash)
	}
	if err := s.RestoreTrash(id); err != nil {
		t.Fatal(err)
	}
	if s.Find(id) == nil {
		t.Fatal("node not restored")
	}
	raw := string(mustRead(t, s.Paths().TrashcanFile))
	if !strings.Contains(raw, model.TrashcanFormat) {
		t.Fatalf("trashcan envelope missing: %s", raw)
	}
}

func TestSetContentNormalizesLineEndings(t *testing.T) {
	s, _ := newService(t)
	id := NewID()
	if err := s.AddNode(&model.Node{ID: id, Type: model.TypeLocal, Title: "LF",
		Extras: map[string]any{}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetContent(id, "1.2.3.4 a.test\r\n2.3.4.5 b.test\r"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Content(id)
	if err != nil || strings.Contains(got, "\r") {
		t.Fatalf("content = %q, err=%v", got, err)
	}
}

func TestApplyRecordsHistoryAndUndoRestores(t *testing.T) {
	s, f := newService(t)
	id := NewID()
	if err := s.AddNode(&model.Node{ID: id, Type: model.TypeLocal, Title: "H",
		On: true, Extras: map[string]any{}}, ""); err != nil {
		t.Fatal(err)
	}
	before := string(mustRead(t, f.path))
	if err := s.SetContent(id, "9.9.9.9 changed.test"); err != nil {
		t.Fatal(err)
	}
	after := string(mustRead(t, f.path))
	if before == after {
		t.Fatal("apply did not change the hosts file")
	}
	if len(s.History()) == 0 {
		t.Fatal("history not recorded")
	}
	if err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRead(t, f.path)); got != before {
		t.Fatalf("undo did not restore:\n%q\nwant:\n%q", got, before)
	}
}

func TestConfigRoundTripPreservesUnknownKeys(t *testing.T) {
	s, _ := newService(t)
	// Simulate a newer SwitchHosts writing a key this build does not know.
	p := s.Paths()
	raw := map[string]any{
		"format": model.ConfigFormat, "schemaVersion": model.ConfigSchemaVersion,
		"theme": "dark", "choice_mode": float64(1), "future_pref": "keep-me",
	}
	b, _ := json.Marshal(raw)
	if err := os.WriteFile(p.ConfigFile, b, 0o644); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAt(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Config().Theme != "dark" || reopened.Config().ChoiceMode != 1 {
		t.Fatalf("config = %+v", reopened.Config())
	}
	if err := reopened.UpdateConfig(func(c *model.Config) { c.HistoryLimit = 7 }); err != nil {
		t.Fatal(err)
	}
	saved := readJSONFile(t, p.ConfigFile)
	if saved["future_pref"] != "keep-me" {
		t.Fatalf("unknown config key dropped: %+v", saved)
	}
	if saved["history_limit"] != float64(7) {
		t.Fatalf("history_limit not saved: %+v", saved)
	}
}

// --- refresh ---------------------------------------------------------------

func addRemote(t *testing.T, s *Service, url string, extra map[string]any, on bool) string {
	t.Helper()
	id := NewID()
	extras := map[string]any{"source": "url"}
	for k, v := range extra {
		extras[k] = v
	}
	n := &model.Node{ID: id, Type: model.TypeRemote, Title: "Remote", URL: url,
		On: on, Extras: extras}
	if err := s.AddNode(n, ""); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRefreshURLCommitsContentAndTimestamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("5.6.7.8 refreshed.test\r\n"))
	}))
	defer srv.Close()

	s, f := newService(t)
	_ = f
	id := addRemote(t, s, srv.URL+"/hosts", nil, true)
	if err := s.store.Paths.WriteEntry(id, "old content"); err != nil {
		t.Fatal(err)
	}
	out, err := s.RefreshOne(id)
	if err != nil || !out.Changed {
		t.Fatalf("refresh: %+v %v", out, err)
	}
	got, _ := s.Content(id)
	if got != "5.6.7.8 refreshed.test\n" {
		t.Fatalf("entry = %q", got)
	}
	if n := s.Find(id); n.LastRefresh == "" || n.LastRefreshMs == 0 {
		t.Fatalf("timestamps not stamped: %+v", n)
	}
	// A second identical refresh should not rewrite the entry.
	out2, err := s.RefreshOne(id)
	if err != nil || out2.Changed {
		t.Fatalf("second refresh should be unchanged: %+v %v", out2, err)
	}
}

func TestRefreshRejectsInvalidURLWithoutTouchingCachedData(t *testing.T) {
	s, _ := newService(t)
	id := addRemote(t, s, "github.com", nil, false)
	cached := "# Source: domain github.com\n1.2.3.4 github.com\n"
	if err := s.store.Paths.WriteEntry(id, cached); err != nil {
		t.Fatal(err)
	}
	before := string(mustRead(t, s.Paths().ManifestFile))
	_, err := s.RefreshOne(id)
	re, ok := err.(*remote.RefreshError)
	if !ok || re.Code != "invalid_url" || !strings.Contains(re.Message, "domain source type") {
		t.Fatalf("expected actionable invalid_url, got %v", err)
	}
	if got, _ := s.Content(id); got != cached {
		t.Fatal("cached content was modified")
	}
	if after := string(mustRead(t, s.Paths().ManifestFile)); after != before {
		t.Fatal("manifest was modified on a failed refresh")
	}
}

func TestRefreshRollsBackWhenManifestSaveFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("7.7.7.7 new.test\n"))
	}))
	defer srv.Close()

	s, _ := newService(t)
	id := addRemote(t, s, srv.URL+"/hosts", nil, false)
	original := "1.1.1.1 old.test\n"
	if err := s.store.Paths.WriteEntry(id, original); err != nil {
		t.Fatal(err)
	}
	beforeManifest := string(mustRead(t, s.Paths().ManifestFile))
	beforeState := string(mustRead(t, s.Paths().StateFile))

	// AtomicWrite uses <dest>.tmp; a directory there makes the rename fail.
	if err := os.Mkdir(s.Paths().ManifestFile+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := s.RefreshOne(id)
	re, ok := err.(*remote.RefreshError)
	if !ok || re.Code != "storage_failed" {
		t.Fatalf("expected storage_failed, got %v", err)
	}
	if got, _ := s.Content(id); got != original {
		t.Fatalf("entry not rolled back: %q", got)
	}
	if after := string(mustRead(t, s.Paths().ManifestFile)); after != beforeManifest {
		t.Fatal("manifest changed despite failed save")
	}
	if after := string(mustRead(t, s.Paths().StateFile)); after != beforeState {
		t.Fatalf("state.json not restored:\n%q\nwant:\n%q", after, beforeState)
	}
	if n := s.Find(id); n.LastRefreshMs != 0 {
		t.Fatalf("metadata stamped despite failed save: %+v", n)
	}
}

func TestRefreshDiscardsStaleResultWhenSourceChanges(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-release
		_, _ = w.Write([]byte("8.8.8.8 stale.test\n"))
	}))
	defer srv.Close()

	s, _ := newService(t)
	id := addRemote(t, s, srv.URL+"/hosts", nil, false)
	original := "1.1.1.1 keep.test\n"
	if err := s.store.Paths.WriteEntry(id, original); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.RefreshOne(id)
		done <- err
	}()
	<-started
	if err := s.EditNode(id, func(n *model.Node) {
		n.URL = srv.URL + "/changed"
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	err := <-done
	re, ok := err.(*remote.RefreshError)
	if !ok || re.Code != "source_changed" {
		t.Fatalf("expected source_changed, got %v", err)
	}
	if got, _ := s.Content(id); got != original {
		t.Fatalf("stale result overwrote content: %q", got)
	}
}

func TestRefreshDomainBatchBuildsHostsAndMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"Status":0,"Answer":[{"type":1,"data":"3.3.3.3"},{"type":1,"data":"4.4.4.4"}]}`)
	}))
	defer srv.Close()

	s, _ := newService(t)
	if err := s.UpdateConfig(func(c *model.Config) {
		c.DNSProvider = "custom"
		c.DNSCustomURL = srv.URL + "/resolve?name={domain}&type=A"
	}); err != nil {
		t.Fatal(err)
	}
	id := addRemote(t, s, "", map[string]any{
		"source": "domain", "domains": []any{"a.test"},
	}, false)
	out, err := s.RefreshOne(id)
	if err != nil || out.Status != "complete" {
		t.Fatalf("domain refresh: %+v %v", out, err)
	}
	content, _ := s.Content(id)
	for _, want := range []string{"# Source: domain a.test", "3.3.3.3 a.test", "# 4.4.4.4 a.test"} {
		if !strings.Contains(content, want) {
			t.Errorf("content missing %q:\n%s", want, content)
		}
	}
	n := s.Find(id)
	if n.Extras["domain_refresh_status"] != "complete" {
		t.Fatalf("status not persisted: %+v", n.Extras)
	}
	results, ok := n.Extras["domain_results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("domain_results = %+v", n.Extras["domain_results"])
	}
}

func TestConcurrentRefreshOfSameNodeIsSerialised(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("1.2.3.4 serial.test\n"))
	}))
	defer srv.Close()
	s, _ := newService(t)
	id := addRemote(t, s, srv.URL+"/hosts", nil, false)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.RefreshOne(id)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent refresh failed: %v", err)
		}
	}
}

func TestSummariseRefresh(t *testing.T) {
	got := SummariseRefresh([]RefreshOutcome{
		{ID: "a", Changed: true},
		{ID: "b", Err: &remote.RefreshError{Code: "fetch_failed"}},
	})
	if !strings.Contains(got, "1 changed") || !strings.Contains(got, "1 failed") {
		t.Fatalf("summary = %q", got)
	}
	if SummariseRefresh(nil) != "No remote entries to refresh" {
		t.Fatal("empty summary mismatch")
	}
}

// Stopping the scanner must not leave a permanently closed stop channel: a
// later StartScanner (for example after a preferences change) still has to
// run. The goroutine captures its own channel, so the old one can be closed
// without orphaning the new one.
func TestScannerStopIsIdempotentAndCanRestart(t *testing.T) {
	s, _ := newService(t)
	s.StartScanner()
	s.mu.Lock()
	first := s.stopScan
	started := s.scanStarted
	s.mu.Unlock()
	if !started || first == nil {
		t.Fatal("StartScanner did not start")
	}

	s.StopScanner()
	s.StopScanner() // must not panic on a second call
	s.mu.Lock()
	second := s.stopScan
	started = s.scanStarted
	s.mu.Unlock()
	if started {
		t.Fatal("StopScanner left scanStarted set")
	}
	select {
	case <-first:
	default:
		t.Fatal("the old stop channel was not closed")
	}
	select {
	case <-second:
		t.Fatal("the replacement stop channel is already closed")
	default:
	}

	s.StartScanner()
	s.mu.Lock()
	third := s.stopScan
	started = s.scanStarted
	s.mu.Unlock()
	if !started || third != second {
		t.Fatal("scanner did not restart on a fresh stop channel")
	}
	s.StopScanner()
}
