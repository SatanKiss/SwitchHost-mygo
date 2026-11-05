package hosts

import (
	"strings"
	"testing"

	"github.com/switchhosts/go-native/internal/model"
)

func TestAggregateSelectedJoinsAndNormalizes(t *testing.T) {
	sources := map[string]string{
		"a": "\ufeff192.0.2.1 a.test\r\n",
		"b": "\ufeff192.0.2.2 a.test b.test\r\n",
	}
	read := func(id string) (string, error) { return sources[id], nil }
	nodes := []*model.Node{
		{ID: "a", Type: model.TypeLocal, On: true},
		{ID: "off", Type: model.TypeLocal, On: false},
		{ID: "b", Type: model.TypeLocal, On: true},
	}
	got, err := AggregateSelected(nodes, read, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(got, '\ufeff') {
		t.Fatal("BOM leaked into aggregated content")
	}
	if !strings.Contains(got, "192.0.2.1 a.test") || !strings.Contains(got, "192.0.2.2 a.test b.test\n") {
		t.Fatalf("unexpected aggregation: %q", got)
	}
	dedup, err := AggregateSelected(nodes, read, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dedup, "192.0.2.2 b.test") {
		t.Fatalf("duplicate domain not removed: %q", dedup)
	}
	if !strings.Contains(dedup, "# invalid hosts (repeated): 192.0.2.2 a.test") {
		t.Fatalf("repeated marker missing: %q", dedup)
	}
}

func TestRemoveDuplicateRecordsIPv4AndIPv6AreIndependent(t *testing.T) {
	in := "127.0.0.1 a.test b.test\n::1 a.test\n127.0.0.1 a.test c.test\n# just a comment\n"
	got := RemoveDuplicateRecords(in, "\n")
	want := "127.0.0.1 a.test b.test\n::1 a.test\n127.0.0.1 c.test\n# invalid hosts (repeated): 127.0.0.1 a.test\n# just a comment\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestRemoveDuplicateRecordsPassesThroughNonRecords(t *testing.T) {
	in := "# comment\n\n# --- SWITCHHOSTS_CONTENT_START ---\n"
	got := RemoveDuplicateRecords(in, "\n")
	if got != in {
		t.Errorf("non-record lines changed: %q", got)
	}
}
