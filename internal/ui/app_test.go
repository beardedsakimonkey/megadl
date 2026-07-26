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
	if got := styleCursor.GetForeground(); got != colorPrimaryText {
		t.Fatalf("cursor bar foreground = %v, want %v", got, colorPrimaryText)
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

// finishedBar and unfinishedBar are the two shapes the held strip is tested
// against: a download whose folder is entirely on disk, and one that only got
// part of the way there.
func finishedBar() *App {
	return heldBarApp(db.StatusDone, db.FileCount{Total: 1, Landed: 1})
}

func unfinishedBar() *App {
	return heldBarApp(db.StatusPending, db.FileCount{Total: 2, Landed: 1})
}

// heldBarApp is an app whose engine is idle but which drew a transfer for
// download 3 a moment ago. counts says how much of that download is on disk,
// which is what the marker on its row — and so on the held strip — comes from.
func heldBarApp(status string, counts db.FileCount) *App {
	app := &App{width: 100, eng: engine.New(nil, nil)}
	app.downloads = newDownloadsModel(app)
	app.downloads.rows = []*db.Download{{ID: 3, Name: "Skins", Status: status, DoneBytes: 100}}
	app.downloads.fileCounts = map[int64]db.FileCount{3: counts}
	app.lastBar = engine.Snapshot{
		ActiveID:    3,
		CurrentFile: "episode-01.mkv",
		FileSize:    100,
		FileDone:    100,
		Rate:        2 << 20,
	}
	return app
}

func TestStatusbarHoldsLastTransferOnceTheEngineGoesIdle(t *testing.T) {
	app := finishedBar()

	got := ansi.Strip(app.statusbarView())
	for _, want := range []string{"✓ episode-01.mkv", "100%", "100 / 100 B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("held statusbar = %q, want %q", got, want)
		}
	}
	// the transfer is over, so the last measured rate must not linger
	if strings.Contains(got, "/s") {
		t.Fatalf("held statusbar = %q, want no rate", got)
	}
	if !strings.Contains(app.footerView(), app.statusbarView()) {
		t.Fatal("footer should carry the held statusbar")
	}
}

// The engine usually finishes between renders, so the frame the strip is
// holding stops a chunk short of the file size. A finished download reads as
// finished anyway: a full bar beside 100%, not 19 of 20 cells beside "100%".
func TestStatusbarCompletesHeldBarForFinishedDownload(t *testing.T) {
	app := finishedBar()
	app.lastBar.FileDone = 9_990
	app.lastBar.FileSize = 10_000

	got := ansi.Strip(app.statusbarView())
	if !strings.Contains(got, "100%") {
		t.Fatalf("held statusbar = %q, want 100%%", got)
	}
	if strings.Contains(got, "░") {
		t.Fatalf("held statusbar = %q, want a full bar", got)
	}
}

// An unfinished download's strip is left where the transfer really stopped.
func TestStatusbarKeepsHeldBarShortForUnfinishedDownload(t *testing.T) {
	app := unfinishedBar()
	app.lastBar.FileDone = 9_990
	app.lastBar.FileSize = 10_000

	got := ansi.Strip(app.statusbarView())
	if !strings.Contains(got, "99%") {
		t.Fatalf("held statusbar = %q, want 99%%", got)
	}
	if !strings.Contains(got, "░") {
		t.Fatalf("held statusbar = %q, want an unfilled cell", got)
	}
}

// An unfinished download keeps its strip too, wearing the same marker its row
// does — here the partial one, since only part of the folder is on disk.
func TestStatusbarHoldsUnfinishedTransfer(t *testing.T) {
	app := unfinishedBar()

	if got := ansi.Strip(app.statusbarView()); !strings.Contains(got, partialGlyph+" episode-01.mkv") {
		t.Fatalf("held statusbar = %q, want marker %q", got, partialGlyph)
	}
}

func TestStatusbarDropsHeldTransferWhenItsDownloadIsRemoved(t *testing.T) {
	app := finishedBar()
	app.downloads.rows = nil

	if got := app.statusbarView(); got != "" {
		t.Fatalf("statusbar = %q, want empty after the download left the list", got)
	}
	if app.lastBar.CurrentFile != "" {
		t.Fatalf("held snapshot = %+v, want cleared", app.lastBar)
	}
}

// barSessionApp is an app over a real database holding one download of a
// single file, as a previous session would have left it: the strip it drew
// last is recorded, and the file is in the given state.
func barSessionApp(t *testing.T, status string, partial int64) (*App, *db.DB, db.File) {
	t.Helper()
	app, database := openAddlinkTestApp(t)
	app.width = 100

	dest := filepath.Join(app.cfg.DownloadDir, "Skins")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := database.InsertDownload(&db.Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "Skins", DestPath: dest,
	}, []db.File{{
		NodeHandle: "h1",
		RemotePath: "/Skins/episode-01.mkv",
		LocalPath:  filepath.Join(dest, "episode-01.mkv"),
		Size:       100,
		Queued:     true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if status != db.FilePending {
		if err := database.SetFileStatusByHandle(id, "h1", status); err != nil {
			t.Fatal(err)
		}
	}
	if status == db.FileDone {
		if err := database.MarkCompleted(id, db.StatusDone); err != nil {
			t.Fatal(err)
		}
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
	if err := database.SetStatusbarFile(files[0].ID); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()
	return app, database, files[0]
}

// Quitting must not empty the footer: the next session opens on the same
// transfer, drawn from the file row rather than from anything it stored.
func TestStatusbarRestoresLastSessionsFinishedTransfer(t *testing.T) {
	app, _, _ := barSessionApp(t, db.FileDone, 0)

	app.restoreBar()

	got := ansi.Strip(app.statusbarView())
	for _, want := range []string{"✓ episode-01.mkv", "100%", "100 / 100 B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("restored statusbar = %q, want %q", got, want)
		}
	}
}

// A transfer that was interrupted comes back where the partial on disk says it
// stopped, not where the last frame happened to be drawn.
func TestStatusbarRestoresUnfinishedTransferFromPartial(t *testing.T) {
	app, _, _ := barSessionApp(t, db.FilePending, 40)

	app.restoreBar()

	got := ansi.Strip(app.statusbarView())
	for _, want := range []string{"episode-01.mkv", "40%", "40 / 100 B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("restored statusbar = %q, want %q", got, want)
		}
	}
}

func TestStatusbarRestoreSkipsRemovedDownload(t *testing.T) {
	app, database, file := barSessionApp(t, db.FileDone, 0)
	if err := database.DeleteDownload(file.DownloadID); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()

	app.restoreBar()

	if app.lastBar.CurrentFile != "" {
		t.Fatalf("restored snapshot = %+v, want nothing to draw", app.lastBar)
	}
	if got := app.statusbarView(); got != "" {
		t.Fatalf("statusbar = %q, want empty", got)
	}
}

// The strip is recorded as the engine reports files, so a session that is
// killed still leaves the last transfer behind. The same file reported again
// must not rewrite the row.
func TestStatusbarRecordsFileAsItIsFetched(t *testing.T) {
	app, database, file := barSessionApp(t, db.FilePending, 0)
	if err := database.SetStatusbarFile(0); err != nil {
		t.Fatal(err)
	}

	snap := engine.Snapshot{
		ActiveID:    file.DownloadID,
		CurrentFile: "episode-01.mkv",
		CurrentPath: file.LocalPath,
		FileSize:    100,
		FileDone:    10,
	}
	app.rememberBar(snap)

	got, err := database.StatusbarFile()
	if err != nil || got != file.ID {
		t.Fatalf("recorded statusbar file = %d, %v, want %d", got, err, file.ID)
	}

	// a second event on the same file is not another write
	if err := database.SetStatusbarFile(0); err != nil {
		t.Fatal(err)
	}
	snap.FileDone = 20
	app.rememberBar(snap)
	if got, err := database.StatusbarFile(); err != nil || got != 0 {
		t.Fatalf("recorded statusbar file = %d, %v, want the write skipped", got, err)
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
