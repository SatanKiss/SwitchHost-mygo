package i18n

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Locale handling
// ---------------------------------------------------------------------------

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want Locale
	}{
		{"", ""},
		{"   ", ""},
		{"system", ""},
		{"auto", ""},
		{"zh", Zh},
		{"ZH", Zh},
		{"cn", Zh},
		{"zh-CN", Zh},
		{"zh_CN", Zh},
		{"zh_CN.UTF-8", Zh},
		{"zh_CN@euc", Zh},
		{"zh-Hans", Zh},
		{"zh_hant", ZhHant},
		{"zh-Hant", ZhHant},
		{"zh-TW", ZhHant},
		{"zh_HK", ZhHant},
		{"zh-MO", ZhHant},
		{"zh-Hant-TW", ZhHant},
		{"zh_cht", ZhHant},
		{"en", En},
		{"EN-us", En},
		{"en_GB.UTF-8", En},
		{"fr", ""},
		{"ja-JP", ""},
		{"de_DE", ""},
	}
	for _, tc := range cases {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSystem(t *testing.T) {
	for _, s := range []string{"", "  ", "system", "SYSTEM", "auto", "default", "os"} {
		if !System(s) {
			t.Errorf("System(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"zh", "en", "zh_hant", "fr"} {
		if System(s) {
			t.Errorf("System(%q) = true, want false", s)
		}
	}
}

func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Locale
	}{
		{"zh", Zh},
		{"zh-CN", Zh},
		{"zh_hant", ZhHant},
		{"zh-TW", ZhHant},
		{"en", En},
		{"en-US", En},
		// A deliberately chosen but unsupported language must not silently
		// become Chinese; it falls back to English.
		{"fr", En},
		{"ja", En},
	} {
		if got := Resolve(tc.in); got != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Empty / system values always resolve to a supported locale, whatever
	// language the CI machine happens to run in.
	for _, s := range []string{"", "system", "auto"} {
		if got := Resolve(s); !IsSupported(got) {
			t.Errorf("Resolve(%q) = %q, want a supported locale", s, got)
		}
	}
	if got := Resolve(""); got != Detect() {
		t.Errorf("Resolve(\"\") = %q, want Detect() = %q", got, Detect())
	}
}

func TestDetect(t *testing.T) {
	if !IsSupported(Detect()) {
		t.Fatalf("Detect() = %q, want a supported locale", Detect())
	}
	if !IsSupported(All()[0]) || len(All()) != 3 {
		t.Fatalf("All() = %v, want en/zh/zh_hant", All())
	}
}

func TestLocaleValues(t *testing.T) {
	if En.String() != "en" || Zh.String() != "zh" || ZhHant.String() != "zh_hant" {
		t.Fatal("Locale.String() must return the config.json value")
	}
	// Endonyms are never translated, so the picker stays usable in any language.
	if En.Name() != "English" || Zh.Name() != "简体中文" || ZhHant.Name() != "繁體中文" {
		t.Fatalf("unexpected endonyms: %q %q %q", En.Name(), Zh.Name(), ZhHant.Name())
	}
	if !IsSupported(Zh) || IsSupported(Locale("fr")) || IsSupported(Locale("")) {
		t.Fatal("IsSupported is wrong")
	}
}

// ---------------------------------------------------------------------------
// Translation
// ---------------------------------------------------------------------------

func TestTranslationAndFallback(t *testing.T) {
	const known = "Ready"
	const unknown = "a key that no catalog has"
	for _, l := range []Locale{Zh, ZhHant} {
		if got := T(l, known); got == known || got == "" {
			t.Errorf("T(%q, %q) = %q, want a translation", l, known, got)
		}
		if got := T(l, unknown); got != unknown {
			t.Errorf("T(%q, %q) = %q, want the English fallback", l, unknown, got)
		}
		if !Has(l, known) || Has(l, unknown) {
			t.Errorf("Has(%q) is inconsistent with T", l)
		}
	}
	if T(En, known) != known || !Has(En, unknown) {
		t.Fatal("English must be the implicit key-based catalog")
	}
	if Keys(En) != nil && len(Keys(En)) != 0 {
		t.Fatal("English has no explicit catalog")
	}
	// An unsupported locale behaves like English instead of panicking.
	if T(Locale("fr"), known) != known || F(Locale("fr"), "%d", 3) != "3" {
		t.Fatal("unsupported locale must fall back to English")
	}
}

func TestF(t *testing.T) {
	const key = "Saved %s"
	if got := F(En, key, "work"); got != "Saved work" {
		t.Errorf("F(en) = %q", got)
	}
	for _, l := range []Locale{Zh, ZhHant} {
		got := F(l, key, "work")
		if got == "Saved work" || got == "" || !strings.Contains(got, "work") {
			t.Errorf("F(%q, %q, \"work\") = %q, want a translated string still containing the argument", l, key, got)
		}
	}
}

func TestKeysSorted(t *testing.T) {
	keys := Keys(Zh)
	if len(keys) < 100 {
		t.Fatalf("zh catalog has only %d keys", len(keys))
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("Keys() must be sorted")
	}
	if len(Keys(ZhHant)) != len(keys) {
		t.Fatal("zh and zh_hant catalogs should cover the same keys")
	}
}

func TestTranslator(t *testing.T) {
	tr := NewTranslator(En)
	if tr.Locale() != En || tr.T("Ready") != "Ready" {
		t.Fatal("translator should start in English")
	}
	tr.SetLocale(Zh)
	if tr.Locale() != Zh || tr.T("Ready") == "Ready" {
		t.Fatal("SetLocale(Zh) did not take effect")
	}
	if tr.F("Saved %s", "x") == "Saved x" {
		t.Fatal("F did not translate")
	}
	tr.SetLocale(Locale("fr")) // must be ignored
	if tr.Locale() != Zh {
		t.Fatal("SetLocale accepted an unsupported locale")
	}
	if NewTranslator(Locale("fr")).Locale() != En {
		t.Fatal("NewTranslator must coerce unsupported locales to English")
	}
}

// ---------------------------------------------------------------------------
// Catalog integrity
//
// Every user-visible string in the app is an English key passed to T/F (or
// stored as a key and drawn later). These tests parse the Go sources so a new
// string cannot be added without a translation, and so a removed string cannot
// leave stale catalog entries behind.
// ---------------------------------------------------------------------------

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// sourceKeys extracts every translation key used by the application code.
func sourceKeys(t *testing.T) map[string]bool {
	t.Helper()
	root := moduleRoot(t)
	patterns := []string{
		filepath.Join(root, "main.go"),
		filepath.Join(root, "internal", "uiapp", "*.go"),
		filepath.Join(root, "internal", "app", "service.go"),
		filepath.Join(root, "internal", "app", "refresh.go"),
	}
	var files []string
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range matches {
			if !strings.HasSuffix(f, "_test.go") {
				files = append(files, f)
			}
		}
	}
	if len(files) < 6 {
		t.Fatalf("only found %d source files, is the module layout still the same?", len(files))
	}

	keys := map[string]bool{}
	for _, f := range files {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				name := ""
				switch fn := x.Fun.(type) {
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				case *ast.Ident:
					name = fn.Name
				}
				switch name {
				case "T", "F", "tf", "setStatusKey", "mutate", "mutateThen":
					// T/F/tf take the key as arg 0.
					if len(x.Args) > 0 {
						addStringLit(keys, x.Args[0])
					}
				case "emitKey":
					// emitKey(kind, key, args...)
					if len(x.Args) > 1 {
						addStringLit(keys, x.Args[1])
					}
				}
			case *ast.AssignStmt:
				// Validation messages are stored as English keys in .err
				// fields and translated only when the modal is drawn.
				for i, lhs := range x.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "err" || i >= len(x.Rhs) {
						continue
					}
					addStringLit(keys, x.Rhs[i])
				}
			case *ast.CompositeLit:
				// map[string]string tables whose values are translation keys
				// (structured remote error categories, etc.).
				mt, ok := x.Type.(*ast.MapType)
				if !ok {
					break
				}
				ki, kv := "", ""
				if id, ok := mt.Key.(*ast.Ident); ok {
					ki = id.Name
				}
				if id, ok := mt.Value.(*ast.Ident); ok {
					kv = id.Name
				}
				if ki != "string" || kv != "string" {
					break
				}
				for _, el := range x.Elts {
					if pair, ok := el.(*ast.KeyValueExpr); ok {
						addStringLit(keys, pair.Value)
					}
				}
			}
			return true
		})
	}
	return keys
}

func addStringLit(keys map[string]bool, e ast.Expr) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return
	}
	s, err := strconv.Unquote(lit.Value)
	if err == nil {
		keys[s] = true
	}
}

// extraKeys are keys that are constructed as struct literals or constants
// rather than passed directly to T/F, so the AST walker cannot see them.
// They are still real, user-visible strings and must be translated.
var extraKeys = []string{
	"Ready",
}

func allSourceKeys(t *testing.T) []string {
	t.Helper()
	keys := sourceKeys(t)
	for _, k := range extraKeys {
		keys[k] = true
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func TestCatalogsCoverEverySourceKey(t *testing.T) {
	keys := allSourceKeys(t)
	if len(keys) < 200 {
		t.Fatalf("only %d keys extracted; the AST walker may be broken", len(keys))
	}
	for _, l := range []Locale{Zh, ZhHant} {
		var missing []string
		for _, k := range keys {
			if !Has(l, k) {
				missing = append(missing, strconv.Quote(k))
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s catalog is missing %d translations:\n  %s",
				l, len(missing), strings.Join(missing, "\n  "))
		}
	}
}

func TestCatalogsHaveNoUnusedKeys(t *testing.T) {
	used := map[string]bool{}
	for _, k := range allSourceKeys(t) {
		used[k] = true
	}
	for _, l := range []Locale{Zh, ZhHant} {
		var stale []string
		for _, k := range Keys(l) {
			if !used[k] {
				stale = append(stale, strconv.Quote(k))
			}
		}
		if len(stale) > 0 {
			t.Errorf("%s catalog has %d keys no longer used by the UI:\n  %s",
				l, len(stale), strings.Join(stale, "\n  "))
		}
	}
}

// fmtVerb matches one Go fmt directive, including indexed verbs such as
// "%[2]s" that translations use to reorder arguments.
var fmtVerb = regexp.MustCompile(`%(?:\[[0-9]+\])?[-+#0 ]*(?:[0-9]+)?(?:\.[0-9]+)?([a-zA-Z%])`)

func verbs(s string) []string {
	m := fmtVerb.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(m))
	for _, v := range m {
		if v[1] != "%" { // "%%" is a literal percent, not an argument
			out = append(out, v[1])
		}
	}
	sort.Strings(out)
	return out
}

func TestTranslatedFormatKeysKeepFmtVerbs(t *testing.T) {
	for _, l := range []Locale{Zh, ZhHant} {
		for _, key := range Keys(l) {
			want := verbs(key)
			if len(want) == 0 {
				continue
			}
			got := verbs(catalogs[l][key])
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%s translation of %q has fmt verbs %v, want %v: %q",
					l, key, got, want, catalogs[l][key])
			}
		}
	}
}

func TestCatalogTranslationsAreNonEmptyAndDistinctFromEnglish(t *testing.T) {
	for _, l := range []Locale{Zh, ZhHant} {
		for key, value := range catalogs[l] {
			if strings.TrimSpace(value) == "" {
				t.Errorf("%s has an empty translation for %q", l, key)
			}
		}
	}
	// A Chinese catalog that just copies its English keys would make the
	// language picker a no-op. Require a meaningful number of real changes.
	for _, l := range []Locale{Zh, ZhHant} {
		changed := 0
		for key, value := range catalogs[l] {
			if value != key {
				changed++
			}
		}
		if changed < len(catalogs[l])*8/10 {
			t.Errorf("%s only translates %d/%d keys", l, changed, len(catalogs[l]))
		}
	}
}
