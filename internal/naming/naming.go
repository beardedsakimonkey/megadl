// Package naming derives safe local names from remote mega node names.
package naming

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sanitize turns a remote node name into a safe single path component.
func Sanitize(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == 0:
			b.WriteRune('_')
		case r < 0x20 || r == 0x7f:
			// drop control chars
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	s = strings.TrimLeft(s, ".")
	s = strings.TrimRight(s, ". ")
	if len(s) > 240 {
		s = s[:240]
	}
	return strings.TrimSpace(s)
}

// ForNode returns a sanitized name with a fallback derived from the link handle.
func ForNode(remoteName, handle string) string {
	if s := Sanitize(remoteName); s != "" {
		return s
	}
	return "download-" + handle
}

// EnsureUnique appends " (2)", " (3)", ... while name exists inside dir
// or is claimed by taken (names of pending downloads).
func EnsureUnique(dir, name string, taken map[string]bool) string {
	candidate := name
	for i := 2; ; i++ {
		if !taken[candidate] {
			if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
				return candidate
			}
		}
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}
