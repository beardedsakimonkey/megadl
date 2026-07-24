package naming

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"Normal Name":            "Normal Name",
		"  padded  ":             "padded",
		"..hidden":               "hidden",
		"a/b\\c":                 "a_b\\c",
		"trailing dots...":       "trailing dots",
		"ctrl\x01chars\x1f":      "ctrlchars",
		"unicode – náme ✓":       "unicode – náme ✓",
		"":                       "",
		"...":                    "",
		strings.Repeat("x", 300): strings.Repeat("x", 240),
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestForNodeFallback(t *testing.T) {
	if got := ForNode("...", "AbCd1234"); got != "download-AbCd1234" {
		t.Errorf("fallback = %q", got)
	}
	if got := ForNode("Fine", "h"); got != "Fine" {
		t.Errorf("got %q", got)
	}
}

func TestEnsureUnique(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "Show"), 0o755)
	os.WriteFile(filepath.Join(dir, "movie.mkv"), nil, 0o644)

	if got := EnsureUnique(dir, "Fresh", nil); got != "Fresh" {
		t.Errorf("fresh = %q", got)
	}
	if got := EnsureUnique(dir, "Show", nil); got != "Show (2)" {
		t.Errorf("dir collision = %q", got)
	}
	if got := EnsureUnique(dir, "movie.mkv", nil); got != "movie (2).mkv" {
		t.Errorf("file collision = %q", got)
	}
	taken := map[string]bool{"Show (2)": true}
	if got := EnsureUnique(dir, "Show", taken); got != "Show (3)" {
		t.Errorf("taken collision = %q", got)
	}
}
