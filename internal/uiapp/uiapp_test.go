package uiapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	appsvc "github.com/switchhosts/go-native/internal/app"
	"github.com/switchhosts/go-native/internal/hosts"
	"github.com/switchhosts/go-native/internal/model"
)

// testEnv is a fully hermetic desktop: a temporary v5 data directory and a
// sandbox file standing in for the OS hosts file. No test in this file can
// write C:\Windows\System32\drivers\etc\hosts or /etc/hosts.
type testEnv struct {
	app       *App
	svc       *appsvc.Service
	root      string
	hostsFile string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	svc, err := appsvc.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	hostsFile := filepath.Join(root, "sandbox-hosts")
	if err := os.WriteFile(hostsFile, []byte("# baseline\r\n127.0.0.1 localhost\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.UseSandboxHostsFile(hostsFile)
	return &testEnv{app: New(svc), svc: svc, root: root, hostsFile: hostsFile}
}

func (e *testEnv) hostsText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.hostsFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A regression guard for the central requirement: the main window must show
// only MyGo's native Content. Setting Content is what makes MyGo mark the
// platform window as a Surface, so its Windows backend never starts a
// WebView2 environment and its Linux backend never loads WebKitGTK.
func TestWindowOptionsAreNativeContentOnly(t *testing.T) {
	e := newTestEnv(t)
	opts := e.app.WindowOptions(false)
	if opts.Content == nil {
		t.Fatal("native content is missing; a window without it would open a web page")
	}
	if _, ok := opts.Content.(*ui.Content); !ok {
		t.Fatalf("window content is %T, want *ui.Content", opts.Content)
	}
	if opts.URL != "" {
		t.Fatalf("window must not load a URL, got %q", opts.URL)
	}
	if !reflect.DeepEqual(opts.Page, mygo.PageOptions{}) {
		t.Fatalf("page options must stay at their zero value, got %+v", opts.Page)
	}
	if opts.Title != "SwitchHosts" || opts.StateKey != "main" || opts.Width <= 0 {
		t.Fatalf("unexpected window options: %+v", opts)
	}
	if !e.app.WindowOptions(true).Hidden {
		t.Fatal("HideAtLaunch was not forwarded to the window")
	}
}

// The entire window must build with no desktop, no webview and no page.
// ui.NewTester exercises the very same (*App).View handed to
// mygo.WindowOptions.Content.
func TestNativeViewBuildsHeadless(t *testing.T) {
	e := newTestEnv(t)
	tt := ui.NewTester(e.app.View, 1280, 800)
	tt.Frame()

	for _, want := range []string{
		"SwitchHosts", "Native", "HOSTS", "System Hosts", "Ready",
		"Refresh all", "Apply", "Preferences", "Trash", "History", "About",
	} {
		if !tt.HasText(want) {
			t.Errorf("frame is missing %q; texts: %q", want, tt.Texts())
			break
		}
	}

	// Explicit light/dark palettes are pure-Go themes, not CSS media queries.
	for _, theme := range []string{"dark", "light", "system"} {
		e.app.SetAppearance(theme)
		tt.Frame()
		if !tt.HasText("SwitchHosts") {
			t.Fatalf("frame failed to build with theme %q", theme)
		}
	}
}

func TestToolbarClickOpensNativeAboutDialog(t *testing.T) {
	e := newTestEnv(t)
	tt := ui.NewTester(e.app.View, 1280, 800)
	tt.Frame()
	if err := tt.Click("About"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("no browser kernel at all") {
		t.Fatalf("About dialog did not open; texts: %q", tt.Texts())
	}
	if !e.app.showAbout {
		t.Fatal("clicking About did not set showAbout")
	}
	// Cancel/Close is a native button too.
	if err := tt.Click("Close"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if e.app.showAbout {
		t.Fatal("Close did not dismiss the dialog")
	}
}

// AlertDialog returns -1 on a frame where no button was chosen: the confirm
// overlay must stay open then, and cancelling must never run the pending
// (possibly destructive) action.
func TestNativeConfirmCancelDropsPendingAction(t *testing.T) {
	e := newTestEnv(t)
	ran := 0
	e.app.askConfirm("Delete this entry?", "Only a test.", "Do it", func() { ran++ })

	tt := ui.NewTester(e.app.View, 1280, 800)
	tt.Frame()
	if !e.app.confirmOpen {
		t.Fatal("alert closed on a frame with no input")
	}
	if !tt.HasText("Delete this entry?") {
		t.Fatalf("confirm alert did not render; texts: %q", tt.Texts())
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ran != 0 || e.app.confirmOpen || e.app.confirmAction != nil {
		t.Fatalf("Cancel ran or kept the pending action: ran=%d open=%v actionNil=%v",
			ran, e.app.confirmOpen, e.app.confirmAction == nil)
	}

	// The OK button still runs the action exactly once.
	e.app.askConfirm("Delete this entry?", "Only a test.", "Do it", func() { ran++ })
	tt = ui.NewTester(e.app.View, 1280, 800)
	tt.Frame()
	if err := tt.Click("Do it"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ran != 1 || e.app.confirmOpen {
		t.Fatalf("confirming should run the action once: ran=%d open=%v", ran, e.app.confirmOpen)
	}
}

func TestNativeDialogsRenderWithoutWindow(t *testing.T) {
	e := newTestEnv(t)
	cases := []struct {
		name string
		open func()
		want string
		flag *bool
	}{
		{"preferences", e.app.OpenPreferences, "Write mode", &e.app.showPrefs},
		{"trash", e.app.OpenTrash, "trashcan.json", &e.app.showTrash},
		{"history", e.app.OpenHistory, "system-hosts.json", &e.app.showHistory},
		{"about", e.app.OpenAbout, "MyGo v0.7.4", &e.app.showAbout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Close whatever the previous case opened.
			e.app.showPrefs, e.app.showTrash = false, false
			e.app.showHistory, e.app.showAbout = false, false
			tc.open()
			tt := ui.NewTester(e.app.View, 1280, 800)
			tt.Frame()
			if !*tc.flag {
				t.Fatal("dialog flag was not set")
			}
			if !tt.HasText(tc.want) {
				t.Fatalf("dialog missing %q; texts: %q", tc.want, tt.Texts())
			}
		})
	}
}

func TestNodeFormValidationDoesNotMutate(t *testing.T) {
	e := newTestEnv(t)
	e.app.NewLocal()
	f := e.app.nodeForm
	if f == nil {
		t.Fatal("NewLocal did not open the form")
	}

	f.title = ""
	e.app.SaveNodeForm()
	if !strings.Contains(f.err, "title is required") {
		t.Fatalf("empty title should be rejected, got %q", f.err)
	}

	f.title, f.interval = "Dev", "-3"
	e.app.SaveNodeForm()
	if !strings.Contains(f.err, "Refresh interval") {
		t.Fatalf("negative interval should be rejected, got %q", f.err)
	}

	f.interval, f.kind, f.sourceKind = "0", 1, 1
	f.domains = "bad_domain!"
	e.app.SaveNodeForm()
	if !strings.Contains(f.err, "Invalid domain") {
		t.Fatalf("invalid domain should be rejected, got %q", f.err)
	}

	f.sourceKind, f.url = 0, ""
	e.app.SaveNodeForm()
	if !strings.Contains(f.err, "URL is required") {
		t.Fatalf("missing URL should be rejected, got %q", f.err)
	}

	if got := len(e.app.tree); got != 1 {
		t.Fatalf("validation failures changed the tree: %d nodes", got)
	}
	if _, err := os.Stat(filepath.Join(e.root, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("validation failures should not have written manifest.json")
	}
}

func TestCreateEntrySaveAndApplyStayInSandbox(t *testing.T) {
	e := newTestEnv(t)
	before := e.hostsText(t)

	e.app.NewLocal()
	f := e.app.nodeForm
	f.title, f.interval, f.kind = "Dev hosts", "0", 0
	e.app.SaveNodeForm()
	if e.app.nodeForm != nil {
		t.Fatal("successful save did not close the form")
	}
	if len(e.app.tree) != 2 {
		t.Fatalf("expected 2 nodes after create, got %d", len(e.app.tree))
	}
	var created *model.Node
	for _, n := range e.app.tree {
		if n.Title == "Dev hosts" {
			created = n
		}
	}
	if created == nil {
		t.Fatal("new node missing from the view's tree snapshot")
	}
	if e.app.selected != created.ID || e.app.editorID != created.ID {
		t.Fatalf("new node was not selected in the editor: selected=%q editor=%q",
			e.app.selected, e.app.editorID)
	}

	// The editor is a native ui/editor buffer, not a textarea in a webview.
	e.app.editor.Buffer = editor.NewBuffer("127.0.0.1 dev.test\n")
	e.app.Save()
	if e.app.editor.Buffer.Modified() {
		t.Fatal("buffer should be marked saved after a successful save")
	}

	entry, err := os.ReadFile(filepath.Join(e.root, "entries", created.ID+".hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), "127.0.0.1 dev.test") {
		t.Fatalf("entry file has unexpected content: %q", entry)
	}

	// The new node starts disabled, so applying must not copy its content into
	// the OS hosts file. Enabling it through the service must rewrite only the
	// sandbox target.
	e.app.ToggleNode(created.ID, true)
	after := e.hostsText(t)
	if !strings.Contains(after, "127.0.0.1 dev.test") {
		t.Fatalf("sandbox hosts file missing applied content: %q", after)
	}
	if strings.Contains(before, "127.0.0.1 dev.test") {
		t.Fatal("test fixture unexpectedly already contained the new record")
	}
	if !strings.Contains(after, "# --- SWITCHHOSTS_CONTENT_START ---") {
		t.Fatalf("append-mode markers missing: %q", after)
	}
}

func TestToggleCollapsePersistsStateWithoutApplyingHosts(t *testing.T) {
	e := newTestEnv(t)
	folder := &model.Node{
		ID: "folder-1", Type: model.TypeFolder, Title: "Work",
		Children: []*model.Node{}, Extras: map[string]any{},
	}
	if err := e.svc.AddNode(folder, ""); err != nil {
		t.Fatal(err)
	}
	e.app.reload()
	// Capture only after AddNode, which does re-apply by design.
	before := e.hostsText(t)

	e.app.ToggleCollapse(model.FindNode(e.app.tree, "folder-1"))
	if after := e.hostsText(t); after != before {
		t.Fatalf("collapsing a folder must not rewrite the hosts file:\n%s", after)
	}
	n := model.FindNode(e.app.tree, "folder-1")
	if n == nil || !n.Collapsed {
		t.Fatal("folder was not collapsed in the UI snapshot")
	}

	state, err := os.ReadFile(filepath.Join(e.root, "internal", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(state), "folder-1") {
		t.Fatalf("collapsedNodeIds missing from state.json: %s", state)
	}
}

func TestPreferencesValidationAndSave(t *testing.T) {
	e := newTestEnv(t)
	e.app.OpenPreferences()
	e.app.prefs.historyLimit = "not-a-number"
	e.app.SavePreferences()
	if e.app.prefs.err == "" {
		t.Fatal("invalid history limit should be rejected")
	}
	if !e.app.showPrefs {
		t.Fatal("dialog should stay open on a validation error")
	}

	e.app.prefs.historyLimit = "25"
	e.app.prefs.proxyPort = "70000"
	e.app.SavePreferences()
	if !strings.Contains(e.app.prefs.err, "Proxy port") {
		t.Fatalf("invalid port should be rejected, got %q", e.app.prefs.err)
	}

	e.app.prefs.proxyPort = "8080"
	e.app.prefs.theme = "dark"
	e.app.prefs.writeMode = "overwrite"
	e.app.SavePreferences()
	if e.app.showPrefs {
		t.Fatal("successful save should close the dialog")
	}
	cfg := e.svc.Config()
	if cfg.HistoryLimit != 25 || cfg.ProxyPort != 8080 ||
		cfg.Theme != "dark" || cfg.WriteMode != "overwrite" {
		t.Fatalf("config was not saved: %+v", cfg)
	}
	if e.app.appearance != "dark" {
		t.Fatalf("appearance was not applied immediately: %q", e.app.appearance)
	}
}

func TestTrashAndHistoryUseV5Data(t *testing.T) {
	e := newTestEnv(t)
	// A delete goes to trashcan.json; restoring puts the node back.
	if err := e.svc.AddNode(&model.Node{
		ID: "tmp-1", Type: model.TypeLocal, Title: "Temporary",
		Extras: map[string]any{},
	}, ""); err != nil {
		t.Fatal(err)
	}
	e.app.reload()
	e.app.DeleteNode("tmp-1")
	// DeleteNode asks for confirmation first; run it.
	if !e.app.confirmOpen {
		t.Fatal("DeleteNode should ask before deleting")
	}
	e.app.runConfirm()
	if len(e.app.svc.Trash()) != 1 {
		t.Fatalf("expected one trash item, got %d", len(e.app.svc.Trash()))
	}
	trash, err := os.ReadFile(filepath.Join(e.root, "trashcan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(trash), "tmp-1") {
		t.Fatalf("trashcan.json missing the deleted node: %s", trash)
	}

	tt := ui.NewTester(e.app.View, 1280, 800)
	e.app.OpenTrash()
	tt.Frame()
	if !tt.HasText("Temporary") {
		t.Fatalf("trash dialog did not list the node; texts: %q", tt.Texts())
	}

	// Apply records a history snapshot in the v5 bare-array file, but only
	// when the on-disk bytes actually change. With no enabled entry the
	// aggregated content is empty, so writing over the plain baseline would
	// be an idempotent no-op. Seed a stale managed section instead: this
	// apply must really remove it, which is both a genuine write and a
	// realistic SwitchHosts scenario.
	stale := "# baseline\r\n127.0.0.1 localhost\r\n\r\n" +
		hosts.ContentStartMarker + "\r\n\r\n9.9.9.9 stale.test\r\n\r\n" +
		hosts.ContentEndMarker + "\r\n"
	if err := os.WriteFile(e.hostsFile, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Apply(); err != nil {
		t.Fatal(err)
	}
	history := e.svc.History()
	if len(history) < 2 {
		t.Fatalf("Apply should record the pre- and post-apply snapshots, got %d items", len(history))
	}
	if after := e.hostsText(t); strings.Contains(after, "9.9.9.9 stale.test") {
		t.Fatalf("stale managed section survived the apply: %q", after)
	}
	foundStale := false
	for _, item := range history {
		if strings.Contains(item.Content, "9.9.9.9 stale.test") {
			foundStale = true
		}
	}
	if !foundStale {
		t.Fatal("history did not preserve the pre-apply snapshot")
	}

	// internal/histories/system-hosts.json is a bare JSON array, not an
	// enveloped object like manifest.json / trashcan.json.
	raw, err := os.ReadFile(filepath.Join(e.root, "internal", "histories", "system-hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		t.Fatalf("v5 history file is not a bare JSON array: %s", raw)
	}
	var onDisk []map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != len(history) {
		t.Fatalf("history on disk has %d items, service reports %d", len(onDisk), len(history))
	}

	e.app.OpenHistory()
	tt.Frame()
	if !tt.HasText("system-hosts.json") {
		t.Fatalf("history dialog did not render; texts: %q", tt.Texts())
	}
}

func TestTokenizeHostsLineCoversEveryByte(t *testing.T) {
	cases := []struct {
		line string
		want []editor.TokenKind
	}{
		{"   ", []editor.TokenKind{editor.TokenText}},
		{"  # a comment", []editor.TokenKind{editor.TokenComment}},
		{"; legacy comment", []editor.TokenKind{editor.TokenComment}},
		{"# --- SWITCHHOSTS_CONTENT_START ---", []editor.TokenKind{editor.TokenMeta}},
		{"127.0.0.1 example.test # trailing", []editor.TokenKind{
			editor.TokenConstant, editor.TokenKeyword, editor.TokenComment}},
		{"::1 localhost", []editor.TokenKind{editor.TokenConstant, editor.TokenKeyword}},
		{"not-an-ip foo", []editor.TokenKind{editor.TokenInvalid, editor.TokenKeyword}},
	}
	for _, tc := range cases {
		tokens, state := TokenizeHostsLine(tc.line, 0, nil)
		if state != 0 {
			t.Errorf("%q: hosts files are stateless, got state %d", tc.line, state)
		}
		if len(tokens) == 0 {
			t.Errorf("%q: no tokens", tc.line)
			continue
		}
		if last := tokens[len(tokens)-1]; last.End != len(tc.line) {
			t.Errorf("%q: tokens end at %d, line is %d bytes", tc.line, last.End, len(tc.line))
		}
		for i := 1; i < len(tokens); i++ {
			if tokens[i].End <= tokens[i-1].End {
				t.Errorf("%q: token ends are not increasing: %v", tc.line, tokens)
				break
			}
		}
		for _, kind := range tc.want {
			found := false
			for _, tk := range tokens {
				if tk.Kind == kind {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%q: missing %v in %v", tc.line, kind, tokens)
			}
		}
	}
}

func TestHostsLanguageIsPureGo(t *testing.T) {
	lang := HostsLanguage()
	if lang.Name != "Hosts" || lang.LineComment != "#" || lang.Tokenizer == nil {
		t.Fatalf("unexpected language: %+v", lang)
	}
	found := false
	for _, ext := range lang.Extensions {
		if ext == ".hosts" {
			found = true
		}
	}
	if !found {
		t.Fatal("Hosts language does not claim .hosts files")
	}
	buf := editor.NewBuffer("# a\n127.0.0.1 example.test\n")
	buf.SetLanguage(lang)
	if got := buf.Language(); got == nil || got.Name != "Hosts" {
		t.Fatal("buffer did not take the custom pure-Go language")
	}
}

// Ensure the v5 store shape is the one the UI is actually reading.
func TestDataLayoutIsV5(t *testing.T) {
	e := newTestEnv(t)
	p := e.svc.Paths()
	if filepath.Clean(p.Root) != filepath.Clean(e.root) {
		t.Fatalf("data root mismatch: %q vs %q", p.Root, e.root)
	}
	if filepath.Base(p.ManifestFile) != "manifest.json" ||
		filepath.Base(p.StateFile) != "state.json" ||
		filepath.Base(p.ConfigFile) != "config.json" ||
		filepath.Base(p.TrashcanFile) != "trashcan.json" ||
		filepath.Base(p.HistoryFile) != "system-hosts.json" {
		t.Fatalf("unexpected v5 paths: %+v", p)
	}
}
