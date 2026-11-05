package uiapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	appsvc "github.com/switchhosts/go-native/internal/app"
	"github.com/switchhosts/go-native/internal/hosts"
	"github.com/switchhosts/go-native/internal/i18n"
	"github.com/switchhosts/go-native/internal/model"
	"github.com/switchhosts/go-native/internal/remote"
)

// The Chinese UI must be produced by the very same native view function that
// main.go hands to WindowOptions.Content. ui.NewTester builds a frame without
// a desktop, a webview or a page, so these assertions also guard the pure-Go
// requirement.
func TestSimplifiedChineseBuildsNativeView(t *testing.T) {
	e := newTestEnv(t)
	e.app.SetLocale(i18n.Zh)
	if e.app.Locale() != i18n.Zh {
		t.Fatal("SetLocale did not switch to Simplified Chinese")
	}

	tt := ui.NewTester(e.app.View, 1280, 800)
	tt.Frame()
	for _, want := range []string{
		"SwitchHosts", // product name is never translated
		"原生版", "HOSTS 列表", "就绪", "全部刷新", "应用",
		"偏好设置", "回收站", "历史", "关于",
	} {
		if !tt.HasText(want) {
			t.Errorf("Chinese frame is missing %q; texts: %q", want, tt.Texts())
		}
	}
	// English chrome must be gone, not merely accompanied by a Chinese copy.
	for _, gone := range []string{"Native", "Ready", "Refresh all", "Preferences", "Trash", "History", "About", "Apply"} {
		for _, text := range tt.Texts() {
			if text == gone {
				t.Errorf("English label %q survived in the Chinese frame", gone)
				break
			}
		}
	}
}

func TestTraditionalChineseIsDistinct(t *testing.T) {
	e := newTestEnv(t)
	e.app.SetLocale(i18n.ZhHant)
	tt := ui.NewTester(e.app.View, 1280, 800)
	tt.Frame()
	for _, want := range []string{"就緒", "套用", "偏好設定", "全部重新整理", "回收桶", "歷程", "關於", "HOSTS 清單"} {
		if !tt.HasText(want) {
			t.Errorf("Traditional Chinese frame is missing %q; texts: %q", want, tt.Texts())
		}
	}
	for _, gone := range []string{"偏好设置", "全部刷新", "应用", "回收站"} {
		if tt.HasText(gone) {
			t.Errorf("Simplified Chinese %q leaked into the Traditional Chinese frame", gone)
		}
	}
}

// MyGo snapshots menus on SetMenu, so main.go must rebuild the app and tray
// menus on a language change. SetLocaleHandler is that signal.
func TestSetLocaleSignalsNativeMenuRebuildOnce(t *testing.T) {
	e := newTestEnv(t)
	var calls []i18n.Locale
	e.app.SetLocaleHandler(func(l i18n.Locale) { calls = append(calls, l) })

	e.app.SetLocale(i18n.Zh)
	e.app.SetLocale(i18n.Zh)           // already active: no redraw needed
	e.app.SetLocale(i18n.Locale("fr")) // unsupported: must be ignored
	if len(calls) != 1 || calls[0] != i18n.Zh {
		t.Fatalf("locale callbacks = %v, want exactly [zh]", calls)
	}
	if e.app.Locale() != i18n.Zh {
		t.Fatal("an unsupported locale must not change the language")
	}
}

// Status lines are keys + arguments, not baked English sentences: switching
// language must re-translate the status bar that is already on screen.
func TestStatusLineReTranslatesOnLocaleChange(t *testing.T) {
	e := newTestEnv(t)
	e.app.setStatusKey("Hosts applied")
	if got := e.app.statusText(); got != "Hosts applied" {
		t.Fatalf("English status = %q", got)
	}
	e.app.SetLocale(i18n.Zh)
	if got := e.app.statusText(); got != "Hosts 已应用" {
		t.Errorf("Simplified Chinese status = %q", got)
	}
	e.app.SetLocale(i18n.ZhHant)
	if got := e.app.statusText(); got == "Hosts applied" || got == "Hosts 已应用" {
		t.Errorf("Traditional Chinese status was not translated: %q", got)
	}

	// Format keys keep their arguments across a language switch.
	e.app.setStatusKey("%s failed: %s", e.app.T("Applying hosts"), "disk full")
	if got := e.app.statusText(); !strings.Contains(got, "disk full") {
		t.Errorf("formatted status lost its argument: %q", got)
	}
	e.app.SetLocale(i18n.Zh)
	if got := e.app.statusText(); !strings.Contains(got, "disk full") || !strings.Contains(got, "失败") {
		t.Errorf("Chinese formatted status = %q", got)
	}
}

// A batch refresh summary is composed from several translated fragments, so
// it must stay as structured data until it is drawn. A pre-rendered English
// sentence would remain English after the user switches language.
func TestRefreshSummaryReTranslatesOnLocaleChange(t *testing.T) {
	e := newTestEnv(t)
	e.app.setStatusRefresh(nil)
	if got := e.app.statusText(); got != "No remote entries to refresh" {
		t.Fatalf("empty English summary = %q", got)
	}
	e.app.SetLocale(i18n.Zh)
	if got := e.app.statusText(); got != "没有可刷新的远程条目" {
		t.Errorf("empty Chinese summary = %q", got)
	}

	results := []appsvc.RefreshOutcome{
		{ID: "a", Changed: true},
		{ID: "b", Err: &remote.RefreshError{Code: "network", Message: "network"}},
	}
	e.app.setStatusRefresh(results)
	zh := e.app.statusText()
	if !strings.Contains(zh, "已刷新 2 个条目") || !strings.Contains(zh, "1 个失败") {
		t.Errorf("Chinese batch summary = %q", zh)
	}
	e.app.SetLocale(i18n.En)
	if got := e.app.statusText(); !strings.Contains(got, "Refreshed 2 entries, 1 changed") {
		t.Errorf("switching back to English did not re-render the summary: %q", got)
	}
	// The service-level helper keeps its standalone English default.
	if got := appsvc.SummariseRefresh(results); !strings.Contains(got, "Refreshed 2 entries") {
		t.Errorf("SummariseRefresh without a translator = %q", got)
	}
}

// config.json written by the Tauri build may contain aliases such as zh-CN
// or zh_TW. The picker must display the correct language for them.
func TestLocaleAliasesShowCorrectPickerLabel(t *testing.T) {
	e := newTestEnv(t)
	values, labels := e.app.localeOptions()
	if len(values) != 4 || len(labels) != 4 {
		t.Fatalf("locale options = %v / %v", values, labels)
	}
	cases := []struct {
		raw  string
		want string
	}{
		{"", "Follow system language"},
		{"system", "Follow system language"},
		{"zh", "简体中文"},
		{"zh-CN", "简体中文"},
		{"cn", "简体中文"},
		{"zh_TW", "繁體中文"},
		{"zh-Hant", "繁體中文"},
		{"en-US", "English"},
		{"fr", "English"}, // unsupported but explicitly chosen -> English
	}
	e.app.SetLocale(i18n.En)
	_, labels = e.app.localeOptions()
	for _, tc := range cases {
		if got := localeLabelFor(tc.raw, values, labels); got != tc.want {
			t.Errorf("localeLabelFor(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	e.app.SetLocale(i18n.Zh)
	_, labels = e.app.localeOptions()
	if got := localeLabelFor("", values, labels); got != "跟随系统语言" {
		t.Errorf("Chinese follow-system label = %q", got)
	}
	if got := localeValueFor("简体中文", values, labels); got != "zh" {
		t.Errorf("picking 简体中文 should store %q, got %q", "zh", got)
	}
	if got := localeValueFor("繁體中文", values, labels); got != "zh_hant" {
		t.Errorf("picking 繁體中文 should store %q, got %q", "zh_hant", got)
	}
	if got := localeValueFor("跟随系统语言", values, labels); got != "" {
		t.Errorf("picking follow-system should store an empty value, got %q", got)
	}
}

// The language choice must live in the existing v5 "locale" field so the
// Tauri build and this build agree, and it must take effect in the same frame.
func TestPreferencesPersistLocaleToV5Config(t *testing.T) {
	e := newTestEnv(t)
	var rebuilt []i18n.Locale
	e.app.SetLocaleHandler(func(l i18n.Locale) { rebuilt = append(rebuilt, l) })

	e.app.OpenPreferences()
	e.app.prefs.locale = "zh"
	e.app.SavePreferences()
	if e.app.showPrefs {
		t.Fatal("successful save should close Preferences")
	}
	if got := e.svc.Config().Locale; got != "zh" {
		t.Fatalf("config Locale = %q, want zh", got)
	}
	if e.app.Locale() != i18n.Zh {
		t.Fatalf("UI locale did not change immediately, got %q", e.app.Locale())
	}
	if len(rebuilt) != 1 || rebuilt[0] != i18n.Zh {
		t.Fatalf("native menus were not rebuilt: %v", rebuilt)
	}

	raw, err := os.ReadFile(filepath.Join(e.root, "internal", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config.json is not v5 JSON: %v\n%s", err, raw)
	}
	if cfg["locale"] != "zh" {
		t.Fatalf("config.json has no v5 locale field: %s", raw)
	}
	// A fresh service must read the same value back: this is the exact file
	// the Tauri build opens.
	reopened, err := appsvc.OpenAt(e.root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Config().Locale; got != "zh" {
		t.Fatalf("reopened config Locale = %q, want zh", got)
	}
	if got := i18n.Resolve(reopened.Config().Locale); got != i18n.Zh {
		t.Fatalf("Resolve(zh) = %q", got)
	}

	// Traditional Chinese and "follow system" are stored the same way.
	e.app.OpenPreferences()
	e.app.prefs.locale = "zh_hant"
	e.app.SavePreferences()
	if e.app.Locale() != i18n.ZhHant || e.svc.Config().Locale != "zh_hant" {
		t.Fatal("zh_hant did not persist or apply")
	}
	e.app.OpenPreferences()
	e.app.prefs.locale = ""
	e.app.SavePreferences()
	if got := e.svc.Config().Locale; got != "" {
		t.Fatalf("follow-system should store an empty locale, got %q", got)
	}
	if !i18n.IsSupported(e.app.Locale()) {
		t.Fatal("follow-system resolved to an unsupported locale")
	}
}

// Validation messages are stored as English keys and translated only when
// the modal is drawn, so an error raised before switching language must show
// up in the new language.
func TestValidationMessagesAreLocalizedWhenDrawn(t *testing.T) {
	e := newTestEnv(t)
	e.app.NewLocal()
	e.app.nodeForm.title = ""
	e.app.SaveNodeForm()
	if e.app.nodeForm == nil || e.app.nodeForm.err != "A title is required." {
		t.Fatal("validation did not leave the form open with a key")
	}
	e.app.SetLocale(i18n.Zh)
	tt := ui.NewTester(e.app.View, 1280, 800)
	tt.Frame()
	if !tt.HasText("请填写标题。") {
		t.Fatalf("Chinese validation message did not render; texts: %q", tt.Texts())
	}
	if tt.HasText("A title is required.") {
		t.Fatal("English validation message leaked into the Chinese form")
	}
}

// The native code editor caches a label and a placeholder. They must be
// refreshed when the language changes, without touching the buffer.
func TestEditorLabelsFollowLocale(t *testing.T) {
	e := newTestEnv(t)
	if e.app.editor == nil {
		t.Fatal("no editor was created for the system hosts node")
	}
	before := e.app.editor.Buffer.String()
	if !strings.HasPrefix(e.app.editor.Placeholder, "# Add hosts records here") {
		t.Fatalf("English placeholder = %q", e.app.editor.Placeholder)
	}
	e.app.SetLocale(i18n.Zh)
	if !strings.Contains(e.app.editor.Placeholder, "在此添加 Hosts 记录") {
		t.Errorf("Chinese placeholder = %q", e.app.editor.Placeholder)
	}
	if !strings.HasSuffix(e.app.editor.Label, "的 Hosts 内容") {
		t.Errorf("Chinese editor label = %q", e.app.editor.Label)
	}
	if got := e.app.editor.Buffer.String(); got != before {
		t.Fatal("switching language changed the editor buffer")
	}
	if e.app.editor.Buffer.Modified() {
		t.Fatal("switching language marked the buffer dirty")
	}
}

// A fallback to the default data directory is explained in the About dialog.
// That note contains a user path, so it must stay structured and translate
// at draw time instead of being stored as a finished English sentence.
func TestRecoveryNoteIsLocalized(t *testing.T) {
	e := newTestEnv(t)
	e.app.SetRecoveryNote("missing", "D:/custom/SwitchHosts")
	e.app.OpenAbout()
	e.app.SetLocale(i18n.Zh)
	tt := ui.NewTester(e.app.View, 1280, 800)
	tt.Frame()
	if !tt.HasText("自定义数据目录不可用：D:/custom/SwitchHosts") {
		t.Fatalf("Chinese recovery note did not render; texts: %q", tt.Texts())
	}
	if tt.HasText("custom data directory is unavailable") {
		t.Fatal("English recovery note leaked into the Chinese About dialog")
	}
	e.app.SetLocale(i18n.ZhHant)
	if !strings.Contains(e.app.recoveryText(), "自訂資料目錄無法使用：D:/custom/SwitchHosts") {
		t.Errorf("Traditional Chinese recovery note = %q", e.app.recoveryText())
	}
	e.app.SetRecoveryNote("invalid", "")
	if got := e.app.recoveryText(); got != "資料目錄指標已損毀或無法讀取。" {
		t.Errorf("invalid-pointer note was not translated: %q", got)
	}
	e.app.SetRecoveryNote("", "")
	if got := e.app.recoveryText(); got != "" {
		t.Errorf("no recovery should render nothing, got %q", got)
	}
}

// Structured remote errors carry privacy-safe category codes. The UI maps
// them to localized sentences instead of showing raw transport strings.
func TestRemoteErrorsAreLocalized(t *testing.T) {
	e := newTestEnv(t)
	e.app.SetLocale(i18n.Zh)
	codes := []string{
		"timeout", "network", "parse", "no_a_record",
		"invalid_provider", "invalid_template", "unknown",
	}
	for _, code := range codes {
		err := &remote.DNSError{Code: code}
		english := err.Error()
		got := e.app.localizeError(err)
		if got == "" || got == english {
			t.Errorf("DNSError(%q) was not localized: %q", code, got)
		}
	}
	if got := e.app.localizeError(&remote.DNSError{Code: "timeout"}); got != "DNS 查询超时，请重试。" {
		t.Errorf("timeout translation = %q", got)
	}
	if got := e.app.localizeError(&remote.DNSError{Code: "http_status", Status: 503}); got != "DNS 服务返回 HTTP 503。" {
		t.Errorf("http_status translation lost its status: %q", got)
	}
	if got := e.app.localizeError(&remote.RefreshError{Code: "storage_failed", Message: "disk"}); got != "无法保存刷新后的 Hosts 内容。" {
		t.Errorf("storage_failed translation = %q", got)
	}
	// fetch_failed deliberately keeps its safe variable detail.
	if got := e.app.localizeError(&remote.RefreshError{Code: "fetch_failed", Message: "HTTP 404"}); got != "HTTP 404" {
		t.Errorf("fetch_failed should keep its safe detail, got %q", got)
	}
}

// Structured service events must carry a translation key, not just English
// text, or PumpEvents could never re-render them in Chinese.
func TestServiceEventsCarryTranslationKeys(t *testing.T) {
	e := newTestEnv(t)
	if err := e.svc.UpdateConfig(func(cfg *model.Config) { cfg.HistoryLimit = 42 }); err != nil {
		t.Fatal(err)
	}
	// An idempotent apply emits nothing, so seed a stale managed section to
	// force a genuine byte-changing write (the sandbox file, never the real
	// OS hosts file).
	stale := "# baseline\r\n127.0.0.1 localhost\r\n\r\n" +
		hosts.ContentStartMarker + "\r\n\r\n9.9.9.9 stale.test\r\n\r\n" +
		hosts.ContentEndMarker + "\r\n"
	if err := os.WriteFile(e.hostsFile, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Apply(); err != nil {
		t.Fatal(err)
	}

	seen := 0
	drain := true
	for drain {
		select {
		case ev := <-e.svc.Events():
			seen++
			if ev.Key == "" {
				t.Errorf("service event has no translation key: %+v", ev)
			}
			if ev.Message == "" {
				t.Errorf("service event lost its English Status() text: %+v", ev)
			}
			if got := e.app.F(ev.Key, ev.Args...); got == "" {
				t.Errorf("event key %q does not render", ev.Key)
			}
		default:
			drain = false
		}
	}
	if seen < 2 {
		t.Fatalf("expected config + apply events, got %d", seen)
	}
	e.app.SetLocale(i18n.Zh)
	if got := e.svc.Status(); strings.Contains(got, "Hosts 已应用") {
		t.Fatal("Service.Status() must stay English for non-UI callers")
	}
	// The Chinese UI renders the same event key through its own translator.
	if got := e.app.F("Hosts applied"); got != "Hosts 已应用" {
		t.Errorf("Chinese translation of the apply event = %q", got)
	}
}
