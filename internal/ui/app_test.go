package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"megadl/internal/engine"
)

func TestFooterStylesOnlyShortcutKeyAsBrightAndBold(t *testing.T) {
	app := &App{width: 100}

	help := app.helpLine()
	want := styleHelpKey.Render("a") + " " + styleDim.Render("add")
	if !strings.Contains(help, want) {
		t.Fatalf("help line = %q, want styled shortcut %q", help, want)
	}
}

func TestPrimaryStylesUseMegaRed(t *testing.T) {
	if got := styleAccent.GetForeground(); got != colorPrimary {
		t.Fatalf("accent foreground = %v, want %v", got, colorPrimary)
	}
	if got := stylePrimaryText.GetForeground(); got != colorPrimaryText {
		t.Fatalf("primary text foreground = %v, want %v", got, colorPrimaryText)
	}
	if got := styleLogo.GetForeground(); got != colorPrimaryText {
		t.Fatalf("logo foreground = %v, want %v", got, colorPrimaryText)
	}
	if got := styleSelected.GetBackground(); got != colorPrimary {
		t.Fatalf("selected background = %v, want %v", got, colorPrimary)
	}
	if got := styleModal.GetBorderTopForeground(); got != colorPrimary {
		t.Fatalf("modal border = %v, want %v", got, colorPrimary)
	}
}

func TestStatusbarShowsActiveFileProgress(t *testing.T) {
	snap := engine.Snapshot{
		ActiveID:    3,
		CurrentFile: "episode-01.mkv",
		FileSize:    100,
		FileDone:    25,
		Rate:        2 << 20,
	}

	got := ansi.Strip(statusbarLine(snap, "⠋", 100))
	for _, want := range []string{
		"⠋ episode-01.mkv",
		"█████░░░░░░░░░░░░░░░", // 25% of a 20-cell bar
		" 25%",
		" 25 / 100 B",
		"2.0 MiB/s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("statusbar = %q, want %q", got, want)
		}
	}
}

func TestStatusbarBarStaysPutAsNumbersChange(t *testing.T) {
	barAt := func(done int64, rate float64) int {
		snap := engine.Snapshot{
			ActiveID:    3,
			CurrentFile: "file.mkv",
			FileSize:    30 << 20,
			FileDone:    done,
			Rate:        rate,
		}
		return strings.IndexAny(ansi.Strip(statusbarLine(snap, "⠋", 100)), "█░")
	}

	want := barAt(0, 0)
	for _, tc := range []struct {
		done int64
		rate float64
	}{
		{512, 0},                   // sub-unit done, no rate yet
		{5 << 20, 100 << 10},       // KiB/s rate
		{12 << 20, 999 << 10},      // widest KiB/s rate
		{29<<20 + 12345, 2 << 20},  // MiB/s rate
		{30 << 20, 1023<<10 + 512}, // done, rate at the KiB/MiB boundary
	} {
		if got := barAt(tc.done, tc.rate); got != want {
			t.Fatalf("bar starts at %d for done=%d rate=%.0f, want %d",
				got, tc.done, tc.rate, want)
		}
	}
}

func TestStatusbarEmptyWhenIdle(t *testing.T) {
	if got := statusbarLine(engine.Snapshot{}, "⠋", 100); got != "" {
		t.Fatalf("statusbar = %q, want empty", got)
	}
	// active download but between files: nothing is being fetched yet
	if got := statusbarLine(engine.Snapshot{ActiveID: 3}, "⠋", 100); got != "" {
		t.Fatalf("statusbar = %q, want empty", got)
	}
}

func TestStatusbarNarrowDropsByteCounts(t *testing.T) {
	snap := engine.Snapshot{
		ActiveID:    3,
		CurrentFile: "a-very-long-file-name.mkv",
		FileSize:    100,
		FileDone:    50,
		Rate:        1 << 20,
	}

	got := ansi.Strip(statusbarLine(snap, "⠋", 48))
	if strings.Contains(got, " / ") {
		t.Fatalf("statusbar = %q, want byte counts dropped", got)
	}
	if !strings.Contains(got, " 50%") {
		t.Fatalf("statusbar = %q, want percent kept", got)
	}
	if w := lipgloss.Width(got); w > 48 {
		t.Fatalf("statusbar width = %d, want <= 48", w)
	}
}

func TestPasteOpensAddlinkDialogPrefilled(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	link := "https://mega.nz/folder/AAAAAAAA#0123456789abcdefghijkl"

	model, _ := app.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(link),
		Paste: true,
	})
	app = model.(*App)
	if app.addlink == nil {
		t.Fatal("paste should open the add-link dialog")
	}
	if got := app.addlink.urlInput.Value(); got != link {
		t.Fatalf("url input = %q, want %q", got, link)
	}
	if got := app.addlink.urlInput.TextStyle.GetForeground(); got != colorOrange {
		t.Fatalf("link hint = %v, want %v", got, colorOrange)
	}

	// a paste is never a shortcut, even when it is a single "q"
	app, _ = openAddlinkTestApp(t)
	model, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q"), Paste: true})
	app = model.(*App)
	if app.addlink == nil || app.addlink.urlInput.Value() != "q" {
		t.Fatalf("pasted %q should open the dialog, got %+v", "q", app.addlink)
	}
}

func TestCtrlVOpensAddlinkDialogAndReadsClipboard(t *testing.T) {
	app, _ := openAddlinkTestApp(t)

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	app = model.(*App)
	if app.addlink == nil || app.addlink.state != stateURL {
		t.Fatalf("ctrl+v should open the add-link dialog, got %+v", app.addlink)
	}
	// the clipboard read itself is a bubbles command; running it here would
	// touch the real clipboard, so only its presence is checked
	if cmd == nil {
		t.Fatal("ctrl+v should schedule a clipboard read")
	}
}

func TestFooterShowsAmountInLastSixHours(t *testing.T) {
	app := &App{
		width:   100,
		quota6h: 1536 << 20,
	}

	footer := app.footerView()
	for _, wanted := range []string{
		styleDim.Render("↓ "),
		stylePrimaryText.Bold(true).Render("1.5"),
		styleDim.Render(" GiB in last 6h"),
	} {
		if !strings.Contains(footer, wanted) {
			t.Fatalf("footer = %q, want %q", footer, wanted)
		}
	}
}
