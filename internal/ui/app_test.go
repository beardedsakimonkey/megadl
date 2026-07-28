package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"megadl/internal/db"
	"megadl/internal/engine"
	"megadl/internal/mega"
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

	line := statusbarLine(snap, "⠋", 100)
	got := ansi.Strip(line)
	for _, want := range []string{
		"⠋ episode-01.mkv",
		" 25%",
		" 25 / 100 B",
		"2.0 MiB/s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("statusbar = %q, want %q", got, want)
		}
	}
	// Fill and track differ only in color, so the bar has to be matched with
	// its styling intact: 25% of a 20-cell bar is five filled cells.
	wantBar := styleProgress.Render(strings.Repeat("█", 5)) +
		styleProgressTrack.Render(strings.Repeat("█", 15))
	if !strings.Contains(line, wantBar) {
		t.Fatalf("statusbar = %q, want bar %q", line, wantBar)
	}
}

// The rate ramp runs the opposite way to the quota one: fast is the good end.
func TestRateStyleGradesSpeedFromFastToSlow(t *testing.T) {
	for _, tc := range []struct {
		rate float64
		want lipgloss.Style
		name string
	}{
		{9 << 20, styleOK, "fast"},
		{4 << 20, styleOK, "at the green threshold"},
		{(4 << 20) - 1, stylePartial, "just under it"},
		{2 << 20, stylePartial, "healthy"},
		{512 << 10, styleWarn, "slow"},
		{64 << 10, styleError, "crawling"},
		{0, styleError, "stalled"},
	} {
		got := rateStyle(tc.rate)
		if got.GetForeground() != tc.want.GetForeground() {
			t.Fatalf("rateStyle(%s) foreground = %v, want %v",
				tc.name, got.GetForeground(), tc.want.GetForeground())
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
		return strings.IndexAny(ansi.Strip(statusbarLine(snap, "⠋", 100)),
			"█"+strings.Join(eighthBlocks[:], ""))
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

// startedProc is a download that has announced its file and then goes quiet,
// which is the window between a file starting and its transfer reporting how
// many bytes are left to fetch.
type startedProc struct {
	events chan mega.Event
}

func (p *startedProc) Events() <-chan mega.Event { return p.events }
func (p *startedProc) Stop()                     { close(p.events) }

type startedDriver struct{ path string }

func (d startedDriver) List(context.Context, string) ([]mega.Node, error) { return nil, nil }

func (d startedDriver) Start(context.Context, mega.DownloadArgs) (mega.Proc, error) {
	p := &startedProc{events: make(chan mega.Event, 1)}
	p.events <- mega.FileStartEvent{Path: d.path, Size: 100}
	return p, nil
}

// Resuming a partial file must not flash the strip back to 0%: the engine has
// no byte count of its own until the transfer starts, so the strip stays on the
// partial already on disk until the live count passes it.
func TestStatusbarKeepsPartialProgressWhileTransferStarts(t *testing.T) {
	app, database, files := queueBarApp(t, 40)

	app.eng = engine.New(startedDriver{path: files[0].LocalPath}, database)
	go app.eng.Run(t.Context())
	app.eng.Kick()

	deadline := time.Now().Add(2 * time.Second)
	for app.eng.Snapshot().CurrentFile == "" {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the download to start")
		}
		time.Sleep(time.Millisecond)
	}

	got := ansi.Strip(app.statusbarView())
	for _, want := range []string{"episode-01.mkv", " 40%", "40 / 100 B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("statusbar = %q, want %q", got, want)
		}
	}
}

// A held queue is holding at its head file, and the strip wears the same marker
// that file's row in the pane does.
func TestStatusbarMarksHeldQueueHead(t *testing.T) {
	app, _, _ := queueBarApp(t, 40)
	app.eng.SetPaused(true)

	got := app.statusbarView()
	if plain := ansi.Strip(got); !strings.Contains(plain, pausedGlyph+" episode-01.mkv") {
		t.Fatalf("statusbar = %q, want marker %q", plain, pausedGlyph)
	}
	wantBar := styleWarn.Render(strings.Repeat("█", 8)) +
		styleProgressTrack.Render(strings.Repeat("█", 12))
	if !strings.Contains(got, wantBar) {
		t.Fatalf("statusbar = %q, want orange progress bar %q", got, wantBar)
	}
	if detail := ansi.Strip(app.downloads.detailView(app.width)); strings.Contains(detail, "PAUSED") {
		t.Fatalf("detail = %q, want no redundant paused notice above the file strip", detail)
	}
}

func TestPausedNoticeShownWithoutStatusbarFile(t *testing.T) {
	app, database, files := queueBarApp(t, 0)
	if err := database.SetDownloadQueued(files[0].DownloadID, false); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()
	app.eng.SetPaused(true)

	if bar := app.statusbarView(); bar != "" {
		t.Fatalf("statusbar = %q, want empty", bar)
	}
	if detail := ansi.Strip(app.downloads.detailView(app.width)); !strings.Contains(detail, "PAUSED") {
		t.Fatalf("detail = %q, want paused notice when no file strip is present", detail)
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
	// with no notice and no strip, the footer is just its divider and the
	// centered shortcuts
	help := app.helpLine()
	want := app.paneRule("┴") + "\n" +
		strings.Repeat(" ", max(0, (app.width-lipgloss.Width(help))/2)) + help
	if got := app.footerView(); got != want {
		t.Fatalf("footer = %q, want divider over the centered help line %q", got, want)
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
	bar := styleOK.Background(colorTrack)
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
	if want := styleOK.Background(colorTrack).Render("▁"); got != want {
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

// Everything under the panes hangs off the footer's divider — notices included,
// so the panes keep the whole body and nothing floats between them and the rule.
func TestFooterDividerCarriesNoticesAndStrip(t *testing.T) {
	app, _, _ := queueBarApp(t, 40)
	app.height = 20
	app.downloads.notice = "copied url"

	lines := strings.Split(app.footerView(), "\n")
	if got := lines[0]; got != app.paneRule("┴") {
		t.Fatalf("footer starts with %q, want the divider", ansi.Strip(got))
	}
	if len(lines) != 5 {
		t.Fatalf("footer = %q, want divider, notice, strip, divider and help", lines)
	}
	if !strings.Contains(ansi.Strip(lines[1]), "copied url") {
		t.Fatalf("footer line 1 = %q, want the notice", ansi.Strip(lines[1]))
	}
	// the strip is fenced below as well, so the shortcuts sit outside the band
	if got := ansi.Strip(lines[3]); got != strings.Repeat("─", app.width) {
		t.Fatalf("footer line 3 = %q, want the rule closing the strip", got)
	}
	if body := app.downloads.view(app.width, app.bodyHeight); strings.Contains(body, "copied url") {
		t.Fatal("the notice belongs under the divider, not in the panes")
	}

	// the two rules meet the pane gutter at the same column
	header := strings.Split(app.headerView(), "\n")
	if top, bottom := ansi.Strip(header[1]), ansi.Strip(lines[0]); //
	strings.IndexRune(top, '┬') != strings.IndexRune(bottom, '┴') {
		t.Fatalf("junctions misaligned: %q vs %q", top, bottom)
	}
}
