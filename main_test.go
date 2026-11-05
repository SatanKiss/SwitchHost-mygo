package main

import (
	"testing"

	"github.com/egoist/mygo"
	appsvc "github.com/switchhosts/go-native/internal/app"
	"github.com/switchhosts/go-native/internal/i18n"
	"github.com/switchhosts/go-native/internal/uiapp"
)

func menuLabels(items []*mygo.MenuItem) []string {
	var out []string
	for _, it := range items {
		if it == nil || it.Hidden {
			continue
		}
		if it.Label != "" {
			out = append(out, it.Label)
		}
		out = append(out, menuLabels(it.Submenu)...)
	}
	return out
}

func hasLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

// The application menu and the tray menu are native MyGo menus, not a web
// page's DOM. MyGo snapshots them on SetMenu, so main.go rebuilds them with
// appMenu/trayMenu after a locale change; this test proves those builders
// actually read the current language.
func TestNativeMenusAreLocalized(t *testing.T) {
	svc, err := appsvc.OpenAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := uiapp.New(svc)

	for _, tc := range []struct {
		name string
		l    i18n.Locale
		want []string
		gone []string
	}{
		{
			name: "english",
			l:    i18n.En,
			want: []string{"File", "Hosts", "View", "Help", "Quit", "Preferences…", "Apply hosts", "Refresh all"},
		},
		{
			name: "simplified chinese",
			l:    i18n.Zh,
			want: []string{"文件", "视图", "帮助", "退出", "偏好设置…", "应用 Hosts", "全部刷新", "新建本地条目"},
			gone: []string{"File", "View", "Help", "Quit", "Preferences…", "Apply hosts", "New local entry"},
		},
		{
			name: "traditional chinese",
			l:    i18n.ZhHant,
			want: []string{"檔案", "檢視", "說明", "結束", "全部重新整理"},
			gone: []string{"文件", "视图", "偏好设置…"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.SetLocale(tc.l)
			var all []string
			for _, menu := range []*mygo.Menu{appMenu(a, func() {}), trayMenu(a, func() {})} {
				labels := menuLabels(menu.Items())
				all = append(all, labels...)
				// Every individual menu must be free of English labels.
				for _, gone := range tc.gone {
					if hasLabel(labels, gone) {
						t.Errorf("English label %q survived in %s menu: %q", gone, tc.name, labels)
					}
				}
			}
			// The union of both menus must contain the translated labels.
			for _, want := range tc.want {
				if !hasLabel(all, want) {
					t.Errorf("native menus are missing %q; labels: %q", want, all)
				}
			}
		})
	}
}

// The window title is a product name and must never be translated, while
// the tray tooltip stays stable across languages too.
func TestProductNamesStayUntranslated(t *testing.T) {
	svc, err := appsvc.OpenAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := uiapp.New(svc)
	for _, l := range []i18n.Locale{i18n.En, i18n.Zh, i18n.ZhHant} {
		a.SetLocale(l)
		if got := a.WindowOptions(false).Title; got != "SwitchHosts" {
			t.Errorf("window title changed with locale %q: %q", l, got)
		}
	}
}
