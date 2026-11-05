package hosts

import (
	"testing"
	"unicode/utf16"
)

func TestNormalizeRemovesLeadingBOMsButKeepsInlineOnes(t *testing.T) {
	in := "\ufeff127.0.0.1 a\r\n\r\n \t\ufeff\ufeff::1 b\r# keep \ufeff here\n"
	want := "127.0.0.1 a\n\n \t::1 b\n# keep \ufeff here\n"
	if got := Normalize(in); got != want {
		t.Errorf("Normalize:\n got %q\nwant %q", got, want)
	}
}

func TestDecodeSystemUTF16AndRejectBadInput(t *testing.T) {
	text := "# 中文\r\n127.0.0.1 example.test\r\n"
	if got, err := DecodeSystem([]byte(text)); err != nil || got != text {
		t.Fatalf("utf8: got %q err %v", got, err)
	}
	for _, little := range []bool{true, false} {
		var raw []byte
		if little {
			raw = append(raw, 0xff, 0xfe)
		} else {
			raw = append(raw, 0xfe, 0xff)
		}
		for _, u := range utf16.Encode([]rune(text)) {
			if little {
				raw = append(raw, byte(u), byte(u>>8))
			} else {
				raw = append(raw, byte(u>>8), byte(u))
			}
		}
		got, err := DecodeSystem(raw)
		if err != nil || got != text {
			t.Fatalf("utf16 little=%v: got %q err %v", little, got, err)
		}
	}
	for _, bad := range [][]byte{
		{0xef, 0xbb, 0xbf, 0xff},
		{0xff, 0xfe, 0x00},
		{0xff, 0xfe, 0x00, 0xd8},
		{'a', 0, 'b'},
		{0xff, 0xfe, 0, 0},
	} {
		if _, err := DecodeSystem(bad); err == nil {
			t.Errorf("expected an error for %v", bad)
		}
	}
}

func TestValidateSystemTextRejectsNUL(t *testing.T) {
	if err := ValidateSystemText("ok\n"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSystemText("a\x00b"); err == nil {
		t.Fatal("NUL should be rejected")
	}
}
