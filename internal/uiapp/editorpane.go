package uiapp

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/switchhosts/go-native/internal/model"
)

// mainPane holds the native code editor (the replacement for CodeMirror) and
// the metadata of the selected entry.
func (a *App) mainPane(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		n := a.current()
		if n == nil {
			ui.Column(c).Fill().Center().Gap(8).Children(func() {
				ui.Text(c, a.T("No hosts entry selected")).FontSize(16).Bold()
				ui.Text(c, a.T("Use “New entry” to create one.")).TextColor(t.TextMuted)
			})
			return
		}
		a.paneHeader(c, t, n)
		if n.Type == model.TypeFolder || n.Type == model.TypeGroup {
			a.containerPane(c, t, n)
		} else {
			a.editorPane(c, t, n)
		}
		a.paneFooter(c, t, n)
	})
}

func (a *App) paneHeader(c *ui.Context, t *ui.Theme, n *model.Node) {
	ui.Row(c).Gap(8).Padding(10, 14).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, n.Title).FontSize(17).Bold().NoWrap().Ellipsis("…")
			ui.Text(c, a.subtitleFor(n)).FontSize(11).TextColor(t.TextMuted).
				NoWrap().Ellipsis("…")
		})
		if n.Type != model.TypeFolder && n.Type != model.TypeGroup {
			dirty := a.editor != nil && a.editorID == n.ID && a.editor.Buffer.Modified()
			if dirty {
				ui.Text(c, a.T("Unsaved")).FontSize(11).TextColor(t.Warning)
			}
			if ui.Button(c, a.T("Save entry")).Disabled(!dirty).Clicked() {
				a.Save()
			}
			if ui.PrimaryButton(c, a.T("Apply now")).Clicked() {
				a.Apply()
			}
		}
		if n.Type == model.TypeRemote {
			if ui.Button(c, a.T("Refresh")).Clicked() {
				a.RefreshOne(n.ID)
			}
		}
		if n.ID != model.SystemNodeID {
			if ui.Button(c, a.T("Edit entry")).Clicked() {
				a.EditSelected()
			}
		}
	})
}

func (a *App) subtitleFor(n *model.Node) string {
	switch {
	case n.IsSys || n.ID == model.SystemNodeID:
		return a.T("Live contents of the operating system hosts file · edits write it directly")
	case n.Type == model.TypeRemote:
		src, _ := n.Extras["source"].(string)
		if src == "domain" {
			return a.F("Remote · %d domains via DoH · last refresh %s",
				len(anyStrings(n.Extras["domains"])), a.orNever(n.LastRefresh))
		}
		return a.F("Remote · %s · last refresh %s",
			orDefault(n.URL, a.T("no URL")), a.orNever(n.LastRefresh))
	case n.Type == model.TypeFolder:
		return a.F("Folder · %d children · choice mode %d", len(n.Children), n.FolderMode)
	case n.Type == model.TypeGroup:
		return a.F("Group · %d included entries", len(n.Include))
	default:
		return a.F("Local entry · entries/%s.hosts", n.ID)
	}
}

func (a *App) orNever(s string) string {
	if s == "" {
		return a.T("never")
	}
	return s
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func (a *App) editorPane(c *ui.Context, t *ui.Theme, n *model.Node) {
	if a.editor == nil || a.editorID != n.ID {
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, a.T("Loading entry…")).TextColor(t.TextMuted)
		})
		return
	}
	if a.editorErr != "" {
		ui.Row(c).Padding(8, 14).Children(func() {
			ui.Text(c, a.editorErr).FontSize(11).TextColor(t.Danger)
		})
	}
	a.editor.ReadOnly = false
	editor.View(c, a.editor).Grow(1).FillWidth()
}

func (a *App) paneFooter(c *ui.Context, t *ui.Theme, n *model.Node) {
	left := ""
	if a.editor != nil && a.editorID == n.ID {
		pos := a.editor.CursorPos()
		left = a.F("Ln %d, Col %d · %d lines · %s",
			pos.Line+1, pos.Col+1, a.editor.Buffer.LineCount(),
			a.editor.Buffer.Language().Name)
	} else {
		left = a.T("No editable content")
	}
	right := ""
	if n.Type == model.TypeRemote && n.RefreshInterval > 0 {
		right = a.F("ID %s · refresh every %ds", n.ID, n.RefreshInterval)
	} else {
		right = a.F("ID %s", n.ID)
	}
	ui.Row(c).Gap(12).Padding(5, 14).BorderWidth(1, 0, 0, 0).
		BorderColor(t.Border).Children(func() {
		ui.Text(c, left).FontSize(11).TextColor(t.TextMuted).Grow(1)
		ui.Text(c, right).FontSize(11).TextColor(t.TextMuted)
	})
}

// containerPane shows folder/group structure instead of an editor, because
// those nodes have no entries/<id>.hosts file of their own.
func (a *App) containerPane(c *ui.Context, t *ui.Theme, n *model.Node) {
	ui.Scroll(c).Grow(1).FillWidth().Padding(16).Gap(8).Children(func() {
		if n.Type == model.TypeFolder {
			ui.Text(c, a.T("Choice mode")).FontSize(13).Bold()
			ui.Text(c, a.T("Controls whether enabling one child turns the others off.")).
				FontSize(11).TextColor(t.TextMuted)
			mode := strconv.Itoa(n.FolderMode)
			labels := a.choiceModeLabels()
			chosen := labels[modeIndex(mode)]
			if ui.Select(c, &chosen, labels).Width(240).Changed() {
				id, value := n.ID, modeOfIndex(indexOf(labels, chosen))
				a.mutate("Saving folder mode", func() error {
					return a.svc.EditNode(id, func(node *model.Node) { node.FolderMode = value })
				})
			}
			ui.Divider(c)
			ui.Text(c, a.T("Contents")).FontSize(13).Bold()
			if len(n.Children) == 0 {
				ui.Text(c, a.T("This folder is empty. Right-click it to add an entry.")).
					FontSize(12).TextColor(t.TextMuted)
			}
			for _, ch := range n.Children {
				child := ch
				ui.Row(c).Gap(8).Padding(6, 8).AlignItems(ui.Center).
					Background(t.Surface).Radius(6).Children(func() {
					ui.Text(c, tagFor(child)).FontSize(9).Bold().TextColor(t.TextMuted).Width(26)
					ui.Text(c, child.Title).Grow(1).NoWrap().Ellipsis("…")
					if ui.Button(c, a.T("Open")).Clicked() {
						a.selectNode(child.ID)
					}
				})
			}
			return
		}

		ui.Text(c, a.T("Included entries")).FontSize(13).Bold()
		ui.Text(c, a.T("A group combines the content of the entries whose ids are listed here.")).
			FontSize(11).TextColor(t.TextMuted)
		if len(n.Include) == 0 {
			ui.Text(c, a.T("Nothing included yet. Use “Edit entry” to add ids.")).
				FontSize(12).TextColor(t.TextMuted)
		}
		for _, id := range n.Include {
			ui.Text(c, id).FontSize(12).TextColor(t.TextMuted)
		}
	})
}

// The choice-mode Select uses translated labels, so selection is converted
// back to the numeric v5 folder mode by index, never by label text.
func (a *App) choiceModeLabels() []string {
	return []string{
		a.T("Inherit global setting"),
		a.T("Single choice"),
		a.T("Multiple choice"),
	}
}

func modeIndex(mode string) int {
	switch mode {
	case "1":
		return 1
	case "2":
		return 2
	default:
		return 0
	}
}

func modeOfIndex(i int) int {
	switch i {
	case 1:
		return 1
	case 2:
		return 2
	default:
		return 0
	}
}
