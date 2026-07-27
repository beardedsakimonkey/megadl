package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"megadl/internal/db"
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

func TestShortcutStylesSlashesBetweenKeysAsDim(t *testing.T) {
	got := renderShortcuts(shortcut{
		keys:  []string{"p/space"},
		label: "pause",
	})
	want := styleHelpKey.Render("p") + styleDim.Render("/") +
		styleHelpKey.Render("space") + " " + styleDim.Render("pause")
	if got != want {
		t.Fatalf("shortcut = %q, want %q", got, want)
	}
}

func TestPrimaryStylesUseMegaRed(t *testing.T) {
	if got := styleAccent.GetForeground(); got != colorPrimary {
		t.Fatalf("accent foreground = %v, want %v", got, colorPrimary)
	}
	if got := stylePrimaryText.GetForeground(); got != colorPrimary {
		t.Fatalf("primary text foreground = %v, want %v", got, colorPrimary)
	}
	if got := styleCursor.GetForeground(); got != colorPrimary {
		t.Fatalf("cursor bar foreground = %v, want %v", got, colorPrimary)
	}
	if got := styleModal.GetBorderTopForeground(); got != colorPrimary {
		t.Fatalf("modal border = %v, want %v", got, colorPrimary)
	}
}

// The cursor row's tint has to survive the resets that the row's own
// foreground styles emit, or the band ends at the first colored segment.
func TestArmBackgroundReopensAfterNestedResets(t *testing.T) {
	const open, reset = "<bg>", "<r>"
	got := armBackground("  "+"<fg>✓"+reset+" name"+reset, open, reset)
	want := open + "  <fg>✓" + reset + open + " name" + reset
	if got != want {
		t.Fatalf("armBackground() = %q, want %q", got, want)
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

// queueBarApp is an app over a real database whose queue holds one folder
// download of two waiting files, with partial bytes of the first already on
// disk. Its engine is idle, so the strip has to come from the queue.
func queueBarApp(t *testing.T, partial int64) (*App, *db.DB, []db.File) {
	t.Helper()
	app, database := openAddlinkTestApp(t)
	app.width = 100

	dest := filepath.Join(app.cfg.DownloadDir, "Skins")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := database.InsertDownload(&db.Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "Skins", DestPath: dest,
	}, []db.File{
		{
			NodeHandle: "h1",
			RemotePath: "/Skins/episode-01.mkv",
			LocalPath:  filepath.Join(dest, "episode-01.mkv"),
			Size:       100,
			Queued:     true,
		},
		{
			NodeHandle: "h2",
			RemotePath: "/Skins/episode-02.mkv",
			LocalPath:  filepath.Join(dest, "episode-02.mkv"),
			Size:       100,
			Queued:     true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if partial > 0 {
		tmp := filepath.Join(dest, ".megatmp.h1")
		if err := os.WriteFile(tmp, make([]byte, partial), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := database.Files(id)
	if err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()
	return app, database, files
}

// With nothing being fetched, the strip describes the file the queue will pick
// up next, sitting where its partial on disk left it.
func TestStatusbarShowsQueueHeadWaiting(t *testing.T) {
	app, _, _ := queueBarApp(t, 40)

	got := ansi.Strip(app.statusbarView())
	for _, want := range []string{queuedGlyph + " episode-01.mkv", " 40%", "40 / 100 B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("statusbar = %q, want %q", got, want)
		}
	}
	// nothing is moving, so no rate may be quoted
	if strings.Contains(got, "/s") {
		t.Fatalf("statusbar = %q, want no rate", got)
	}
	if !strings.Contains(app.footerView(), app.statusbarView()) {
		t.Fatal("footer should carry the statusbar")
	}
}

// A held queue is holding at its head file, and the strip wears the same marker
// that file's row in the pane does.
func TestStatusbarMarksHeldQueueHead(t *testing.T) {
	app, _, _ := queueBarApp(t, 40)
	app.eng.SetPaused(true)

	got := ansi.Strip(app.statusbarView())
	if !strings.Contains(got, pausedGlyph+" episode-01.mkv") {
		t.Fatalf("statusbar = %q, want marker %q", got, pausedGlyph)
	}
}

// The strip follows the queue rather than the last thing fetched, so it moves
// on to the next waiting file as soon as one lands.
func TestStatusbarFollowsQueuePastAFinishedFile(t *testing.T) {
	app, database, files := queueBarApp(t, 40)
	if err := database.SetFileStatusByHandle(files[0].DownloadID, "h1", db.FileDone); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()

	got := ansi.Strip(app.statusbarView())
	if !strings.Contains(got, queuedGlyph+" episode-02.mkv") {
		t.Fatalf("statusbar = %q, want the queue's next file", got)
	}
	if strings.Contains(got, "episode-01.mkv") {
		t.Fatalf("statusbar = %q, want the finished file gone", got)
	}
}

// Nothing queued means nothing is going to be fetched, so the footer must not
// keep showing a file — least of all one from a transfer that is over.
func TestStatusbarEmptyWhenNothingIsQueued(t *testing.T) {
	app, database, files := queueBarApp(t, 40)
	if err := database.SetDownloadQueued(files[0].DownloadID, false); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()

	if got := app.statusbarView(); got != "" {
		t.Fatalf("statusbar = %q, want empty with an empty queue", got)
	}
	help := app.helpLine()
	want := strings.Repeat(" ", max(0, (app.width-lipgloss.Width(help))/2)) + help
	if got := app.footerView(); got != want {
		t.Fatalf("footer = %q, want centered help line %q", got, want)
	}
}

// Every file on disk empties the queue too, whether or not the download's own
// row ever recorded an outcome.
func TestStatusbarEmptyOnceEveryFileHasLanded(t *testing.T) {
	app, database, files := queueBarApp(t, 40)
	for _, f := range files {
		if err := database.SetFileStatusByHandle(f.DownloadID, f.NodeHandle, db.FileDone); err != nil {
			t.Fatal(err)
		}
	}
	app.downloads.reload()

	if got := app.statusbarView(); got != "" {
		t.Fatalf("statusbar = %q, want empty once nothing is left to fetch", got)
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

func TestHeaderShowsAmountInLastSixHours(t *testing.T) {
	app := &App{
		width:   100,
		quota6h: 1536 << 20,
	}

	header := app.headerView()
	for _, wanted := range []string{
		styleDim.Render("↓ "),
		quotaStyle(1536 << 20).Bold(true).Render("1.5"),
		styleDim.Render(" GiB"),
	} {
		if !strings.Contains(header, wanted) {
			t.Fatalf("header = %q, want %q", header, wanted)
		}
	}
}

func TestSparklineMeasuresBarsAgainstAFixedCeiling(t *testing.T) {
	const full = 5 << 30
	got := sparkline([]int64{0, 1, full / 2, full - 1, full, 3 * full}, full, styleOK)
	bar := styleOK.Background(colorSparkTrack)
	want := styleSparkTrack.Render(" ") + bar.Render("▁") + bar.Render("▄") +
		bar.Render("▇") + bar.Render("█") + bar.Render("█")
	if got != want {
		t.Fatalf("sparkline = %q, want %q", got, want)
	}

	// An idle window is bare track: color only, no glyph.
	if got, want := sparkline([]int64{0, 0}, full, styleOK),
		styleSparkTrack.Render(" ")+styleSparkTrack.Render(" "); got != want {
		t.Fatalf("idle sparkline = %q, want %q", got, want)
	}
}

// Bars used to be scaled against each other, so the first few hundred MiB of a
// download drew a full-height bar with nothing to compare it to.
func TestSparklineKeepsAModestTransferLow(t *testing.T) {
	got := sparkline([]int64{300 << 20}, sparkFull, styleOK)
	if want := styleOK.Background(colorSparkTrack).Render("▁"); got != want {
		t.Fatalf("0.3 GiB bar = %q, want %q", got, want)
	}
}

func TestHeaderSparklineYieldsToTheTotalWhenNarrow(t *testing.T) {
	app := &App{
		width:   100,
		quota6h: 1536 << 20,
		spark:   []int64{0, 0, 1, 3, 8, 2},
	}

	row := styleDim.Render("6h") + "  " + sparkline(app.spark, sparkFull, quotaStyle(app.quota6h))
	if header := app.headerView(); !strings.Contains(header, row) {
		t.Fatalf("header = %q, want sparkline %q", header, row)
	}

	// Too narrow for both: the row goes, the number stays.
	app.width = 40
	header := app.headerView()
	if strings.Contains(header, row) {
		t.Fatalf("header = %q, sparkline should give way at width %d", header, app.width)
	}
	if !strings.Contains(header, styleDim.Render(" GiB")) {
		t.Fatalf("header = %q, want the quota total kept", header)
	}
	// Every header line still fits the terminal.
	for line := range strings.SplitSeq(header, "\n") {
		if w := lipgloss.Width(line); w > app.width {
			t.Fatalf("header line %q is %d cells wide, want <= %d", line, w, app.width)
		}
	}
}
