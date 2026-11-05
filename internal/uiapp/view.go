package uiapp

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// WindowOptions describes the single main window. It is deliberately the
// native-UI form: Content is set, while URL and Page stay at their zero
// values. MyGo derives the platform window's Surface flag from
// `Content != nil`; on Windows that is what stops the backend from ever
// starting a WebView2 environment, and on Linux it is why WebKitGTK is
// never loaded. Keeping the options in one testable function makes the
// "no browser shell" guarantee part of the build, not just a comment.
//
// The title is the product name, which is the same in every language.
func (a *App) WindowOptions(hidden bool) mygo.WindowOptions {
	return mygo.WindowOptions{
		Title:     "SwitchHosts",
		Width:     1120,
		Height:    720,
		MinWidth:  820,
		MinHeight: 520,
		Hidden:    hidden,
		StateKey:  "main",
		Content:   ui.View(a.View),
	}
}

// view builds one frame of the whole window. MyGo calls it on the main thread
// after input, after Window.Update / Invalidate, and while something animates.
func (a *App) view(c *ui.Context) {
	// "system" leaves MyGo following the OS appearance. An explicit choice
	// replaces the frame theme with the pure-Go light/dark palettes; neither
	// path touches a web page or a CSS media query.
	switch a.appearance {
	case "light":
		c.SetTheme(ui.LightTheme())
	case "dark":
		c.SetTheme(ui.DarkTheme())
	}
	t := c.Theme()

	// Global shortcuts. ui.Cmd is Command on macOS and Control elsewhere.
	c.OnShortcut(ui.Cmd, ui.KeyS, a.Save)
	c.OnShortcut(ui.Cmd, ui.KeyN, a.NewLocal)
	c.OnShortcut(ui.Cmd, ui.KeyR, a.RefreshAll)
	c.OnShortcut(ui.Cmd, ui.KeyComma, a.OpenPreferences)
	c.OnShortcut(ui.Cmd|ui.Shift, ui.KeyT, a.OpenTrash)
	c.OnShortcut(ui.Cmd|ui.Shift, ui.KeyH, a.OpenHistory)
	c.OnShortcut(ui.Cmd, ui.KeyW, a.Hide)

	// A toast is emitted once, then cleared so it does not repeat each frame.
	if a.notice != "" {
		msg := a.notice
		a.notice = ""
		c.Toast(msg)
	}

	ui.Column(c).Fill().Background(t.Background).Children(func() {
		a.toolbar(c, t)
		ui.Split(c, &a.sidebarWidth, func() {
			a.sidebar(c, t)
		}, func() {
			a.mainPane(c, t)
		}).Grow(1).FillWidth()
		a.statusBar(c, t)
	})

	a.overlays(c, t)
}

func (a *App) toolbar(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(8).Padding(9, 12).AlignItems(ui.Center).
		Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		Children(func() {
			ui.Text(c, "SwitchHosts").FontSize(16).Bold()
			ui.Text(c, a.T("Native")).FontSize(10).TextColor(t.TextMuted)
			ui.Box(c).Grow(1)
			if ui.Button(c, a.T("Refresh all")).Tooltip(a.T("Refresh every remote entry")).Clicked() {
				a.RefreshAll()
			}
			if ui.Button(c, a.T("Undo")).Tooltip(a.T("Restore the hosts file from before the last apply")).Clicked() {
				a.Undo()
			}
			if ui.PrimaryButton(c, a.T("Apply")).Tooltip(a.T("Write all enabled entries to the system hosts file")).Clicked() {
				a.Apply()
			}
			if ui.Button(c, a.T("Preferences")).Clicked() {
				a.OpenPreferences()
			}
			if ui.Button(c, a.T("Trash")).Clicked() {
				a.OpenTrash()
			}
			if ui.Button(c, a.T("History")).Clicked() {
				a.OpenHistory()
			}
			if ui.Button(c, a.T("About")).Clicked() {
				a.OpenAbout()
			}
		})
}

func (a *App) statusBar(c *ui.Context, t *ui.Theme) {
	applied := ""
	if !a.svc.IsApplied() {
		applied = a.T(" · external edit detected")
	}
	ui.Row(c).Gap(10).Padding(6, 12).AlignItems(ui.Center).
		Background(t.Surface).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).
		Children(func() {
			if a.busy > 0 {
				ui.Spinner(c)
			}
			ui.Text(c, a.statusText()+applied).FontSize(11).TextColor(t.TextMuted).
				Grow(1).NoWrap().Ellipsis("…")
			ui.Text(c, a.DataDir()).FontSize(10).TextColor(t.TextMuted).
				MaxWidth(340).NoWrap().Ellipsis("…").Tooltip(a.DataDir())
			if ui.Button(c, a.T("Open data folder")).Clicked() {
				a.openDataDir()
			}
		})
}

func (a *App) openDataDir() {
	// mygo.Shell talks to the running desktop; headless view tests have no
	// application loop and must not call into it.
	if a.win != nil {
		mygo.Shell.OpenPath(a.DataDir())
	}
}
