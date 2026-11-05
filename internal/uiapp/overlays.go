package uiapp

import (
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/switchhosts/go-native/internal/remote"
)

// overlays builds every dialog on top of the main view. They are all MyGo
// native widgets drawn in the same frame as the window: no HTML modal, no
// renderer-side dialog, no browser alert(), no webview.
//
// Modal and AlertDialog own their *open flag: a click on the backdrop or
// Escape closes them. A local bool is enough for the node form because those
// functions only read and write the pointer during the frame they are built,
// never retaining it (see ui's coreDialogBase / coreAlertDialog).
func (a *App) overlays(c *ui.Context, t *ui.Theme) {
	if a.confirmOpen {
		switch ui.AlertDialog(c, &a.confirmOpen,
			a.T(a.confirmTitle), a.T(a.confirmMessage),
			a.T("Cancel"), a.T(a.confirmOK)) {
		case 1:
			a.runConfirm()
		case 0, ui.AlertDismissed:
			// Cancel and Escape dismiss the alert. -1 means no button was
			// chosen yet, so the dialog must stay open in that case. Drop the
			// pending mutation so a cancelled action can never run later.
			a.confirmAction = nil
			a.confirmOpen = false
		}
	}

	if f := a.nodeForm; f != nil {
		open := true
		if ui.Modal(c, &open, func() { a.nodeFormView(c, t, f) }).Dismissed() || !open {
			a.nodeForm = nil
		}
	}
	if a.showPrefs {
		ui.Modal(c, &a.showPrefs, func() { a.prefsView(c, t) })
	}
	if a.showTrash {
		ui.Modal(c, &a.showTrash, func() { a.trashView(c, t) })
	}
	if a.showHistory {
		ui.Modal(c, &a.showHistory, func() { a.historyView(c, t) })
	}
	if a.showAbout {
		ui.Modal(c, &a.showAbout, func() { a.aboutView(c, t) })
	}
}

// ---------------------------------------------------------------------------
// Shared dialog chrome
// ---------------------------------------------------------------------------

func modalHeader(c *ui.Context, t *ui.Theme, title, subtitle string) {
	ui.Column(c).Gap(2).FillWidth().Children(func() {
		ui.Text(c, title).FontSize(18).Bold()
		if subtitle != "" {
			ui.Text(c, subtitle).FontSize(11).TextColor(t.TextMuted).Wrap()
		}
	})
}

// modalButtons puts the actions at the right edge with plain native buttons.
// An empty saveLabel turns the dialog into a read-only panel with one Close
// button. Callers pass already-translated labels.
func (a *App) modalButtons(c *ui.Context, saveLabel string, save, cancel func()) {
	ui.Row(c).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
		ui.Box(c).Grow(1)
		if saveLabel == "" {
			if ui.PrimaryButton(c, a.T("Close")).Clicked() {
				cancel()
			}
			return
		}
		if ui.Button(c, a.T("Cancel")).Clicked() {
			cancel()
		}
		if ui.PrimaryButton(c, saveLabel).Clicked() {
			save()
		}
	})
}

func prefHeading(c *ui.Context, t *ui.Theme, s string) {
	ui.Text(c, s).FontSize(12).Bold().TextColor(t.Accent)
}

func prefNote(c *ui.Context, t *ui.Theme, s string) {
	ui.Text(c, s).FontSize(11).TextColor(t.TextMuted).Wrap()
}

// formError renders a stored validation key in the active language. Keys with
// an argument (a domain, a numeric limit) carry it in errArg.
func (a *App) formError(c *ui.Context, t *ui.Theme, key string, arg any) {
	if key == "" {
		return
	}
	text := a.T(key)
	if arg != nil {
		text = a.F(key, arg)
	}
	ui.Text(c, text).FontSize(11).TextColor(t.Danger).Wrap()
}

// ---------------------------------------------------------------------------
// New / edit entry
// ---------------------------------------------------------------------------

func (a *App) nodeFormView(c *ui.Context, t *ui.Theme, f *nodeForm) {
	ui.Column(c).Gap(10).Width(540).Children(func() {
		head, sub := a.T("New hosts entry"), a.T("Choose what to add to the tree.")
		if f.editing {
			head, sub = a.T("Edit hosts entry"), a.F("ID %s", f.id)
		}
		modalHeader(c, t, head, sub)

		ui.TextInput(c, &f.title).Label(a.T("Title")).Placeholder(a.T("My hosts")).FillWidth()

		// Changing the type of an existing node would orphan its
		// entries/<id>.hosts file, so it is fixed after creation.
		if f.editing {
			prefHeading(c, t, a.F("Type: %s", a.kindLabel(f.kind)))
		} else {
			if ui.Segmented(c, &f.kind,
				a.T("Local"), a.T("Remote"), a.T("Folder"), a.T("Group")).
				Label(a.T("Type")).Changed() {
				f.err, f.errArg = "", nil
			}
		}

		switch f.kind {
		case 1: // remote
			ui.Segmented(c, &f.sourceKind, a.T("URL"), a.T("Domain list")).Label(a.T("Source"))
			if f.sourceKind == 1 {
				ui.TextArea(c, &f.domains).Label(a.T("Domains (one per line)")).
					Placeholder("example.com\napi.example.com").
					MinHeight(110).FillWidth()
				ui.Text(c, a.F("Up to %d distinct domains, resolved over DNS-over-HTTPS.",
					remote.MaxDomains)).FontSize(10).TextColor(t.TextMuted).Wrap()
			} else {
				ui.TextInput(c, &f.url).Label(a.T("URL")).
					Placeholder(a.T("https://example.com/hosts or file:///path/hosts")).FillWidth()
			}
			ui.TextInput(c, &f.interval).
				Label(a.T("Refresh interval (seconds; 0 = manual)")).Width(240)
		case 2: // folder
			labels := a.choiceModeLabels()
			chosen := labels[modeIndex(f.folderMode)]
			if ui.Select(c, &chosen, labels).Width(260).Label(a.T("Choice mode")).Changed() {
				f.folderMode = strconv.Itoa(modeOfIndex(indexOf(labels, chosen)))
			}
			prefNote(c, t, a.T("A folder has no hosts content of its own; it only groups entries."))
		case 3: // group
			ui.TextArea(c, &f.include).Label(a.T("Included entry IDs (one per line)")).
				MinHeight(100).FillWidth()
			prefNote(c, t, a.T("A group combines the content of the listed entries."))
		default: // local
			prefNote(c, t, a.T("Content is stored in entries/<id>.hosts and can be edited after creation."))
		}

		a.formError(c, t, f.err, f.errArg)
		a.modalButtons(c, a.T("Save entry"), a.SaveNodeForm, func() { a.nodeForm = nil })
	})
}

func (a *App) kindLabel(kind int) string {
	switch kind {
	case 1:
		return a.T("Remote")
	case 2:
		return a.T("Folder")
	case 3:
		return a.T("Group")
	default:
		return a.T("Local")
	}
}

// ---------------------------------------------------------------------------
// Preferences
// ---------------------------------------------------------------------------

func (a *App) prefsView(c *ui.Context, t *ui.Theme) {
	p := &a.prefs
	ui.Column(c).Gap(10).Width(560).Children(func() {
		modalHeader(c, t, a.T("Preferences"),
			a.T("Saved to internal/config.json — the very same file SwitchHosts 5 reads."))
		ui.Scroll(c).MaxHeight(420).FillWidth().Children(func() {
			ui.Column(c).Gap(12).FillWidth().Children(func() {
				prefHeading(c, t, a.T("Hosts file"))
				writeIdx := 0
				if p.writeMode == "overwrite" {
					writeIdx = 1
				}
				if ui.Segmented(c, &writeIdx,
					a.T("Append (marked section)"), a.T("Overwrite entire file")).
					Label(a.T("Write mode")).Changed() {
					if writeIdx == 1 {
						p.writeMode = "overwrite"
					} else {
						p.writeMode = "append"
					}
				}
				ui.Checkbox(c, &p.removeDuplicates, a.T("Remove duplicate records when aggregating"))
				ui.TextInput(c, &p.cmd).Label(a.T("Command to run after applying hosts")).
					Placeholder(a.T("Optional shell command")).FillWidth()
				ui.Divider(c)

				prefHeading(c, t, a.T("Appearance and selection"))
				themes := []string{"system", "light", "dark"}
				themeIdx := indexOf(themes, p.theme)
				if ui.Segmented(c, &themeIdx,
					a.T("System"), a.T("Light"), a.T("Dark")).
					Label(a.T("Appearance")).Changed() {
					p.theme = themes[themeIdx]
				}
				values, labels := a.localeOptions()
				chosen := localeLabelFor(p.locale, values, labels)
				if ui.Select(c, &chosen, labels).Width(260).
					Label(a.T("Language")).Changed() {
					p.locale = localeValueFor(chosen, values, labels)
				}
				prefNote(c, t, a.T("Changing the language updates the window and the native menus immediately."))
				choiceIdx := 0
				if p.choiceMode == "2" {
					choiceIdx = 1
				}
				if ui.Segmented(c, &choiceIdx,
					a.T("Single choice"), a.T("Multiple choice")).
					Label(a.T("Default choice mode")).Changed() {
					if choiceIdx == 1 {
						p.choiceMode = "2"
					} else {
						p.choiceMode = "1"
					}
				}
				ui.Checkbox(c, &p.switchAllChildren, a.T("Toggling a folder switches all its children"))
				ui.Divider(c)

				prefHeading(c, t, a.T("Remote and DNS"))
				ids, labels2 := a.providerOptions()
				provider := providerLabelFor(p.dnsProvider, ids, labels2)
				if ui.Select(c, &provider, labels2).Width(320).
					Label(a.T("DNS-over-HTTPS provider")).Changed() {
					p.dnsProvider = providerIDFor(provider, ids, labels2)
				}
				ui.TextInput(c, &p.dnsCustomURL).Label(a.T("Custom DoH endpoint")).
					Placeholder("https://…/dns-query?name={domain}&type=A").FillWidth()
				ui.TextInput(c, &p.proxyPort).Label(a.T("Proxy port")).Width(160)
				ui.Checkbox(c, &p.useProxy, a.T("Use proxy for remote fetches"))
				prefNote(c, t, a.T("Remote metadata stores only safe error categories; URLs, proxy secrets and response bodies are never persisted."))
				ui.Divider(c)

				prefHeading(c, t, a.T("History and startup"))
				ui.TextInput(c, &p.historyLimit).Label(a.T("History limit")).Width(160)
				ui.Checkbox(c, &p.historyEnabled, a.T("Record system hosts history"))
				ui.Checkbox(c, &p.refreshOnStartup, a.T("Refresh remote entries at startup"))
				ui.Checkbox(c, &p.hideAtLaunch, a.T("Hide the window at launch"))
				ui.Checkbox(c, &p.quitOnClose, a.T("Quit instead of hiding to the tray on close"))
			})
		})
		a.formError(c, t, p.err, nil)
		a.modalButtons(c, a.T("Save preferences"), a.SavePreferences, func() {
			p.err = ""
			a.showPrefs = false
		})
	})
}

func indexOf(items []string, want string) int {
	for i, v := range items {
		if v == want {
			return i
		}
	}
	return 0
}

func providerLabelFor(id string, ids, labels []string) string {
	if id == "" {
		id = "alidns"
	}
	for i, v := range ids {
		if v == id && i < len(labels) {
			return labels[i]
		}
	}
	if len(labels) > 0 {
		return labels[0]
	}
	return ""
}

func providerIDFor(label string, ids, labels []string) string {
	for i, v := range labels {
		if v == label && i < len(ids) {
			return ids[i]
		}
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// ---------------------------------------------------------------------------
// Trash
// ---------------------------------------------------------------------------

func (a *App) trashView(c *ui.Context, t *ui.Theme) {
	items := a.svc.Trash()
	ui.Column(c).Gap(10).Width(600).Children(func() {
		modalHeader(c, t, a.T("Trash"),
			a.T("Deleted entries live in trashcan.json and can be put back at any time."))
		ui.Scroll(c).MaxHeight(420).FillWidth().Children(func() {
			if len(items) == 0 {
				ui.Text(c, a.T("The trash is empty.")).FontSize(13).TextColor(t.TextMuted)
				return
			}
			ui.Column(c).Gap(6).FillWidth().Children(func() {
				for _, item := range items {
					id, title, ms := trashItemInfo(item)
					if id == "" {
						continue
					}
					ui.Row(c).Key("trash-"+id).Gap(8).Padding(8, 10).
						AlignItems(ui.Center).Background(t.Surface).Radius(6).
						FillWidth().Children(func() {
						ui.Column(c).Grow(1).Gap(2).Children(func() {
							ui.Text(c, orDefault(title, a.T("(untitled)"))).FontSize(13)
							ui.Text(c, a.F("%s · deleted %s", id, a.formatTime(ms))).
								FontSize(10).TextColor(t.TextMuted)
						})
						if ui.Button(c, a.T("Restore")).Clicked() {
							a.RestoreTrash(id)
						}
					})
				}
			})
		})
		a.modalButtons(c, "", nil, func() { a.showTrash = false })
	})
}

func trashItemInfo(item map[string]any) (id, title string, ms int64) {
	data, _ := item["data"].(map[string]any)
	id, _ = data["id"].(string)
	title, _ = data["title"].(string)
	return id, title, numFromAny(item["add_time_ms"])
}

// numFromAny tolerates both raw JSON numbers (float64) and values that were
// already parsed into Go integers by another code path.
func numFromAny(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case uint64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// ---------------------------------------------------------------------------
// System hosts history
// ---------------------------------------------------------------------------

func (a *App) historyView(c *ui.Context, t *ui.Theme) {
	items := a.svc.History()
	ui.Column(c).Gap(10).Width(620).Children(func() {
		modalHeader(c, t, a.T("System hosts history"),
			a.T("Snapshots from internal/histories/system-hosts.json. Restoring overwrites the OS hosts file."))
		ui.Scroll(c).MaxHeight(420).FillWidth().Children(func() {
			if len(items) == 0 {
				ui.Text(c, a.T("No history recorded yet.")).FontSize(13).TextColor(t.TextMuted)
				return
			}
			ui.Column(c).Gap(6).FillWidth().Children(func() {
				// Newest first, matching the order the web build shows.
				for i := len(items) - 1; i >= 0; i-- {
					h := items[i]
					label := orDefault(h.Label, firstNonEmptyLine(h.Content, 72, a.T("(empty snapshot)")))
					ui.Row(c).Key("hist-"+h.ID).Gap(8).Padding(8, 10).
						AlignItems(ui.Center).Background(t.Surface).Radius(6).
						FillWidth().Children(func() {
						ui.Column(c).Grow(1).Gap(2).Children(func() {
							ui.Text(c, label).FontSize(12).NoWrap().Ellipsis("…")
							ui.Text(c, a.F("%s · %s", a.formatTime(h.AddTimeMs), h.ID)).
								FontSize(10).TextColor(t.TextMuted)
						})
						if ui.Button(c, a.T("Restore")).Clicked() {
							a.RestoreHistory(h.Content)
						}
					})
				}
			})
		})
		a.modalButtons(c, "", nil, func() { a.showHistory = false })
	})
}

func firstNonEmptyLine(s string, limit int, fallback string) string {
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > limit {
			return string(runes[:limit]) + "…"
		}
		return line
	}
	return fallback
}

// ---------------------------------------------------------------------------
// About
// ---------------------------------------------------------------------------

func (a *App) aboutView(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(8).Width(480).Children(func() {
		ui.Text(c, "SwitchHosts Native").FontSize(20).Bold()
		ui.Text(c, a.T("A pure-Go rebuild of SwitchHosts 5.0.3 on the MyGo native UI.")).
			TextColor(t.TextMuted).Wrap()
		ui.Divider(c)
		aboutRow(c, t, a.T("Interface"), a.T("MyGo v0.7.4 · native ui widgets · ui/editor"))
		aboutRow(c, t, a.T("Rendering"), a.T("GPU-drawn by MyGo; no browser kernel at all"))
		aboutRow(c, t, a.T("Excluded"), a.T("WebView2, Electron, WKWebView, WebKitGTK, HTML, CSS, JavaScript"))
		aboutRow(c, t, a.T("Runtime"), runtime.Version()+" · "+runtime.GOOS+"/"+runtime.GOARCH)
		aboutRow(c, t, a.T("Language"), a.Locale().Name())
		aboutRow(c, t, a.T("Data"), a.DataDir())
		if note := a.recoveryText(); note != "" {
			ui.Text(c, note).FontSize(11).TextColor(t.Warning).Wrap()
		}
		ui.Divider(c)
		prefNote(c, t, a.T("Reads and writes the same v5 layout as the Tauri build: manifest.json, entries/<id>.hosts, internal/config.json, internal/state.json, internal/histories/system-hosts.json and trashcan.json. Both builds can be swapped without exporting anything."))
		a.modalButtons(c, "", nil, func() { a.showAbout = false })
	})
}

func aboutRow(c *ui.Context, t *ui.Theme, k, v string) {
	ui.Row(c).Gap(10).FillWidth().Children(func() {
		ui.Text(c, k).FontSize(11).Bold().TextColor(t.TextMuted).Width(94)
		ui.Text(c, v).FontSize(11).Grow(1).Wrap()
	})
}
