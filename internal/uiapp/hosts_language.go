package uiapp

import (
	"strings"

	"github.com/egoist/mygo/ui/editor"
)

// HostsLanguage replaces the CodeMirror "hosts" mode of the web build with a
// tokenizer that runs inside MyGo's native code editor. It is pure Go: no
// JavaScript grammar, no webview, no syntax-highlighting subprocess.
//
// A hosts line is `IP hostname [hostname...] [# comment]`. The first field is
// coloured as a constant (an address), following fields as keywords, and
// comments (whole-line or trailing) as comments. Everything else is plain
// text so malformed lines stay readable.
func HostsLanguage() *editor.Language {
	return &editor.Language{
		Name:        "Hosts",
		Extensions:  []string{".hosts"},
		Filenames:   []string{"hosts"},
		LineComment: "#",
		TabSize:     4,
		Tokenizer:   editor.TokenizerFunc(TokenizeHostsLine),
	}
}

// TokenizeHostsLine appends tokens for one line. editor.Token.End is the byte
// offset where that token ends, so the sequence must cover the whole line.
// The returned state is always 0: hosts files have no multi-line constructs.
func TokenizeHostsLine(line string, _ uint64, tokens []editor.Token) ([]editor.Token, uint64) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return append(tokens, editor.Token{End: len(line), Kind: editor.TokenText}), 0
	}
	// SwitchHosts' append-mode markers are comments too, so they must be
	// recognised before the generic comment branch; otherwise the meta
	// colour would never be reachable.
	if strings.Contains(trimmed, "SWITCHHOSTS_CONTENT_") {
		return append(tokens, editor.Token{End: len(line), Kind: editor.TokenMeta}), 0
	}
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") ||
		strings.HasPrefix(trimmed, "//") {
		return append(tokens, editor.Token{End: len(line), Kind: editor.TokenComment}), 0
	}

	field := 0
	i := 0
	for i < len(line) {
		// Whitespace between fields stays plain text.
		start := i
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i > start {
			tokens = append(tokens, editor.Token{End: i, Kind: editor.TokenText})
		}
		if i >= len(line) {
			break
		}

		// A trailing comment runs to the end of the line, including spaces.
		if line[i] == '#' || line[i] == ';' {
			return append(tokens, editor.Token{End: len(line), Kind: editor.TokenComment}), 0
		}

		start = i
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		word := line[start:i]
		kind := editor.TokenKeyword
		if field == 0 {
			kind = editor.TokenConstant
			if !looksLikeAddress(word) {
				kind = editor.TokenInvalid
			}
		} else if strings.HasPrefix(word, "#") {
			kind = editor.TokenComment
		}
		tokens = append(tokens, editor.Token{End: i, Kind: kind})
		field++
	}
	if len(tokens) == 0 || tokens[len(tokens)-1].End != len(line) {
		tokens = append(tokens, editor.Token{End: len(line), Kind: editor.TokenText})
	}
	return tokens, 0
}

// looksLikeAddress is a deliberately loose check so IPv6 literals, IPv4
// literals and unusual-but-legal forms all colour as addresses. Highlighting
// is cosmetic: it never validates or rewrites the file.
func looksLikeAddress(s string) bool {
	if s == "" {
		return false
	}
	if strings.Contains(s, ":") {
		return true // IPv6 (or an IPv6-shaped literal)
	}
	dots := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c == '.':
			dots++
		case c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return dots == 3 || dots == 0
}
