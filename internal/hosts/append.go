package hosts

import (
	"strings"
	"unicode"
)

const (
	ContentStartMarker = "# --- SWITCHHOSTS_CONTENT_START ---"
	ContentEndMarker   = "# --- SWITCHHOSTS_CONTENT_END ---"
)

// MakeAppendContent rebuilds the hosts file around the SwitchHosts-managed
// section. Exact port of make_append_content in hosts_apply/write.rs.
//
// Layout: head (untouched) + managed section wrapped in START/END markers +
// tail (untouched). Preserving the tail keeps records other tools appended
// after END alive across re-applies.
//
// When the aggregated content is empty but a tail exists, an empty START/END
// anchor is kept: dropping it would make the next non-empty apply re-create
// the section after the tail, silently reordering entries (hosts resolution
// is first-match-wins).
//
// The seams are normalised so re-applying the same content is byte-identical,
// which lets the caller skip a needless privileged write.
func MakeAppendContent(previousLF, newContentLF string) string {
	head, tail := splitAroundManagedSection(previousLF)
	head = strings.TrimRightFunc(head, unicode.IsSpace)
	tail = strings.TrimSpace(tail)
	content := strings.TrimSpace(stripMarkerLines(newContentLF))

	var parts []string
	if head != "" {
		parts = append(parts, head)
	}
	switch {
	case content != "":
		parts = append(parts, ContentStartMarker+"\n\n"+content+"\n\n"+ContentEndMarker)
	case tail != "":
		parts = append(parts, ContentStartMarker+"\n\n"+ContentEndMarker)
	}
	if tail != "" {
		parts = append(parts, tail)
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// isMarkerLine reports whether a whole line (modulo surrounding whitespace)
// is a section boundary. A marker quoted inside an inline comment is content.
func isMarkerLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == ContentStartMarker || trimmed == ContentEndMarker
}

// splitAroundManagedSection returns (head, tail). END is only searched for
// after START, so a stray END before START stays in the head. When no END
// follows START (pre-END legacy files), the tail is empty — everything from
// START to EOF is SwitchHosts-owned.
func splitAroundManagedSection(previousLF string) (string, string) {
	startBegin, startEnd, ok := findMarkerLine(previousLF, 0, ContentStartMarker)
	if !ok {
		return previousLF, ""
	}
	head := previousLF[:startBegin]
	if _, endEnd, ok := findMarkerLine(previousLF, startEnd, ContentEndMarker); ok {
		return head, previousLF[endEnd:]
	}
	return head, ""
}

// findMarkerLine finds the first line at or after byte offset from whose
// trimmed content equals marker. Returns (lineStart, lineEnd) byte offsets,
// with lineEnd excluding the trailing newline.
func findMarkerLine(text string, from int, marker string) (int, int, bool) {
	if from < 0 || from > len(text) {
		return 0, 0, false
	}
	lineStart := from
	rest := text[from:]
	for rest != "" {
		seg := rest
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			seg = rest[:i+1]
		}
		line := strings.TrimSuffix(seg, "\n")
		if strings.TrimSpace(line) == marker {
			return lineStart, lineStart + len(line), true
		}
		lineStart += len(seg)
		rest = rest[len(seg):]
	}
	return 0, 0, false
}

// stripMarkerLines drops whole marker lines from the aggregated payload;
// left in, they would end the managed section early on the next parse and
// leak the remainder into the preserved tail forever. Lines merely containing
// a marker substring are kept.
func stripMarkerLines(contentLF string) string {
	lines := strings.Split(contentLF, "\n")
	hasMarker := false
	for _, line := range lines {
		if isMarkerLine(line) {
			hasMarker = true
			break
		}
	}
	if !hasMarker {
		return contentLF
	}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if !isMarkerLine(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
