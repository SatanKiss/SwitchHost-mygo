// Package model defines the SwitchHosts v5 on-disk data model.
//
// It is deliberately byte-format compatible with the Tauri/Rust build's
// `~/.SwitchHosts` layout so that an existing installation can be opened by
// this pure-Go build without migrating anything:
//
//	manifest.json          format/schemaVersion/root, nested camelCase nodes
//	entries/<id>.hosts     one UTF-8/LF file per local or remote node
//	internal/config.json   flat snake_case preferences + format envelope
//	internal/state.json    tree.collapsedNodeIds + format envelope
//	trashcan.json          format/schemaVersion/items
//	internal/histories/system-hosts.json   bare JSON array
package model

import (
	"encoding/json"
	"fmt"
)

const (
	ManifestFormat        = "switchhosts-data"
	ManifestSchemaVersion = 1
	ConfigFormat          = "switchhosts-config"
	ConfigSchemaVersion   = 1
	StateFormat           = "switchhosts-state"
	StateSchemaVersion    = 1
	TrashcanFormat        = "switchhosts-trashcan"
	TrashcanSchemaVersion = 1
	PointerDirName        = "net.oldj.switchhosts"
	PointerFileName       = "data_dir.json"
	SystemNodeID          = "0"
	DefaultHTTPAPIPort    = 50761
)

// Node types, exactly as the renderer's HostsType.
const (
	TypeLocal  = "local"
	TypeRemote = "remote"
	TypeGroup  = "group"
	TypeFolder = "folder"
)

// Node is one entry in the hosts tree, in the renderer-facing (legacy) shape.
// Unknown fields are preserved in Extras so a Go round-trip never drops data
// written by another SwitchHosts build.
type Node struct {
	ID              string
	Type            string
	Title           string
	On              bool
	IsSys           bool
	URL             string
	LastRefresh     string
	LastRefreshMs   int64
	RefreshInterval int // seconds
	Include         []string
	FolderMode      int // 0 inherit, 1 single choice, 2 multiple choice
	Children        []*Node
	Collapsed       bool // persisted in internal/state.json, not manifest.json
	Extras          map[string]any
}

var remoteMetadata = map[string]string{
	"source":                "kind",
	"domains":               "domains",
	"domain_results":        "domainResults",
	"domain_refresh_status": "domainRefreshStatus",
	"last_attempt":          "lastAttempt",
	"last_attempt_ms":       "lastAttemptMs",
}

// legacyModeledKeys are the keys Node already understands. Everything else
// goes to Extras and is written back into the v5 "extras" envelope.
var legacyModeledKeys = map[string]bool{
	"id": true, "type": true, "title": true, "on": true,
	"is_sys": true, "isSys": true, "contentFile": true,
	"children": true, "extras": true,
	"url": true, "last_refresh": true, "last_refresh_ms": true,
	"refresh_interval": true, "include": true,
	"folder_mode": true, "folder_open": true, "is_collapsed": true,
	"lastRefresh": true, "lastRefreshMs": true, "refreshIntervalSec": true,
	"source": true, "group": true, "folder": true,
	"domains": true, "domain_results": true, "domain_refresh_status": true,
	"last_attempt": true, "last_attempt_ms": true,
	"domainResults": true, "domainRefreshStatus": true,
	"lastAttempt": true, "lastAttemptMs": true, "kind": true,
}

func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Extras = cloneMap(n.Extras)
	c.Include = append([]string(nil), n.Include...)
	c.Children = make([]*Node, 0, len(n.Children))
	for _, ch := range n.Children {
		c.Children = append(c.Children, ch.Clone())
	}
	return &c
}

// NodeFromMap reads either a v5 nested node or a legacy flat node.
func NodeFromMap(m map[string]any) *Node {
	n := &Node{Extras: map[string]any{}}
	n.ID = str(m["id"])
	n.Type = str(m["type"])
	if n.Type == "" {
		n.Type = TypeLocal
	}
	n.Title = str(m["title"])
	n.On = boolean(m["on"])
	if v, ok := m["isSys"]; ok {
		n.IsSys = boolean(v)
	} else {
		n.IsSys = boolean(m["is_sys"])
	}

	switch n.Type {
	case TypeRemote:
		if src, ok := asMap(m["source"]); ok {
			n.URL = str(src["url"])
			n.LastRefresh = str(src["lastRefresh"])
			n.LastRefreshMs = int64(num(src["lastRefreshMs"]))
			n.RefreshInterval = int(num(src["refreshIntervalSec"]))
			for legacy, persisted := range remoteMetadata {
				if v, exists := src[persisted]; exists {
					n.Extras[legacy] = v
				}
			}
		} else {
			// Legacy flat shape. "source" here is the discriminator string.
			if s, ok := m["source"].(string); ok {
				n.Extras["source"] = s
			}
			n.URL = str(m["url"])
			n.LastRefresh = str(m["last_refresh"])
			n.LastRefreshMs = int64(num(m["last_refresh_ms"]))
			n.RefreshInterval = int(num(m["refresh_interval"]))
			for legacy := range remoteMetadata {
				if v, exists := m[legacy]; exists {
					n.Extras[legacy] = v
				}
			}
		}
	case TypeGroup:
		if g, ok := asMap(m["group"]); ok {
			n.Include = stringsOf(g["include"])
		} else {
			n.Include = stringsOf(m["include"])
		}
	case TypeFolder:
		if f, ok := asMap(m["folder"]); ok {
			n.FolderMode = int(num(f["mode"]))
		} else {
			n.FolderMode = int(num(m["folder_mode"]))
		}
		if v, ok := m["folder_open"]; ok {
			n.Collapsed = !boolean(v)
		}
		if boolean(m["is_collapsed"]) {
			n.Collapsed = true
		}
		n.Children = nodesOf(m["children"])
	}

	if ex, ok := asMap(m["extras"]); ok {
		for k, v := range ex {
			if !legacyModeledKeys[k] {
				n.Extras[k] = v
			}
		}
	}
	for k, v := range m {
		if !legacyModeledKeys[k] && k != "extras" {
			if _, exists := n.Extras[k]; !exists {
				n.Extras[k] = v
			}
		}
	}
	return n
}

// ToMap writes a node in the v5 nested camelCase shape.
func (n *Node) ToMap() map[string]any {
	m := map[string]any{
		"id":    n.ID,
		"type":  n.Type,
		"title": n.Title,
		"on":    n.On,
	}
	if n.IsSys {
		m["isSys"] = true
	}
	if (n.Type == TypeLocal || n.Type == TypeRemote) && !n.IsSys && n.ID != SystemNodeID {
		m["contentFile"] = fmt.Sprintf("entries/%s.hosts", n.ID)
	}
	switch n.Type {
	case TypeRemote:
		src := map[string]any{}
		if n.URL != "" {
			src["url"] = n.URL
		}
		if n.LastRefresh != "" {
			src["lastRefresh"] = n.LastRefresh
		}
		if n.LastRefreshMs != 0 {
			src["lastRefreshMs"] = n.LastRefreshMs
		}
		if n.RefreshInterval != 0 {
			src["refreshIntervalSec"] = n.RefreshInterval
		}
		for legacy, persisted := range remoteMetadata {
			if v, ok := n.Extras[legacy]; ok {
				src[persisted] = v
			}
		}
		if len(src) > 0 {
			m["source"] = src
		}
	case TypeGroup:
		include := n.Include
		if include == nil {
			include = []string{}
		}
		m["group"] = map[string]any{"include": include}
	case TypeFolder:
		m["folder"] = map[string]any{"mode": n.FolderMode}
		children := make([]any, 0, len(n.Children))
		for _, ch := range n.Children {
			children = append(children, ch.ToMap())
		}
		m["children"] = children
	}
	extras := map[string]any{}
	for k, v := range n.Extras {
		if !legacyModeledKeys[k] {
			extras[k] = v
		}
	}
	if len(extras) > 0 {
		m["extras"] = extras
	}
	return m
}

func NodesFromMaps(items []any) []*Node {
	out := make([]*Node, 0, len(items))
	for _, it := range items {
		if m, ok := asMap(it); ok {
			out = append(out, NodeFromMap(m))
		}
	}
	return out
}

func NodesToMaps(nodes []*Node) []any {
	out := make([]any, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ToMap())
	}
	return out
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config mirrors AppConfig in src-tauri/src/storage/config.rs (and
// ConfigsType in src/common/default_configs.ts). Field names are the wire
// names, so the same internal/config.json works in both builds.
type Config struct {
	LeftPanelShow               bool   `json:"left_panel_show"`
	LeftPanelWidth              int    `json:"left_panel_width"`
	RightPanelShow              bool   `json:"right_panel_show"`
	RightPanelWidth             int    `json:"right_panel_width"`
	UseSystemWindowFrame        bool   `json:"use_system_window_frame"`
	WriteMode                   string `json:"write_mode"`
	HistoryLimit                int    `json:"history_limit"`
	HistoryEnabled              bool   `json:"history_enabled"`
	Locale                      string `json:"locale,omitempty"`
	Theme                       string `json:"theme"`
	ChoiceMode                  int    `json:"choice_mode"`
	ShowTitleOnTray             bool   `json:"show_title_on_tray"`
	LaunchAtLogin               bool   `json:"launch_at_login"`
	HideAtLaunch                bool   `json:"hide_at_launch"`
	SendUsageData               bool   `json:"send_usage_data"`
	CmdAfterHostsApply          string `json:"cmd_after_hosts_apply"`
	RemoveDuplicateRecords      bool   `json:"remove_duplicate_records"`
	HideDockIcon                bool   `json:"hide_dock_icon"`
	MultiChoseFolderSwitchAll   bool   `json:"multi_chose_folder_switch_all"`
	TrayMiniWindow              bool   `json:"tray_mini_window"`
	LightweightMode             bool   `json:"lightweight_mode"`
	QuitOnClose                 bool   `json:"quit_on_close"`
	FindIsRegexp                bool   `json:"find_is_regexp"`
	FindIsIgnoreCase            bool   `json:"find_is_ignore_case"`
	FindResultColumnWidths      []int  `json:"find_result_column_widths"`
	UseProxy                    bool   `json:"use_proxy"`
	ProxyProtocol               string `json:"proxy_protocol"`
	ProxyHost                   string `json:"proxy_host"`
	ProxyPort                   int    `json:"proxy_port"`
	DNSProvider                 string `json:"dns_provider"`
	DNSCustomURL                string `json:"dns_custom_url"`
	RefreshRemoteHostsOnStartup bool   `json:"refresh_remote_hosts_on_startup"`
	HTTPAPIOn                   bool   `json:"http_api_on"`
	HTTPAPIOnlyLocal            bool   `json:"http_api_only_local"`
	HTTPAPIPort                 int    `json:"http_api_port"`
	AutoCheckUpdate             bool   `json:"auto_check_update"`
}

func DefaultConfig() Config {
	return Config{
		LeftPanelShow: true, LeftPanelWidth: 270,
		RightPanelShow: false, RightPanelWidth: 240,
		UseSystemWindowFrame: false,
		WriteMode:            "append", HistoryLimit: 50, HistoryEnabled: true,
		Theme: "system", ChoiceMode: 2,
		ShowTitleOnTray: false, LaunchAtLogin: false, HideAtLaunch: false,
		SendUsageData: false, CmdAfterHostsApply: "",
		RemoveDuplicateRecords: false, HideDockIcon: false,
		MultiChoseFolderSwitchAll: false, TrayMiniWindow: true,
		LightweightMode: false, QuitOnClose: false,
		FindIsRegexp: false, FindIsIgnoreCase: false,
		FindResultColumnWidths: []int{},
		UseProxy:               false, ProxyProtocol: "http", ProxyHost: "", ProxyPort: 0,
		DNSProvider: "alidns", DNSCustomURL: "",
		RefreshRemoteHostsOnStartup: false,
		HTTPAPIOn:                   false, HTTPAPIOnlyLocal: true, HTTPAPIPort: DefaultHTTPAPIPort,
		AutoCheckUpdate: true,
	}
}

// configKnownKeys is used to separate unknown keys into extras, so a config
// written by a newer SwitchHosts survives this build untouched.
var configKnownKeys = func() map[string]bool {
	b, _ := json.Marshal(DefaultConfig())
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	out := make(map[string]bool, len(m)+3)
	for k := range m {
		out[k] = true
	}
	out["locale"] = true
	out["format"] = true
	out["schemaVersion"] = true
	return out
}()

// ParseConfig reads internal/config.json, falling back to defaults per field
// and returning unknown keys so SaveConfig can put them back.
func ParseConfig(data []byte) (Config, map[string]any) {
	cfg := DefaultConfig()
	extras := map[string]any{}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return cfg, extras
	}
	if b, err := json.Marshal(raw); err == nil {
		_ = json.Unmarshal(b, &cfg) // unknown keys are ignored
	}
	for k, v := range raw {
		if !configKnownKeys[k] {
			extras[k] = v
		}
	}
	if cfg.HTTPAPIPort == 0 || cfg.HTTPAPIPort > 65535 {
		cfg.HTTPAPIPort = DefaultHTTPAPIPort
	}
	if cfg.Theme != "light" && cfg.Theme != "dark" && cfg.Theme != "system" {
		cfg.Theme = "system"
	}
	if cfg.WriteMode == "" {
		cfg.WriteMode = "append"
	}
	if cfg.FindResultColumnWidths == nil {
		cfg.FindResultColumnWidths = []int{}
	}
	return cfg, extras
}

// ConfigToMap serializes a config with its format envelope and unknown keys.
func ConfigToMap(cfg Config, extras map[string]any) map[string]any {
	b, _ := json.Marshal(cfg)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k, v := range extras {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	m["format"] = ConfigFormat
	m["schemaVersion"] = ConfigSchemaVersion
	return m
}

// ---------------------------------------------------------------------------
// Tree helpers
// ---------------------------------------------------------------------------

// Walk calls fn for every node, depth first, parents before children.
func Walk(nodes []*Node, fn func(n *Node, depth int, parent *Node) bool) {
	var walk func(list []*Node, depth int, parent *Node) bool
	walk = func(list []*Node, depth int, parent *Node) bool {
		for _, n := range list {
			if !fn(n, depth, parent) {
				return false
			}
			if len(n.Children) > 0 && !walk(n.Children, depth+1, n) {
				return false
			}
		}
		return true
	}
	walk(nodes, 0, nil)
}

func FindNode(nodes []*Node, id string) *Node {
	var found *Node
	Walk(nodes, func(n *Node, _ int, _ *Node) bool {
		if n.ID == id {
			found = n
			return false
		}
		return true
	})
	return found
}

// RemoveNode detaches a node, reports its former parent, and returns the
// resulting slice (the root forest may need to be reassigned by the caller).
func RemoveNode(nodes []*Node, id string) (*Node, *Node, []*Node) {
	for i, n := range nodes {
		if n.ID == id {
			out := append(nodes[:i:i], nodes[i+1:]...)
			return n, nil, out
		}
	}
	for i, n := range nodes {
		if len(n.Children) == 0 {
			continue
		}
		removed, parent, children := RemoveNode(n.Children, id)
		if removed != nil {
			nodes[i].Children = children
			if parent == nil {
				parent = n
			}
			return removed, parent, nodes
		}
	}
	return nil, nil, nodes
}

// PathOf returns the index path from the root forest to id.
func PathOf(nodes []*Node, id string) []int {
	for i, n := range nodes {
		if n.ID == id {
			return []int{i}
		}
		if sub := PathOf(n.Children, id); sub != nil {
			return append([]int{i}, sub...)
		}
	}
	return nil
}

func NodeAt(nodes []*Node, path []int) *Node {
	cur := nodes
	for i, idx := range path {
		if idx < 0 || idx >= len(cur) {
			return nil
		}
		if i == len(path)-1 {
			return cur[idx]
		}
		cur = cur[idx].Children
	}
	return nil
}

// SetOnStateOfItem is a direct port of set_on_state_of_item in
// src-tauri/src/storage/manifest.rs (which itself ports hostsFn.ts).
// It implements folder choice modes and parent reconciliation.
func SetOnStateOfItem(root []*Node, id string, on bool, defaultChoiceMode int, switchAll bool) {
	path := PathOf(root, id)
	if path == nil {
		return
	}
	if n := NodeAt(root, path); n != nil {
		n.On = on
	}
	inTopLevel := len(path) == 1
	if switchAll {
		if n := NodeAt(root, path); n != nil {
			switchFolderChild(n, on)
		}
		if !inTopLevel {
			switchItemParentIsOn(root, path, on)
		}
	}
	if !on {
		return
	}
	if inTopLevel {
		if defaultChoiceMode != 1 {
			return
		}
		chosen := path[0]
		for i, n := range root {
			if i == chosen {
				continue
			}
			turnOff(n, switchAll)
		}
		return
	}
	parentPath := path[:len(path)-1]
	mode := defaultChoiceMode
	if p := NodeAt(root, parentPath); p != nil && p.FolderMode != 0 {
		mode = p.FolderMode
	}
	if mode != 1 {
		return
	}
	chosen := path[len(path)-1]
	parent := NodeAt(root, parentPath)
	if parent == nil {
		return
	}
	for i, ch := range parent.Children {
		if i == chosen {
			continue
		}
		turnOff(ch, switchAll)
	}
}

func turnOff(n *Node, cascade bool) {
	n.On = false
	if cascade {
		switchFolderChild(n, false)
	}
}

func switchFolderChild(n *Node, on bool) {
	if n.Type != TypeFolder || n.FolderMode == 1 {
		return
	}
	for _, ch := range n.Children {
		ch.On = on
		switchFolderChild(ch, on)
	}
}

func switchItemParentIsOn(root []*Node, path []int, on bool) {
	if len(path) < 2 {
		return
	}
	pp := path[:len(path)-1]
	parent := NodeAt(root, pp)
	if parent == nil || parent.FolderMode == 1 {
		return
	}
	if !on {
		parent.On = false
	} else {
		all := len(parent.Children) > 0
		for _, ch := range parent.Children {
			if !ch.On {
				all = false
				break
			}
		}
		parent.On = all
	}
	switchItemParentIsOn(root, pp, on)
}

// CollectContentIDs returns the ids of every local/remote node.
func CollectContentIDs(nodes []*Node) []string {
	var out []string
	Walk(nodes, func(n *Node, _ int, _ *Node) bool {
		if n.Type == TypeLocal || n.Type == TypeRemote {
			out = append(out, n.ID)
		}
		return true
	})
	return out
}

// CollapsedIDs returns the ids of every collapsed folder.
func CollapsedIDs(nodes []*Node) []string {
	var out []string
	Walk(nodes, func(n *Node, _ int, _ *Node) bool {
		if n.Type == TypeFolder && n.Collapsed {
			out = append(out, n.ID)
		}
		return true
	})
	return out
}

func ApplyCollapsedIDs(nodes []*Node, ids []string) {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	Walk(nodes, func(n *Node, _ int, _ *Node) bool {
		if n.Type == TypeFolder {
			n.Collapsed = set[n.ID]
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func str(v any) string {
	s, _ := v.(string)
	return s
}

func boolean(v any) bool {
	b, _ := v.(bool)
	return b
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func stringsOf(v any) []string {
	a, ok := v.([]any)
	if !ok {
		if ss, ok := v.([]string); ok {
			return append([]string(nil), ss...)
		}
		return nil
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		out = append(out, str(x))
	}
	return out
}

func nodesOf(v any) []*Node {
	a, ok := v.([]any)
	if !ok {
		return nil
	}
	return NodesFromMaps(a)
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
