//go:build !windows

package i18n

import "os"

// detectOSLocale reads the POSIX locale environment. LC_ALL wins, then the
// message category, then LANG; LANGUAGE holds a colon-separated preference
// list whose first entry is the best match. No child process (no `locale`
// command, no `defaults read`) is started, so this stays pure Go and fast.
func detectOSLocale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := os.Getenv(k); s != "" && s != "C" && s != "POSIX" {
			return s
		}
	}
	if s := os.Getenv("LANGUAGE"); s != "" {
		if i := indexByte(s, ':'); i >= 0 {
			s = s[:i]
		}
		if s != "" && s != "C" {
			return s
		}
	}
	return ""
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
