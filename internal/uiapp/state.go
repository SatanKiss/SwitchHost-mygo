// Package uiapp is the MyGo native UI for SwitchHosts.
//
// Everything here is drawn by MyGo on the GPU. There is no HTML, no CSS, no
// JavaScript, no React/Vite build, no WindowOptions.URL/Page and no webview
// of any kind. All application state lives in Go structs, and every frame is
// built by (*App).View from those structs.
//
// Threading: the service layer is safe from any goroutine, but all fields of
// App are owned by the main thread. Background work reports back through
// Window.Update; the only cross-thread entry point is PumpEvents.
//
// Localisation: every visible string is passed through a.T / a.F. The English
// text is the translation key, so English is the built-in fallback and the
// Chinese catalogs can be verified mechanically. Status lines keep their key
// and arguments until they are drawn, so changing language in Preferences
// also re-translates the status bar immediately.
package uiapp

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	appsvc "github.com/switchhosts/go-native/internal/app"
	"github.com/switchhosts/go-native/internal/i18n"
	"github.com/switchhosts/go-native/internal/model"
	"github.com/switchhosts/go-native/internal/remote"
	"github.com/switchhosts/go-native/internal/store"
)

// statusLine keeps an untranslated key (and its fmt arguments) until the
// frame is built. A raw line is used for messages that are already final
// text, such as an error detail coming from the service. A batch refresh
// result is kept as data too: SummariseRefresh composes several translated
// fragments, so storing the finished English sentence would freeze the
// status bar in the language that happened to be active at refresh time.
type statusLine struct {
	key        string
	args       []any
	raw        string
	refresh    []appsvc.RefreshOutcome
	refreshSet bool
}

// App holds the whole visible state of the window.
type App struct {
	svc *appsvc.Service
	win *mygo.Window

	// Called when the user changes the appearance in Preferences. Wired by
	// main so headless view tests never touch the running desktop.
	onTheme func(string)
	// Called when the language changes, so main can rebuild the native
	// application menu and tray menu (MyGo snapshots them on SetMenu).
	onLocale func(i18n.Locale)

	tr *i18n.Translator

	tree     []*model.Node // snapshot of the service's tree
	cfg      model.Config
	selected string
	status   statusLine
	notice   string
	busy     int

	// appearance is "system", "light" or "dark". It is read from config.json
	// and can be applied a frame early so Preferences feels immediate.
	appearance string

	sidebarWidth float32
	treeSearch   string

	// One editor state per selected node; switching nodes loads a new buffer.
	editor    *editor.State
	editorID  string
	editorErr string

	// Overlays and their form state.
	showPrefs   bool
	showTrash   bool
	showHistory bool
	showAbout   bool
	nodeForm    *nodeForm
	prefs       prefsForm

	confirmOpen    bool
	confirmTitle   string
	confirmMessage string
	confirmOK      string
	confirmAction  func()

	quitting    bool
	pendingID   string
	dataDirNote recoveryNote
}

// recoveryNote is kept structured because it contains a user path and is
// shown in the About dialog: the sentence must be translated at draw time.
type recoveryNote struct {
	kind string // "missing" | "invalid"
	path string
}

// nodeForm backs the "New entry" / "Edit entry" modal.
type nodeForm struct {
	editing    bool
	id         string
	parentID   string
	title      string
	kind       int // 0 local, 1 remote, 2 folder, 3 group
	sourceKind int // 0 URL, 1 domains
	url        string
	domains    string
	interval   string
	folderMode string
	include    string
	err        string // an English translation key; translated when drawn
	errArg     any    // optional fmt argument for err
}

type prefsForm struct {
	writeMode         string
	theme             string
	choiceMode        string
	dnsProvider       string
	dnsCustomURL      string
	historyLimit      string
	proxyPort         string
	cmd               string
	locale            string // raw internal/config.json "locale" value
	removeDuplicates  bool
	historyEnabled    bool
	useProxy          bool
	refreshOnStartup  bool
	hideAtLaunch      bool
	quitOnClose       bool
	switchAllChildren bool
	err               string // an English translation key; translated when drawn
}

// New builds the UI state from an already-opened service. Headless callers
// (ui.NewTester, tests) get English by default; main calls SetLocale with the
// resolved config.json / OS locale once the desktop is ready.
func New(svc *appsvc.Service) *App {
	a := &App{
		svc:          svc,
		tr:           i18n.NewTranslator(i18n.En),
		status:       statusLine{key: "Ready"},
		sidebarWidth: 264,
	}
	a.reload()
	return a
}

// SetWindow attaches the real window. Until it is set the app runs headless,
// which is how ui.NewTester exercises the same view without a desktop.
func (a *App) SetWindow(w *mygo.Window) { a.win = w }

func (a *App) SetThemeHandler(fn func(string)) { a.onTheme = fn }

// SetLocaleHandler wires the callback that rebuilds native menus after a
// language change. It is optional: headless tests draw only the window.
func (a *App) SetLocaleHandler(fn func(i18n.Locale)) { a.onLocale = fn }

// Locale is the language currently used to draw the window.
func (a *App) Locale() i18n.Locale { return a.tr.Locale() }

// SetLocale switches the whole UI. Stored status/validation strings are keys,
// so they re-translate on the next frame; the editor's label and placeholder
// are the only cached text and are refreshed here.
func (a *App) SetLocale(l i18n.Locale) {
	if !i18n.IsSupported(l) || l == a.tr.Locale() {
		return
	}
	a.tr.SetLocale(l)
	a.refreshEditorLabels()
	if a.onLocale != nil {
		a.onLocale(l)
	}
	a.invalidate()
}

// SetAppearance applies the theme to the next frame without waiting for the
// service round-trip. main wires it as the theme handler.
func (a *App) SetAppearance(theme string) {
	a.appearance = theme
	a.invalidate()
}

// SetRecoveryNote records why the app fell back to the default data directory;
// the About dialog surfaces it so a user with a custom directory is not left
// wondering where their hosts went. It stores a key-like reason, not a
// finished English sentence, so the note follows the active language.
func (a *App) SetRecoveryNote(kind, path string) {
	a.dataDirNote = recoveryNote{kind: kind, path: path}
	a.invalidate()
}

func (a *App) recoveryText() string {
	switch a.dataDirNote.kind {
	case "missing":
		return a.F("Custom data directory is unavailable: %s", a.dataDirNote.path)
	case "invalid":
		return a.T("Data directory pointer is corrupt or unreadable.")
	default:
		return ""
	}
}

// View is the function handed to ui.View. It is exported so main.go can pass
// it directly to mygo.WindowOptions.Content.
func (a *App) View(c *ui.Context) { a.view(c) }

func (a *App) DataDir() string { return a.svc.DataDir() }

func (a *App) MarkQuitting() { a.quitting = true }

func (a *App) Quitting() bool { return a.quitting }

// T and F are the translation helpers used by every widget in this package.
func (a *App) T(key string) string { return a.tr.T(key) }

func (a *App) F(key string, args ...any) string { return a.tr.F(key, args...) }

func (a *App) statusText() string {
	if a.status.refreshSet {
		return appsvc.SummariseRefresh(a.status.refresh, a.F)
	}
	if a.status.raw != "" {
		return a.status.raw
	}
	if a.status.key == "" {
		return a.T("Ready")
	}
	return a.F(a.status.key, a.status.args...)
}

func (a *App) setStatusKey(key string, args ...any) {
	a.status = statusLine{key: key, args: args}
}

// setStatusRefresh stores a batch result rather than a rendered sentence, so
// a language change re-translates the whole summary on the next frame.
func (a *App) setStatusRefresh(results []appsvc.RefreshOutcome) {
	a.status = statusLine{refresh: results, refreshSet: true}
}

func (a *App) setStatusRaw(s string) { a.status = statusLine{raw: s} }

// Show brings the window back from the tray.
func (a *App) Show() {
	if a.win == nil {
		return
	}
	a.win.Show()
	a.win.Focus()
}

func (a *App) Hide() {
	if a.win != nil {
		a.win.Hide()
	}
}

func (a *App) ToggleWindow() {
	if a.win == nil {
		return
	}
	if a.win.IsVisible() {
		a.win.Hide()
	} else {
		a.Show()
	}
}

// ---------------------------------------------------------------------------
// State reload / cross-thread plumbing
// ---------------------------------------------------------------------------

func (a *App) reload() {
	a.tree = a.svc.Tree()
	a.cfg = a.svc.Config()
	a.appearance = a.cfg.Theme
	if a.selected == "" || model.FindNode(a.tree, a.selected) == nil {
		a.selected = ""
		if len(a.tree) > 0 {
			a.selected = a.tree[0].ID
		}
	}
	if a.editorID != a.selected {
		a.loadEditor(a.selected)
	}
}

// post runs fn on the main thread. In headless tests it runs inline.
func (a *App) post(fn func()) {
	if a.win != nil {
		a.win.Update(fn)
		return
	}
	fn()
}

func (a *App) invalidate() {
	if a.win != nil {
		a.win.Invalidate()
	}
}

// PumpEvents forwards non-blocking service notifications to the UI. The
// service never calls into UI code while holding its own lock. Events that
// carry a translation key are rendered in the active language; raw error
// details are shown as-is.
func (a *App) PumpEvents() {
	for ev := range a.svc.Events() {
		a.post(func() {
			if ev.Key != "" {
				a.setStatusKey(ev.Key, ev.Args...)
			} else {
				a.setStatusRaw(ev.Message)
			}
			a.reload()
		})
	}
}

// mutate runs a service mutation off the main thread, because several of them
// write the OS hosts file and may raise a UAC / polkit prompt. In headless
// tests it runs inline so assertions are deterministic.
func (a *App) mutate(label string, fn func() error) {
	a.mutateThen(label, fn, nil)
}

// mutateThen is mutate with a completion callback. done runs on the main
// thread *after* the mutation's result has been folded back into the view,
// which is essential for "mark the buffer saved" and "select the node that
// was just created": scheduling those separately could race the goroutine.
func (a *App) mutateThen(label string, fn func() error, done func()) {
	if a.win == nil {
		a.busy++
		err := fn()
		a.busy--
		a.finish(label, err)
		if done != nil {
			done()
		}
		return
	}
	a.busy++
	a.setStatusKey("%s…", a.T(label))
	go func() {
		err := fn()
		a.post(func() {
			a.busy--
			a.finish(label, err)
			if done != nil {
				done()
			}
		})
	}()
}

func (a *App) finish(label string, err error) {
	if err != nil {
		a.fail(label, err)
	} else {
		a.setStatusKey("%s done", a.T(label))
	}
	a.reload()
}

func (a *App) fail(label string, err error) {
	msg := a.localizeError(err)
	a.setStatusKey("%s failed: %s", a.T(label), msg)
	a.notice = msg
}

// localizeError turns the structured, privacy-safe error categories emitted
// by internal/remote into localized text. Codes whose message carries useful
// variable detail (an HTTP status, a byte limit, a file path) keep the raw
// safe text instead of losing it.
func (a *App) localizeError(err error) string {
	if err == nil {
		return ""
	}
	var dnsErr *remote.DNSError
	if errors.As(err, &dnsErr) {
		switch dnsErr.Code {
		case "http_status":
			return a.F("DNS service returned HTTP %d.", dnsErr.Status)
		case "bad_status":
			return a.F("DNS server returned status %d. The DNS service can be changed in Preferences.", dnsErr.Status)
		default:
			if key, ok := dnsErrorKeys[dnsErr.Code]; ok {
				return a.T(key)
			}
		}
	}
	var re *remote.RefreshError
	if errors.As(err, &re) {
		if key, ok := refreshErrorKeys[re.Code]; ok {
			return a.T(key)
		}
	}
	return safeMessage(err)
}

var dnsErrorKeys = map[string]string{
	"timeout":          "DNS query timed out. Please retry.",
	"network":          "DNS network request failed. The DNS service can be changed in Preferences.",
	"parse":            "DNS service returned an invalid response.",
	"no_a_record":      "Domain has no IPv4 (A) records.",
	"invalid_provider": "Unknown DNS provider. Fix it in Preferences.",
	"invalid_template": "Custom DoH template must be an HTTP(S) URL containing the {domain} placeholder.",
	"unknown":          "DNS query failed.",
}

var refreshErrorKeys = map[string]string{
	"invalid_id":     "node not found",
	"not_remote":     "node is not a remote hosts",
	"no_url":         "remote node has no URL",
	"invalid_url":    "Unknown remote source type.",
	"source_changed": "Remote source changed during refresh; its old result was discarded.",
	"storage_failed": "Could not save the refreshed hosts content.",
	// fetch_failed is deliberately absent: its message carries the HTTP
	// status, size limit or local path that the user needs to act on.
}

func safeMessage(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ReplaceAll(err.Error(), "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}

// refreshEditorLabels updates the two cached strings inside the native code
// editor after a language change, without touching the buffer or undo stack.
func (a *App) refreshEditorLabels() {
	if a.editor == nil {
		return
	}
	a.editor.Label = a.F("Hosts content of %s", a.editorID)
	a.editor.Placeholder = a.T("# Add hosts records here, e.g.\n# 127.0.0.1 example.test\n")
}

// loadEditor builds a fresh native code-editor buffer for id.
func (a *App) loadEditor(id string) {
	a.editorID = id
	a.editor = nil
	a.editorErr = ""
	if id == "" {
		return
	}
	text, err := a.svc.Content(id)
	if err != nil {
		a.editorErr = safeMessage(err)
		text = ""
	}
	buf := editor.NewBuffer(store.NormalizeToLF(text))
	buf.SetLanguage(HostsLanguage())
	buf.MarkSaved()
	a.editor = &editor.State{
		Buffer:      buf,
		LineNumbers: editor.LineNumbersOn,
	}
	a.refreshEditorLabels()
	n := model.FindNode(a.tree, id)
	if n != nil && (n.Type == model.TypeFolder || n.Type == model.TypeGroup) {
		a.editor.ReadOnly = true
	}
}

func (a *App) current() *model.Node { return model.FindNode(a.tree, a.selected) }

func (a *App) selectNode(id string) {
	if id == a.selected {
		return
	}
	if a.editor != nil && a.editor.Buffer.Modified() {
		a.pendingID = id
		a.askConfirm(a.T("Discard unsaved changes?"),
			a.T("The current entry has edits that have not been saved. Switching entries will discard them."),
			a.T("Discard"), func() {
				a.selected = a.pendingID
				a.loadEditor(a.selected)
			})
		return
	}
	a.selected = id
	a.loadEditor(id)
	a.invalidate()
}

// ---------------------------------------------------------------------------
// Actions (also used by the native application/tray menus)
// ---------------------------------------------------------------------------

func (a *App) Save() {
	if a.editor == nil || a.editorID == "" {
		return
	}
	id := a.editorID
	content := a.editor.Buffer.String()
	if id == model.SystemNodeID {
		a.askConfirm(a.T("Write the system hosts file?"),
			a.T("Saving this entry writes directly to the operating system's hosts file."),
			a.T("Write"), func() { a.commitContent(id, content) })
		return
	}
	a.commitContent(id, content)
}

func (a *App) commitContent(id, content string) {
	a.mutateThen("Saving entry", func() error {
		return a.svc.SetContent(id, content)
	}, func() {
		if a.editor != nil && a.editorID == id {
			a.editor.Buffer.MarkSaved()
		}
	})
}

func (a *App) Apply() {
	a.mutate("Applying hosts", func() error { return a.svc.Apply() })
}

func (a *App) Undo() {
	a.mutate("Undoing last apply", func() error { return a.svc.Undo() })
}

func (a *App) ToggleNode(id string, on bool) {
	a.mutate("Updating selection", func() error { return a.svc.ToggleNode(id, on) })
}

func (a *App) ToggleCollapse(n *model.Node) {
	if n == nil || n.Type != model.TypeFolder {
		return
	}
	id, next := n.ID, !n.Collapsed
	a.mutate("Saving folder state", func() error { return a.svc.SetCollapsed(id, next) })
}

func (a *App) RefreshOne(id string) {
	a.mutate("Refreshing remote entry", func() error {
		_, err := a.svc.RefreshOne(id)
		return err
	})
}

// RefreshSelected is used by the native app menu, which has no node id of
// its own. It is a no-op when the selection is not a remote entry.
func (a *App) RefreshSelected() {
	if n := a.current(); n != nil && n.Type == model.TypeRemote {
		a.RefreshOne(n.ID)
	}
}

func (a *App) RefreshAll() {
	if a.win == nil {
		a.setStatusRefresh(a.svc.RefreshAll())
		a.reload()
		return
	}
	a.busy++
	a.setStatusKey("Refreshing all remote entries…")
	go func() {
		results := a.svc.RefreshAll()
		var firstErr error
		for _, r := range results {
			if r.Err != nil && firstErr == nil {
				firstErr = r.Err
			}
		}
		msg := a.localizeError(firstErr)
		a.post(func() {
			a.busy--
			a.setStatusRefresh(results)
			if msg != "" {
				a.notice = msg
			}
			a.reload()
		})
	}()
}

func (a *App) DeleteNode(id string) {
	n := model.FindNode(a.tree, id)
	if n == nil || id == model.SystemNodeID {
		return
	}
	title := n.Title
	a.askConfirm(a.F("Delete “%s”?", title),
		a.T("The entry is moved to the trash and can be restored later."),
		a.T("Delete"), func() {
			a.mutate("Deleting entry", func() error { return a.svc.DeleteNode(id) })
		})
}

func (a *App) RestoreTrash(id string) {
	a.mutate("Restoring entry", func() error { return a.svc.RestoreTrash(id) })
}

func (a *App) RestoreHistory(content string) {
	a.askConfirm(a.T("Restore this snapshot?"),
		a.T("The operating system's hosts file is overwritten with the selected historical content."),
		a.T("Restore"), func() {
			a.mutate("Restoring snapshot", func() error { return a.svc.RestoreHistory(content) })
		})
}

func (a *App) OpenPreferences() {
	a.prefs = prefsFromConfig(a.cfg)
	a.showPrefs = true
	a.invalidate()
}

func (a *App) SavePreferences() {
	p := a.prefs
	limit, err := strconv.Atoi(strings.TrimSpace(p.historyLimit))
	if err != nil || limit < 0 || limit > 10000 {
		a.prefs.err = "History limit must be a number between 0 and 10000."
		return
	}
	port := 0
	if s := strings.TrimSpace(p.proxyPort); s != "" {
		port, err = strconv.Atoi(s)
		if err != nil || port < 0 || port > 65535 {
			a.prefs.err = "Proxy port must be a number between 0 and 65535."
			return
		}
	}
	if !remote.IsKnownProviderID(p.dnsProvider) {
		a.prefs.err = "Unknown DNS provider."
		return
	}
	choice, err := strconv.Atoi(p.choiceMode)
	if err != nil {
		choice = 2
	}
	locale := p.locale
	a.mutate("Saving preferences", func() error {
		return a.svc.UpdateConfig(func(cfg *model.Config) {
			cfg.WriteMode = p.writeMode
			cfg.Theme = p.theme
			cfg.ChoiceMode = choice
			cfg.DNSProvider = p.dnsProvider
			cfg.DNSCustomURL = p.dnsCustomURL
			cfg.HistoryLimit = limit
			cfg.ProxyPort = port
			cfg.CmdAfterHostsApply = p.cmd
			cfg.Locale = locale
			cfg.RemoveDuplicateRecords = p.removeDuplicates
			cfg.HistoryEnabled = p.historyEnabled
			cfg.UseProxy = p.useProxy
			cfg.RefreshRemoteHostsOnStartup = p.refreshOnStartup
			cfg.HideAtLaunch = p.hideAtLaunch
			cfg.QuitOnClose = p.quitOnClose
			cfg.MultiChoseFolderSwitchAll = p.switchAllChildren
		})
	})
	if a.onTheme != nil {
		a.onTheme(p.theme)
	}
	a.SetLocale(i18n.Resolve(locale))
	a.showPrefs = false
}

func (a *App) OpenTrash() {
	a.showTrash = true
	a.invalidate()
}

func (a *App) OpenHistory() {
	a.showHistory = true
	a.invalidate()
}

func (a *App) OpenAbout() {
	a.showAbout = true
	a.invalidate()
}

func (a *App) NewLocal() { a.openNodeForm(false, nil, "") }

func (a *App) NewFolder() {
	a.openNodeForm(false, nil, "")
	a.nodeForm.kind = 2
}

func (a *App) NewInFolder(parentID string) { a.openNodeForm(false, nil, parentID) }

func (a *App) EditSelected() {
	if n := a.current(); n != nil {
		a.openNodeForm(true, n, "")
	}
}

func (a *App) openNodeForm(editing bool, n *model.Node, parentID string) {
	f := &nodeForm{
		editing:    editing,
		parentID:   parentID,
		kind:       0,
		interval:   "0",
		folderMode: "0",
	}
	if n != nil {
		f.id = n.ID
		f.title = n.Title
		switch n.Type {
		case model.TypeLocal:
			f.kind = 0
		case model.TypeRemote:
			f.kind = 1
		case model.TypeFolder:
			f.kind = 2
		case model.TypeGroup:
			f.kind = 3
		}
		f.url = n.URL
		f.interval = strconv.Itoa(n.RefreshInterval)
		f.folderMode = strconv.Itoa(n.FolderMode)
		f.include = strings.Join(n.Include, "\n")
		if src, _ := n.Extras["source"].(string); src == "domain" {
			f.sourceKind = 1
		}
		if v, ok := n.Extras["domains"]; ok {
			f.domains = strings.Join(anyStrings(v), "\n")
		} else if f.sourceKind == 1 && n.URL != "" {
			f.domains = n.URL
		}
	} else if parentID != "" {
		f.kind = 0
	}
	a.nodeForm = f
	a.invalidate()
}

func anyStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// SaveNodeForm creates or updates one node from the modal's fields.
func (a *App) SaveNodeForm() {
	f := a.nodeForm
	if f == nil {
		return
	}
	f.err, f.errArg = "", nil
	title := strings.TrimSpace(f.title)
	if title == "" {
		f.err = "A title is required."
		return
	}
	interval, err := strconv.Atoi(strings.TrimSpace(f.interval))
	if err != nil || interval < 0 {
		f.err = "Refresh interval must be a non-negative number of seconds."
		return
	}
	folderMode, err := strconv.Atoi(f.folderMode)
	if err != nil {
		folderMode = 0
	}

	types := []string{model.TypeLocal, model.TypeRemote, model.TypeFolder, model.TypeGroup}
	kind := types[f.kind]
	node := &model.Node{
		ID:    appsvc.NewID(),
		Type:  kind,
		Title: title,
		On:    false,
	}
	switch kind {
	case model.TypeRemote:
		node.RefreshInterval = interval
		if f.sourceKind == 1 {
			domains := splitList(f.domains)
			if len(domains) == 0 {
				f.err = "Enter at least one domain."
				return
			}
			if len(domains) > remote.MaxDomains {
				f.err, f.errArg = "A domain list can contain at most %d distinct domains.", remote.MaxDomains
				return
			}
			for _, d := range domains {
				if !remote.IsValidDomain(d) {
					f.err, f.errArg = "Invalid domain: %s", d
					return
				}
			}
			node.Extras = map[string]any{
				"source":  "domain",
				"domains": domains,
			}
		} else {
			u := strings.TrimSpace(f.url)
			if u == "" {
				f.err = "A URL is required for a URL remote entry."
				return
			}
			node.URL = u
			node.Extras = map[string]any{"source": "url"}
		}
	case model.TypeFolder:
		node.FolderMode = folderMode
		node.Children = []*model.Node{}
	case model.TypeGroup:
		node.Include = splitList(f.include)
	}

	parentID := f.parentID
	if f.editing {
		id := f.id
		patch := node
		a.mutate("Saving entry", func() error {
			return a.svc.EditNode(id, func(n *model.Node) {
				n.Type = patch.Type
				n.Title = patch.Title
				n.URL = patch.URL
				n.RefreshInterval = patch.RefreshInterval
				n.FolderMode = patch.FolderMode
				n.Include = patch.Include
				// Always drop stale remote source fields first. Switching a
				// remote node to local/folder/group, or URL <-> domains,
				// must not leave a discriminator that remote.TargetOf would
				// otherwise read if the type is ever switched back.
				if n.Extras == nil {
					n.Extras = map[string]any{}
				}
				delete(n.Extras, "source")
				delete(n.Extras, "domains")
				for k, v := range patch.Extras {
					n.Extras[k] = v
				}
			})
		})
	} else {
		a.mutateThen("Adding entry", func() error { return a.svc.AddNode(node, parentID) },
			func() {
				a.selected = node.ID
				a.loadEditor(node.ID)
			})
	}
	a.nodeForm = nil
}

func splitList(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == ',' || r == ';' || r == ' '
	}) {
		v := strings.TrimSpace(part)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func (a *App) askConfirm(title, message, okLabel string, fn func()) {
	a.confirmTitle = title
	a.confirmMessage = message
	a.confirmOK = okLabel
	a.confirmAction = fn
	a.confirmOpen = true
	a.invalidate()
}

func (a *App) runConfirm() {
	fn := a.confirmAction
	a.confirmAction = nil
	a.confirmOpen = false
	if fn != nil {
		fn()
	}
}

func prefsFromConfig(cfg model.Config) prefsForm {
	writeMode := cfg.WriteMode
	if writeMode == "" {
		writeMode = "append"
	}
	return prefsForm{
		writeMode:         writeMode,
		theme:             cfg.Theme,
		choiceMode:        strconv.Itoa(cfg.ChoiceMode),
		dnsProvider:       cfg.DNSProvider,
		dnsCustomURL:      cfg.DNSCustomURL,
		historyLimit:      strconv.Itoa(cfg.HistoryLimit),
		proxyPort:         strconv.Itoa(cfg.ProxyPort),
		cmd:               cfg.CmdAfterHostsApply,
		locale:            cfg.Locale,
		removeDuplicates:  cfg.RemoveDuplicateRecords,
		historyEnabled:    cfg.HistoryEnabled,
		useProxy:          cfg.UseProxy,
		refreshOnStartup:  cfg.RefreshRemoteHostsOnStartup,
		hideAtLaunch:      cfg.HideAtLaunch,
		quitOnClose:       cfg.QuitOnClose,
		switchAllChildren: cfg.MultiChoseFolderSwitchAll,
	}
}

func (a *App) providerOptions() ([]string, []string) {
	var ids, labels []string
	for _, p := range remote.BuiltinProviders() {
		ids = append(ids, p.ID)
		labels = append(labels, p.Label)
	}
	ids = append(ids, "custom")
	labels = append(labels, a.T("Custom DoH endpoint"))
	return ids, labels
}

// a.formatTime renders an epoch-millisecond timestamp for the history panel.
func (a *App) formatTime(ms int64) string {
	if ms <= 0 {
		return a.T("unknown time")
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}

// ---------------------------------------------------------------------------
// Language picker helpers
// ---------------------------------------------------------------------------

// localeOptions returns the raw config values and their localized labels.
// The first entry is "follow the operating system", whose stored value is
// empty so config.json stays compatible with the Tauri build.
func (a *App) localeOptions() ([]string, []string) {
	values := []string{"", string(i18n.En), string(i18n.Zh), string(i18n.ZhHant)}
	labels := []string{
		a.T("Follow system language"),
		i18n.En.Name(), i18n.Zh.Name(), i18n.ZhHant.Name(),
	}
	return values, labels
}

func localeValueFor(label string, values, labels []string) string {
	for i, v := range labels {
		if v == label && i < len(values) {
			return values[i]
		}
	}
	return ""
}

// localeLabelFor must also understand locales written by the Tauri build:
// config.json may contain "zh-CN", "zh_TW", "cn" or "en-US", none of which
// is one of our four stored values, but all of which must show the right
// language in the picker instead of falling back to "follow the system".
func localeLabelFor(value string, values, labels []string) string {
	for i, v := range values {
		if v == value && i < len(labels) {
			return labels[i]
		}
	}
	if l := i18n.Normalize(value); i18n.IsSupported(l) {
		for i, v := range values {
			if v == string(l) && i < len(labels) {
				return labels[i]
			}
		}
	}
	if i18n.System(value) && len(labels) > 0 {
		return labels[0]
	}
	// An explicitly configured but unsupported language (French, Japanese…)
	// resolves to English; show that rather than a misleading "follow system".
	for i, v := range values {
		if v == string(i18n.En) && i < len(labels) {
			return labels[i]
		}
	}
	return ""
}
