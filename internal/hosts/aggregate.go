package hosts

import (
	"strings"

	"github.com/switchhosts/go-native/internal/model"
)

// AggregateSelected builds the content that gets written to the system hosts
// file. Direct port of hosts_apply/aggregate.rs (which ports
// src/main/actions/list/getContentOfList.ts):
//
//  1. Recursively flatten the manifest tree.
//  2. Keep nodes whose On flag is true.
//  3. For each, read entries/<id>.hosts (system/folder/group nodes contribute
//     empty content because they have no entry file).
//  4. Join with "\n\n".
//  5. Optionally run the duplicate-record pass from src/common/normalize.ts.
//
// readEntry is injected so that tests and the service layer can supply a
// different source without touching disk.
func AggregateSelected(nodes []*model.Node, readEntry func(id string) (string, error), dedup bool) (string, error) {
	var chunks []string
	if err := collectSelected(nodes, readEntry, &chunks); err != nil {
		return "", err
	}
	content := strings.Join(chunks, "\n\n")
	if dedup {
		content = RemoveDuplicateRecords(content, NativeLineEnding())
	}
	return content, nil
}

func collectSelected(nodes []*model.Node, readEntry func(string) (string, error), out *[]string) error {
	for _, n := range nodes {
		if n.On && n.ID != "" {
			content, err := readEntry(n.ID)
			if err != nil {
				return err
			}
			// Keep source text intact for the editor; remove BOMs only from
			// the content being aggregated for application.
			*out = append(*out, Normalize(content))
		}
		if len(n.Children) > 0 {
			if err := collectSelected(n.Children, readEntry, out); err != nil {
				return err
			}
		}
	}
	return nil
}

type parsedLine struct {
	ip      string
	domains []string
	comment string
}

func parseLine(line string) parsedLine {
	before, after, found := strings.Cut(line, "#")
	comment := ""
	if found {
		comment = strings.TrimSpace(after)
	}
	normalized := strings.Join(strings.Fields(before), " ")
	parts := strings.Fields(normalized)
	p := parsedLine{comment: comment}
	if len(parts) > 0 {
		p.ip = parts[0]
		p.domains = parts[1:]
	}
	return p
}

func formatLine(ip string, domains []string, comment string) string {
	parts := []string{ip, strings.Join(domains, " ")}
	if comment != "" {
		parts = append(parts, "# "+comment)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// RemoveDuplicateRecords is a direct port of removeDuplicateRecords in
// src/common/normalize.ts:
//
//   - Lines that don't parse into ip + at least one domain pass through.
//   - IPv4 and IPv6 addresses occupy independent namespaces.
//   - Duplicate domain lines get an "# invalid hosts (repeated): ..." marker
//     line appended right after the surviving line.
//   - Output line endings follow the current platform (os.EOL in Node).
func RemoveDuplicateRecords(content, lineEnding string) string {
	seen := make(map[string]bool)
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		p := parseLine(line)
		if p.ip == "" || len(p.domains) == 0 {
			out = append(out, line)
			continue
		}
		version := "4"
		if strings.Contains(p.ip, ":") {
			version = "6"
		}
		var fresh, repeated []string
		for _, domain := range p.domains {
			key := domain + "_" + version
			if seen[key] {
				repeated = append(repeated, domain)
			} else {
				seen[key] = true
				fresh = append(fresh, domain)
			}
		}
		if len(fresh) > 0 {
			out = append(out, formatLine(p.ip, fresh, p.comment))
		}
		if len(repeated) > 0 {
			inner := formatLine(p.ip, repeated, "")
			out = append(out, "# invalid hosts (repeated): "+inner)
		}
	}
	return strings.Join(out, lineEnding)
}
