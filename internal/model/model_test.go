package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestNodeV5RoundTripPreservesNestedFieldsAndExtras(t *testing.T) {
	in := map[string]any{
		"id": "r1", "type": "remote", "title": "Remote", "on": true,
		"contentFile": "entries/r1.hosts",
		"source": map[string]any{
			"url":                "https://example.test/hosts",
			"lastRefresh":        "2026-01-01T00:00:00Z",
			"lastRefreshMs":      float64(1767225600000),
			"refreshIntervalSec": float64(3600),
			"kind":               "url",
			"lastAttemptMs":      float64(1767225600001),
		},
		"extras": map[string]any{"custom_future_field": "keep me"},
	}
	n := NodeFromMap(in)
	if n.URL != "https://example.test/hosts" || n.RefreshInterval != 3600 {
		t.Fatalf("remote fields not parsed: %+v", n)
	}
	if n.Extras["custom_future_field"] != "keep me" {
		t.Fatalf("extras lost: %+v", n.Extras)
	}
	out := n.ToMap()
	if got := out["contentFile"]; got != "entries/r1.hosts" {
		t.Fatalf("contentFile = %v", got)
	}
	src, _ := out["source"].(map[string]any)
	if src["kind"] != "url" || src["lastAttemptMs"] != float64(1767225600001) {
		t.Fatalf("remote metadata lost: %+v", src)
	}
	ex, _ := out["extras"].(map[string]any)
	if ex["custom_future_field"] != "keep me" {
		t.Fatalf("extras not written back: %+v", out)
	}
	// A second round-trip must be stable.
	if !reflect.DeepEqual(out, NodeFromMap(out).ToMap()) {
		t.Fatal("node round-trip is not stable")
	}
}

func TestSystemNodeHasNoContentFile(t *testing.T) {
	n := &Node{ID: SystemNodeID, Type: TypeLocal, Title: "System Hosts", IsSys: true, On: true}
	m := n.ToMap()
	if _, ok := m["contentFile"]; ok {
		t.Fatal("system node must not own an entries file")
	}
	if m["isSys"] != true {
		t.Fatal("isSys missing")
	}
}

func TestConfigRoundTripAndUnknownKeys(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Theme = "dark"
	cfg.ChoiceMode = 1
	m := ConfigToMap(cfg, map[string]any{"future_pref": float64(42)})
	if m["format"] != ConfigFormat || fmt.Sprint(m["schemaVersion"]) != fmt.Sprint(ConfigSchemaVersion) {
		t.Fatalf("format envelope missing: %+v", m)
	}
	b, _ := json.Marshal(m)
	got, extras := ParseConfig(b)
	if got.Theme != "dark" || got.ChoiceMode != 1 || got.HTTPAPIPort != DefaultHTTPAPIPort {
		t.Fatalf("config changed: %+v", got)
	}
	if extras["future_pref"] != float64(42) {
		t.Fatalf("unknown key lost: %+v", extras)
	}
}

func TestConfigDefaultsForPartialFile(t *testing.T) {
	cfg, _ := ParseConfig([]byte(`{"theme":"sepia","http_api_port":99999}`))
	if cfg.Theme != "system" {
		t.Fatalf("invalid theme not normalised: %q", cfg.Theme)
	}
	if cfg.HTTPAPIPort != DefaultHTTPAPIPort {
		t.Fatalf("invalid port not normalised: %d", cfg.HTTPAPIPort)
	}
	if cfg.WriteMode != "append" || !cfg.HistoryEnabled || cfg.LeftPanelWidth != 270 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestSetOnStateSingleChoiceAtRoot(t *testing.T) {
	root := []*Node{
		{ID: "a", On: false},
		{ID: "b", On: true},
		{ID: "c", On: true},
	}
	// choice_mode 1: turning a on turns every other top-level node off.
	SetOnStateOfItem(root, "a", true, 1, true)
	if !root[0].On || root[1].On || root[2].On {
		t.Fatalf("single choice failed: %v %v %v", root[0].On, root[1].On, root[2].On)
	}
	// choice_mode 2: multiple selections are allowed.
	root[1].On = false
	SetOnStateOfItem(root, "b", true, 2, true)
	if !root[0].On || !root[1].On {
		t.Fatal("multi choice should keep other selections")
	}
}

func TestSetOnStateFolderModeOverridesGlobal(t *testing.T) {
	root := []*Node{{
		ID: "f", Type: TypeFolder, FolderMode: 1, On: true,
		Children: []*Node{{ID: "x", On: false}, {ID: "y", On: true}},
	}}
	path := PathOf(root, "x")
	if len(path) != 2 {
		t.Fatalf("unexpected path %v", path)
	}
	SetOnStateOfItem(root, "x", true, 2, false)
	if !root[0].Children[0].On || root[0].Children[1].On {
		t.Fatal("folder single-choice override did not apply")
	}
}

func TestRemoveNodeAtRootAndNested(t *testing.T) {
	root := []*Node{
		{ID: "a", Children: []*Node{{ID: "a1"}, {ID: "a2"}}},
		{ID: "b"},
	}
	removed, parent, out := RemoveNode(root, "b")
	if removed == nil || removed.ID != "b" || parent != nil || len(out) != 1 {
		t.Fatalf("root removal failed: %+v %+v %d", removed, parent, len(out))
	}
	root = []*Node{{ID: "a", Children: []*Node{{ID: "a1"}, {ID: "a2"}}}, {ID: "b"}}
	removed, parent, out = RemoveNode(root, "a1")
	if removed == nil || parent == nil || parent.ID != "a" || len(out[0].Children) != 1 {
		t.Fatalf("nested removal failed: %+v %+v %+v", removed, parent, out)
	}
}

func TestCollapsedIDsRoundTrip(t *testing.T) {
	nodes := []*Node{
		{ID: "f1", Type: TypeFolder, Children: []*Node{{ID: "f2", Type: TypeFolder}}},
	}
	ApplyCollapsedIDs(nodes, []string{"f2"})
	ids := CollapsedIDs(nodes)
	if len(ids) != 1 || ids[0] != "f2" {
		t.Fatalf("collapsed ids = %v", ids)
	}
}
