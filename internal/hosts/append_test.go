package hosts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type appendCase struct {
	Name     string   `json:"name"`
	Previous string   `json:"previous"`
	Applies  []string `json:"applies"`
	Expected string   `json:"expected"`
}

// TestMakeAppendContentMatchesSharedFixture consumes the same behaviour
// contract as src-tauri/src/hosts_apply/write.rs and the Playwright mock,
// so the Rust, JS and Go implementations cannot drift apart silently.
func TestMakeAppendContentMatchesSharedFixture(t *testing.T) {
	path := filepath.Join("..", "..", "test", "fixtures", "hosts_append_cases.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared fixture %s: %v", path, err)
	}
	var doc struct {
		Cases []appendCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			content := c.Previous
			for _, payload := range c.Applies {
				content = MakeAppendContent(content, payload)
			}
			if content != c.Expected {
				t.Errorf("got:\n%q\nwant:\n%q", content, c.Expected)
			}
		})
	}
}

func TestFindMarkerLineIgnoresInlineMentions(t *testing.T) {
	text := "127.0.0.1 a # docs: " + ContentStartMarker + "\n\n" + ContentStartMarker + "\n"
	start, end, ok := findMarkerLine(text, 0, ContentStartMarker)
	if !ok {
		t.Fatal("marker not found")
	}
	if got := text[start:end]; got != ContentStartMarker {
		t.Fatalf("matched %q", got)
	}
	if start < 40 {
		t.Fatalf("inline mention was mistaken for a boundary (offset %d)", start)
	}
}
