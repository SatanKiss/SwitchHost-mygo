package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/switchhosts/go-native/internal/model"
)

func TestOpenAtSeedsSystemNodeAndWritesV5Layout(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".SwitchHosts")
	s, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 1 || s.Nodes[0].ID != model.SystemNodeID || !s.Nodes[0].IsSys {
		t.Fatalf("fresh install not seeded: %+v", s.Nodes)
	}
	if err := s.SaveManifest(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(root, "manifest.json"),
		filepath.Join(root, "internal", "state.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	var raw map[string]any
	b, _ := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["format"] != model.ManifestFormat {
		t.Fatalf("format envelope wrong: %+v", raw)
	}
	nodes, _ := raw["root"].([]any)
	first, _ := nodes[0].(map[string]any)
	if first["isSys"] != true {
		t.Fatalf("system node not in v5 shape: %+v", first)
	}
	if _, ok := first["contentFile"]; ok {
		t.Fatal("system node must not have contentFile")
	}
}

func TestOpenAtReadsAnExistingV5Manifest(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".SwitchHosts")
	manifest := `{
  "format": "switchhosts-data",
  "schemaVersion": 1,
  "root": [
    {"id":"0","type":"local","title":"System Hosts","on":true,"isSys":true},
    {"id":"n1","type":"local","title":"Dev","on":true,"contentFile":"entries/n1.hosts"},
    {"id":"r1","type":"remote","title":"Remote","on":false,
     "contentFile":"entries/r1.hosts",
     "source":{"url":"https://example.test/hosts","refreshIntervalSec":3600,"kind":"url"}},
    {"id":"f1","type":"folder","title":"Folder","on":true,"folder":{"mode":1},
     "children":[{"id":"c1","type":"local","title":"Child","on":false,
                  "contentFile":"entries/c1.hosts","extras":{"note":"future"}}]}
  ]
}`
	if err := os.MkdirAll(filepath.Join(root, "entries"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "config.json"),
		[]byte(`{"format":"switchhosts-config","schemaVersion":1,"theme":"dark","write_mode":"overwrite","future_key":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(s.Nodes))
	}
	if s.Nodes[2].URL != "https://example.test/hosts" || s.Nodes[2].RefreshInterval != 3600 {
		t.Fatalf("remote source not parsed: %+v", s.Nodes[2])
	}
	if s.Nodes[3].FolderMode != 1 || len(s.Nodes[3].Children) != 1 {
		t.Fatalf("folder not parsed: %+v", s.Nodes[3])
	}
	if s.Nodes[3].Children[0].Extras["note"] != "future" {
		t.Fatalf("extras lost: %+v", s.Nodes[3].Children[0].Extras)
	}
	if s.Config.Theme != "dark" || s.Config.WriteMode != "overwrite" {
		t.Fatalf("config not read: %+v", s.Config)
	}
	if s.cfgExtras["future_key"] != float64(7) {
		t.Fatalf("unknown config key lost: %+v", s.cfgExtras)
	}
	// Saving must preserve the unknown fields and stay loadable.
	if err := s.SaveManifest(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if again.Config.Theme != "dark" || again.cfgExtras["future_key"] != float64(7) {
		t.Fatal("config round-trip lost data")
	}
	if again.Nodes[2].URL != "https://example.test/hosts" {
		t.Fatal("manifest round-trip lost remote URL")
	}
}

func TestEntriesNormalizeLineEndings(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".SwitchHosts")
	s, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Paths.WriteEntry("n1", "a\r\nb\rc"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Paths.ReadEntry("n1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "a\nb\nc" {
		t.Fatalf("entry not LF-normalized: %q", got)
	}
	if _, err := s.Paths.ReadEntry("missing"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "../etc", "a/b", `a\b`, "a..b"} {
		if err := s.Paths.WriteEntry(bad, "x"); err == nil {
			t.Errorf("unsafe id %q accepted", bad)
		}
	}
}

func TestTrashAndHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".SwitchHosts")
	s, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Config.HistoryLimit = 2
	s.Config.HistoryEnabled = true
	if err := s.AddTrash(&model.Node{ID: "n1", Type: model.TypeLocal, Title: "Dev"}, "f1"); err != nil {
		t.Fatal(err)
	}
	if i := s.TrashIndex("n1"); i != 0 {
		t.Fatalf("trash index = %d", i)
	}
	if err := s.RecordChange("old\n", "new\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordChange("new\n", "newer\n"); err != nil {
		t.Fatal(err)
	}
	if len(s.History) != 2 || s.History[0].Content != "new\n" {
		t.Fatalf("history trim wrong: %+v", s.History)
	}
	again, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.History) != 2 || again.TrashIndex("n1") < 0 {
		t.Fatal("trash/history not persisted")
	}
	b, _ := os.ReadFile(again.Paths.HistoryFile)
	if strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
		t.Fatal("history file must be a bare JSON array, not an envelope")
	}
}
