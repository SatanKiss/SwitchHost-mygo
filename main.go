// Command switchhosts-native is a pure-Go, desktop build of SwitchHosts 5.
//
// The window shows a MyGo native UI: ui.View(app.View) is attached through
// WindowOptions.Content, so the window has no Page at all. There is no URL,
// no dev server, no bundled frontend, no generated TypeScript client and no
// webview of any kind (WebView2, Electron, WKWebView or WebKitGTK). Every
// widget, including the hosts code editor, is drawn by MyGo in Go.
//
// Data compatibility: this binary opens the same ~/.SwitchHosts v5 layout as
// the Tauri build, so the two can be swapped without exporting anything.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"runtime"

	"github.com/egoist/mygo"
	appsvc "github.com/switchhosts/go-native/internal/app"
	"github.com/switchhosts/go-native/internal/hosts"
	"github.com/switchhosts/go-native/internal/i18n"
	"github.com/switchhosts/go-native/internal/store"
	"github.com/switchhosts/go-native/internal/uiapp"
)

// appVersion is a var, not a const, so `mygo build` can inject the version
// from mygo.json with -X main.appVersion=... (MYGO_LDFLAGS in CI). The
// literal is the fallback for a plain `go build` / `go test`.
var appVersion = "5.0.3"

// trayRef keeps the tray alive for the whole process. MyGo holds the native
// handle, but keeping the Go wrapper reachable makes the intent explicit.
var trayRef *mygo.Tray

func main() {
	// UAC helper: this is a second, invisible instance of our own binary,
	// launched by ShellExecuteExW with the "runas" verb. It must return
	// before any GUI, tray or data-directory work happens. The staged file
	// path is the only argument; the child resolves the hosts path itself.
	if hosts.ElevationHelperArg != "" && len(os.Args) >= 3 &&
		os.Args[1] == hosts.ElevationHelperArg {
		if err := hosts.RunElevationHelper(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "elevated hosts write failed:", err)
			os.Exit(1)
		}
		return
	}

	dataDir := flag.String("data-dir", "", "use an explicit SwitchHosts v5 data directory")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("SwitchHosts Native %s (%s, %s/%s, no webview)\n",
			appVersion, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return
	}

	var (
		svc      *appsvc.Service
		recovery *store.Recovery
		err      error
	)
	if *dataDir != "" {
		svc, err = appsvc.OpenAt(*dataDir)
	} else {
		svc, recovery, err = appsvc.Open()
	}
	if err != nil {
		fatal("Cannot open the SwitchHosts data directory", err)
	}

	app := uiapp.New(svc)
	// internal/config.json already has a v5 "locale" field. An empty value
	// follows the OS language; "zh"/"zh_hant"/aliases select it explicitly.
	// Headless callers keep English by default.
	app.SetLocale(i18n.Resolve(svc.Config().Locale))
	if recovery != nil {
		// Pass the reason as structured data, not a finished English sentence:
		// the About dialog translates it in the active language and keeps the
		// custom path intact.
		app.SetRecoveryNote(recovery.Kind, recovery.Path)
	}
	// Applying an appearance choice in the same frame makes Preferences feel
	// immediate instead of waiting for the service round-trip.
	app.SetThemeHandler(app.SetAppearance)

	mygo.App.SetName("SwitchHosts")
	mygo.App.SetVersion(appVersion)

	requestQuit := func() {
		app.MarkQuitting() // let Window.OnClose allow the real close
		mygo.App.Quit()
	}

	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		svc.StopScanner()
		app.MarkQuitting()
	})

	mygo.App.WhenReady(func() {
		cfg := svc.Config()

		// The one and only window content is a UI MyGo draws itself;
		// URL and Page are deliberately absent (see App.WindowOptions).
		w := mygo.NewWindow(app.WindowOptions(cfg.HideAtLaunch))
		app.SetWindow(w)

		w.OnClose(func(e *mygo.CloseEvent) {
			if app.Quitting() || svc.Config().QuitOnClose {
				// Allow the close. App.lastWindowClosed then ends the loop
				// when the user chose "quit on close".
				return
			}
			// SwitchHosts is a tray app: the X button hides the window.
			e.PreventDefault()
			w.Hide()
		})

		// MyGo snapshots menus when SetMenu is called, so a language change
		// must rebuild both the application menu and the tray menu.
		app.SetLocaleHandler(func(i18n.Locale) {
			mygo.App.SetMenu(appMenu(app, requestQuit))
			if trayRef != nil {
				trayRef.SetMenu(trayMenu(app, requestQuit))
			}
		})
		mygo.App.SetMenu(appMenu(app, requestQuit))
		installTray(app, requestQuit)

		// Service notifications are drained off-thread and marshalled back
		// with Window.Update. StartScanner already performs a refresh on
		// startup when config.json asks for one, so no duplicate fan-out.
		go app.PumpEvents()
		svc.StartScanner()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	svc.StopScanner()
}

func fatal(title string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", title, err)
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Native application menu
// ---------------------------------------------------------------------------

func appMenu(a *uiapp.App, quit func()) *mygo.Menu {
	item := func(label, accel string, fn func()) *mygo.MenuItem {
		return &mygo.MenuItem{
			Label:       label,
			Accelerator: accel,
			Click:       func(*mygo.MenuItem, *mygo.Window) { fn() },
		}
	}

	file := []*mygo.MenuItem{
		item(a.T("New local entry"), "CmdOrCtrl+N", a.NewLocal),
		item(a.T("New folder"), "CmdOrCtrl+Shift+N", a.NewFolder),
		item(a.T("Edit selected entry"), "CmdOrCtrl+E", a.EditSelected),
		mygo.Separator(),
		item(a.T("Save entry"), "CmdOrCtrl+S", a.Save),
		item(a.T("Apply hosts"), "CmdOrCtrl+Return", a.Apply),
		item(a.T("Undo last apply"), "CmdOrCtrl+Z", a.Undo),
		mygo.Separator(),
		item(a.T("Preferences…"), "CmdOrCtrl+Comma", a.OpenPreferences),
	}
	if runtime.GOOS != "darwin" {
		file = append(file, mygo.Separator(), item(a.T("Quit"), "CmdOrCtrl+Q", quit))
	}

	hostsItems := []*mygo.MenuItem{
		item(a.T("Refresh selected"), "CmdOrCtrl+R", a.RefreshSelected),
		item(a.T("Refresh all"), "CmdOrCtrl+Shift+R", a.RefreshAll),
		mygo.Separator(),
		item(a.T("Trash…"), "CmdOrCtrl+Shift+T", a.OpenTrash),
		item(a.T("System hosts history…"), "CmdOrCtrl+Shift+H", a.OpenHistory),
	}

	viewItems := []*mygo.MenuItem{
		item(a.T("Show / hide window"), "CmdOrCtrl+W", a.ToggleWindow),
		item(a.T("Bring window to front"), "", a.Show),
		item(a.T("Open data folder"), "", func() {
			_ = mygo.Shell.OpenPath(a.DataDir())
		}),
	}

	menus := []*mygo.MenuItem{
		{Label: a.T("File"), Submenu: file},
		{Label: a.T("Hosts"), Submenu: hostsItems},
		{Label: a.T("View"), Submenu: viewItems},
		{Label: a.T("Help"), Submenu: []*mygo.MenuItem{
			item(a.T("About SwitchHosts Native"), "", a.OpenAbout),
		}},
	}
	if runtime.GOOS == "darwin" {
		// macOS gets the standard application menu (About / Hide / Quit).
		menus = append([]*mygo.MenuItem{{Role: mygo.RoleAppMenu}}, menus...)
	}
	return mygo.NewMenu(menus)
}

// ---------------------------------------------------------------------------
// Native tray
// ---------------------------------------------------------------------------

func trayMenu(a *uiapp.App, quit func()) *mygo.Menu {
	item := func(label string, fn func()) *mygo.MenuItem {
		return &mygo.MenuItem{
			Label: label,
			Click: func(*mygo.MenuItem, *mygo.Window) { fn() },
		}
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		item(a.T("Show / hide SwitchHosts"), a.ToggleWindow),
		mygo.Separator(),
		item(a.T("Apply hosts"), a.Apply),
		item(a.T("Refresh all remote entries"), a.RefreshAll),
		item(a.T("Undo last apply"), a.Undo),
		mygo.Separator(),
		item(a.T("Preferences…"), a.OpenPreferences),
		item(a.T("Trash…"), a.OpenTrash),
		mygo.Separator(),
		item(a.T("Quit"), quit),
	})
}

func installTray(a *uiapp.App, quit func()) {
	menu := trayMenu(a, quit)

	var err error
	trayRef, err = mygo.NewTray(mygo.TrayOptions{
		ID:             "switchhosts-main",
		Icon:           trayIconPNG(),
		IconIsTemplate: false,
		ToolTip:        "SwitchHosts Native",
		Menu:           menu,
	})
	if err != nil {
		// A missing system tray must not make the app unusable: the window
		// and the native menu still work.
		log.Printf("tray unavailable: %v", err)
		trayRef = nil
	}
}

// trayIconPNG draws a 32x32 icon with the standard library only. Generating
// it in Go keeps the project asset-free: no bundled PNG, no icon font, no
// image decoder beyond image/png, and no platform-specific resource script.
func trayIconPNG() []byte {
	const n = 32
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	bg := color.RGBA{R: 0x25, G: 0x63, B: 0xeb, A: 0xff}
	fg := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			img.Set(x, y, bg)
		}
	}
	// A block "H" for Hosts: two stems and a crossbar.
	for y := 7; y < 25; y++ {
		for x := 8; x < 13; x++ {
			img.Set(x, y, fg)
		}
		for x := 19; x < 24; x++ {
			img.Set(x, y, fg)
		}
	}
	for y := 14; y < 18; y++ {
		for x := 12; x < 20; x++ {
			img.Set(x, y, fg)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
