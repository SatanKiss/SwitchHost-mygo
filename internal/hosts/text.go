// Package hosts implements hosts-content aggregation, normalisation and
// writing to the operating system's hosts file. It is a pure-Go port of
// src-tauri/src/hosts_text.rs, hosts_apply/aggregate.rs and
// hosts_apply/write.rs.
package hosts

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const bom = '\ufeff'

// Normalize removes BOMs before records (including BOMs left by concatenated
// sources), preserving indentation and inline comment text, and normalises
// line endings to LF. Direct port of hosts_text::normalize.
func Normalize(content string) string {
	lf := strings.ReplaceAll(content, "\r\n", "\n")
	lf = strings.ReplaceAll(lf, "\r", "\n")
	var out strings.Builder
	out.Grow(len(lf))
	for i := 0; i < len(lf); {
		end := len(lf)
		if j := strings.IndexByte(lf[i:], '\n'); j >= 0 {
			end = i + j + 1
		}
		line := lf[i:end]
		// Find the leading run of spaces, tabs and BOMs.
		k := 0
		for k < len(line) {
			r, size := utf8.DecodeRuneInString(line[k:])
			if r != ' ' && r != '\t' && r != bom {
				break
			}
			k += size
		}
		for p := 0; p < k; {
			r, size := utf8.DecodeRuneInString(line[p:k])
			if r != bom {
				out.WriteRune(r)
			}
			p += size
		}
		out.WriteString(line[k:])
		i = end
	}
	return out.String()
}

// ValidateSystemText applies the same invariant to incoming content and
// decoded system files, so a successful write can always be read back.
func ValidateSystemText(text string) error {
	if strings.ContainsRune(text, 0) {
		return errors.New("hosts file contains NUL characters")
	}
	return nil
}

// NormalizeLineEndings collapses CRLF / lone CR to LF.
func NormalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// NativeLineEnding is the on-disk line ending for the current platform.
func NativeLineEnding() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// RestoreLineEndings converts LF to the platform-native ending.
func RestoreLineEndings(s string) string {
	if NativeLineEnding() == "\n" {
		return s
	}
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// DecodeSystem decodes a system hosts file without replacement characters.
// A declared Unicode encoding must never fall back to ANSI on malformed
// input, because compensation must be able to restore the original bytes.
func DecodeSystem(data []byte) (string, error) {
	if bytes.HasPrefix(data, []byte{0xff, 0xfe, 0x00, 0x00}) ||
		bytes.HasPrefix(data, []byte{0x00, 0x00, 0xfe, 0xff}) {
		return "", errors.New("UTF-32 hosts files are not supported")
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		little := data[0] == 0xff
		body := data[2:]
		if len(body)%2 != 0 {
			return "", errors.New("incomplete UTF-16 hosts file")
		}
		units := make([]uint16, 0, len(body)/2)
		for i := 0; i < len(body); i += 2 {
			if little {
				units = append(units, uint16(body[i])|uint16(body[i+1])<<8)
			} else {
				units = append(units, uint16(body[i])<<8|uint16(body[i+1]))
			}
		}
		text, err := decodeUTF16Strict(units)
		if err != nil {
			return "", err
		}
		if err := ValidateSystemText(text); err != nil {
			return "", err
		}
		return text, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("hosts file contains NUL bytes")
	}
	if utf8.Valid(data) {
		text := string(data)
		if err := ValidateSystemText(text); err != nil {
			return "", err
		}
		return text, nil
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return "", errors.New("invalid UTF-8 hosts file (BOM present)")
	}
	text, err := decodeLegacyEncoding(data)
	if err != nil {
		return "", err
	}
	if err := ValidateSystemText(text); err != nil {
		return "", err
	}
	return text, nil
}

// decodeUTF16Strict is String::from_utf16: unpaired surrogates are an error,
// not U+FFFD (which utf16.Decode would silently substitute).
func decodeUTF16Strict(units []uint16) (string, error) {
	runes := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		r := rune(units[i])
		switch {
		case r >= 0xd800 && r <= 0xdbff:
			if i+1 >= len(units) {
				return "", fmt.Errorf("unpaired high surrogate at unit %d", i)
			}
			low := rune(units[i+1])
			if low < 0xdc00 || low > 0xdfff {
				return "", fmt.Errorf("unpaired high surrogate at unit %d", i)
			}
			runes = append(runes, utf16.DecodeRune(r, low))
			i++
		case r >= 0xdc00 && r <= 0xdfff:
			return "", fmt.Errorf("unpaired low surrogate at unit %d", i)
		default:
			runes = append(runes, r)
		}
	}
	return string(runes), nil
}
