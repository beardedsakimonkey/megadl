package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestOverlayCenterPlacesDialogOverBackground(t *testing.T) {
	bg := strings.TrimSuffix(strings.Repeat("..........\n", 5), "\n")
	got := overlayCenter(bg, "AB\nCD", 10, 5)

	want := strings.Join([]string{
		"..........",
		"....AB....",
		"....CD....",
		"..........",
		"..........",
	}, "\n")
	if stripped := ansi.Strip(got); stripped != want {
		t.Fatalf("overlay mismatch:\n got:\n%s\nwant:\n%s", stripped, want)
	}
}

func TestOverlayCenterPadsShortBackgroundLines(t *testing.T) {
	got := overlayCenter("ab\ncd", "XX", 8, 3)

	want := strings.Join([]string{
		"ab",
		"cd " + "XX" + "   ",
		"",
	}, "\n")
	if stripped := ansi.Strip(got); stripped != want {
		t.Fatalf("overlay mismatch:\n got:\n%q\nwant:\n%q", stripped, want)
	}
}

func TestOverlayCenterKeepsStyledBackgroundIntact(t *testing.T) {
	row := lipgloss.NewStyle().Background(lipgloss.Color("1")).Render(strings.Repeat("x", 12))
	bg := strings.Join([]string{row, row, row}, "\n")
	got := overlayCenter(bg, "OK", 12, 3)

	want := strings.Join([]string{
		strings.Repeat("x", 12),
		"xxxxxOKxxxxx",
		strings.Repeat("x", 12),
	}, "\n")
	if stripped := ansi.Strip(got); stripped != want {
		t.Fatalf("overlay mismatch:\n got:\n%q\nwant:\n%q", stripped, want)
	}
	for i, line := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(line); w != 12 {
			t.Fatalf("line %d width = %d, want 12", i, w)
		}
	}
}

func TestOverlayCenterCropsOversizedDialog(t *testing.T) {
	bg := "....\n....\n...."
	got := overlayCenter(bg, "123456\nabcdef\nABCDEF\nZZZZZZ", 4, 3)

	for i, line := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(line); w != 4 {
			t.Fatalf("line %d width = %d, want 4", i, w)
		}
	}
	if lines := strings.Count(got, "\n") + 1; lines != 3 {
		t.Fatalf("got %d lines, want 3", lines)
	}
}
