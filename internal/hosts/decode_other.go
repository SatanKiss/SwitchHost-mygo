//go:build !windows

package hosts

import "fmt"

// decodeLegacyEncoding is a no-op outside Windows: on macOS/Linux a system
// hosts file must be UTF-8 (or UTF-16 with a BOM), and anything else is a
// hard error rather than a guessed encoding.
func decodeLegacyEncoding(data []byte) (string, error) {
	return "", fmt.Errorf("invalid UTF-8 hosts file: non-UTF-8 encodings are only decoded on Windows (%d bytes)", len(data))
}
