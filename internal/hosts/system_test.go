package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func writeHosts(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(RestoreLineEndings(content)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readHosts(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyAtAppendIsByteIdenticalOnSecondApply(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hosts")
	backups := filepath.Join(dir, "backups")
	initial := "127.0.0.1 localhost\n255.255.255.255 broadcasthost\n"
	writeHosts(t, target, initial)

	first, err := ApplyAt(target, "1.2.3.4 a.com\n", "append", backups)
	if err != nil {
		t.Fatal(err)
	}
	if first.Unchanged {
		t.Fatal("first apply should write")
	}
	want := RestoreLineEndings(MakeAppendContent(Normalize(initial), "1.2.3.4 a.com\n"))
	if got := readHosts(t, target); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}

	second, err := ApplyAt(target, "1.2.3.4 a.com\n", "append", backups)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Unchanged {
		t.Fatal("re-applying identical content must short-circuit")
	}
}

func TestApplyAtAppendPreservesTailAndSupportsRestore(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hosts")
	backups := filepath.Join(dir, "backups")
	old := "127.0.0.1 localhost\n" + ContentStartMarker + "\n10.0.0.2 old.test\n" +
		ContentEndMarker + "\n# user-managed tail\n"
	writeHosts(t, target, old)
	before := readHosts(t, target)

	out, err := ApplyAt(target, "10.0.0.1 dev.test\n", "append", backups)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.NewContent, "dev.test") || !strings.Contains(out.NewContent, "# user-managed tail") {
		t.Fatalf("tail lost: %q", out.NewContent)
	}
	if err := RestoreAt(target, out.PreviousContent, out.NewContent, out.PreviousBytes); err != nil {
		t.Fatal(err)
	}
	if got := readHosts(t, target); got != before {
		t.Fatalf("restore mismatch:\n got %q\nwant %q", got, before)
	}

	// A later external write must survive a delayed compensation attempt.
	writeHosts(t, target, "newer external content\n")
	err = RestoreAt(target, out.PreviousContent, out.NewContent, out.PreviousBytes)
	if !errors.Is(err, ErrContentChanged) {
		t.Fatalf("expected ErrContentChanged, got %v", err)
	}
	if got := readHosts(t, target); got != RestoreLineEndings("newer external content\n") {
		t.Fatalf("external content was overwritten: %q", got)
	}
}

func TestApplyAtOverwriteReplacesWholeFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hosts")
	writeHosts(t, target, "127.0.0.1 localhost\n"+ContentStartMarker+"\nold\n"+ContentEndMarker+"\n")
	out, err := ApplyAt(target, "1.2.3.4 a.com\n", "overwrite", filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Unchanged {
		t.Fatal("expected a write")
	}
	if got := readHosts(t, target); got != RestoreLineEndings("1.2.3.4 a.com\n") {
		t.Fatalf("overwrite produced %q", got)
	}
}

func TestApplyAtBacksUpLegacyEncodingBeforeConversion(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hosts")
	backups := filepath.Join(dir, "backups")
	text := "# 中文\r\n127.0.0.1 localhost\r\n"
	var raw = []byte{0xff, 0xfe}
	for _, u := range utf16.Encode([]rune(text)) {
		raw = append(raw, byte(u), byte(u>>8))
	}
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAt(target, "1.2.3.4 a.com\n", "overwrite", backups); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(backups)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one encoding backup, got %v (%v)", entries, err)
	}
	backup, err := os.ReadFile(filepath.Join(backups, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(raw) {
		t.Fatal("backup is not byte-identical to the original UTF-16 file")
	}
}
