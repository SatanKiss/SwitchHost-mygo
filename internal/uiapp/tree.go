package uiapp

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/switchhosts/go-native/internal/model"
)

// sidebar is the hosts tree. It is a real native tree: every row is a Go
// widget with its own switch, its own click target and a native context menu.
// No DOM, no virtualised HTML list, no renderer process.
func (a *App) sidebar(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(t.Surface).Children(func() {
		ui.Row(c).Gap(6).Padding(10, 10, 6, 10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.T("HOSTS")).FontSize(11).Bold().TextColor(t.TextMuted).Grow(1)
			if ui.Button(c, a.T("New folder")).Tooltip(a.T("Create a folder at the root")).Clicked() {
				a.NewFolder()
			}
			if ui.Button(c, a.T("New entry")).Tooltip(a.T("Create a local hosts entry")).Clicked() {
				a.NewLocal()
			}
		})
		ui.Box(c).PaddingX(10).PaddingY(4).Children(func() {
			ui.SearchField(c, &a.treeSearch).Placeholder(a.T("Filter hosts…")).FillWidth()
		})
		ui.Scroll(c).Grow(1).FillWidth().Gap(2).Padding(4, 6).Children(func() {
			query := strings.ToLower(strings.TrimSpace(a.treeSearch))
			a.treeRows(c, t, a.tree, 0, query)
		})
		total, enabled := 0, 0
		model.Walk(a.tree, func(n *model.Node, _ int, _ *model.Node) bool {
			total++
			if n.On && (n.Type == model.TypeLocal || n.Type == model.TypeRemote) {
				enabled++
			}
			return true
		})
		ui.Row(c).Padding(8, 10).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, a.F("%d entries · %d enabled", total, enabled)).
				FontSize(11).TextColor(t.TextMuted)
		})
	})
}

func (a *App) treeRows(c *ui.Context, t *ui.Theme, nodes []*model.Node, depth int, query string) {
	for _, n := range nodes {
		match := query == "" || strings.Contains(strings.ToLower(n.Title), query)
		hasMatchBelow := subtreeMatches(n.Children, query)
		if !match && !hasMatchBelow {
			continue
		}
		a.treeRow(c, t, n, depth)
		if n.Type == model.TypeFolder && (query != "" || !n.Collapsed) {
			a.treeRows(c, t, n.Children, depth+1, query)
		}
	}
}

func subtreeMatches(nodes []*model.Node, query string) bool {
	if query == "" {
		return false
	}
	for _, n := range nodes {
		if strings.Contains(strings.ToLower(n.Title), query) || subtreeMatches(n.Children, query) {
			return true
		}
	}
	return false
}

func (a *App) treeRow(c *ui.Context, t *ui.Theme, n *model.Node, depth int) {
	row := ui.Row(c).Key("row-" + n.ID).Gap(4).Height(30).FillWidth().
		AlignItems(ui.Center).Radius(6)
	if n.ID == a.selected {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Box(c).Width(float32(depth * 14)).Shrink(0)
		if n.Type == model.TypeFolder {
			mark := "-"
			if n.Collapsed {
				mark = "+"
			}
			if ui.Button(c, mark).Width(24).Height(22).
				Tooltip(a.T("Expand or collapse folder")).Clicked() {
				a.ToggleCollapse(n)
			}
		} else {
			ui.Box(c).Width(24).Shrink(0)
		}

		if ui.ButtonBase(c).Grow(1).Children(func() {
			ui.Text(c, tagFor(n)).FontSize(9).Bold().TextColor(t.TextMuted).Width(26)
			ui.Text(c, n.Title).FontSize(13).Grow(1).NoWrap().Ellipsis("…")
		}).Label(a.F("Select %s", n.Title)).Clicked() {
			a.selectNode(n.ID)
		}

		if n.Type == model.TypeLocal || n.Type == model.TypeRemote {
			on := n.On
			if ui.Switch(c, &on).Label(a.F("Enable %s", n.Title)).Changed() {
				a.ToggleNode(n.ID, on)
			}
		}
	}).ContextMenu(func(m *ui.Menu) { a.nodeMenu(m, n) })
}

// tagFor uses plain ASCII badges so the tree renders identically on every
// platform and in every language without relying on an icon font or an image
// bundle. These are not translated.
func tagFor(n *model.Node) string {
	switch {
	case n.IsSys || n.ID == model.SystemNodeID:
		return "SYS"
	case n.Type == model.TypeRemote:
		return "NET"
	case n.Type == model.TypeFolder:
		return "DIR"
	case n.Type == model.TypeGroup:
		return "GRP"
	default:
		return "LOC"
	}
}

func (a *App) nodeMenu(m *ui.Menu, n *model.Node) {
	if m.Item(a.T("Open")).Chosen() {
		a.selectNode(n.ID)
	}
	if m.Item(a.T("Rename / edit…")).Chosen() {
		a.openNodeForm(true, n, "")
	}
	if n.Type == model.TypeRemote {
		if m.Item(a.T("Refresh now")).Chosen() {
			a.RefreshOne(n.ID)
		}
	}
	if n.Type == model.TypeFolder {
		if m.Item(a.T("New entry inside")).Chosen() {
			a.NewInFolder(n.ID)
		}
		if m.Item(a.T("Expand / collapse")).Chosen() {
			a.ToggleCollapse(n)
		}
	}
	m.Separator()
	if n.ID != model.SystemNodeID {
		if m.Item(a.T("Move to trash")).Chosen() {
			a.DeleteNode(n.ID)
		}
	}
	m.Separator()
	if m.Item(a.T("New local entry")).Chosen() {
		a.NewLocal()
	}
	if m.Item(a.T("New folder")).Chosen() {
		a.NewFolder()
	}
}
