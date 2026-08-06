package ui

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"

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

func TestShortcutStylesFilterKeyAsBoldAndColored(t *testing.T) {
	got := renderShortcuts(shortcut{
		keys:  []string{"/"},
		label: "filter",
	})
	want := styleHelpKey.Render("/") + " " + styleDim.Render("filter")
	if got != want {
		t.Fatalf("shortcut = %q, want %q", got, want)
	}
}

func TestPrimaryStylesUseTheTerminalsRed(t *testing.T) {
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

func TestRenderModalPutsTitleInTopBorder(t *testing.T) {
	got := ansi.Strip(renderModal("Delete download", "some name\nsecond line"))
	lines := strings.Split(got, "\n")

	if want := "╔═ Delete download ═"; !strings.HasPrefix(lines[0], want) {
		t.Fatalf("top border = %q, want prefix %q", lines[0], want)
	}
	if !strings.HasSuffix(lines[0], "═╗") {
		t.Fatalf("top border = %q, want it to close with the corner", lines[0])
	}
	if strings.Contains(strings.Join(lines[1:], "\n"), "Delete download") {
		t.Fatalf("title repeated in the body:\n%s", got)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w != lipgloss.Width(lines[0]) {
			t.Fatalf("line %d width = %d, want %d:\n%s", i, w, lipgloss.Width(lines[0]), got)
		}
	}
}

// A dialog whose body is narrower than its heading still has to show the whole
// heading: the frame grows to the title rather than cropping it.
func TestRenderModalWidensForLongTitle(t *testing.T) {
	const title = "A title much wider than this dialog's body"
	got := ansi.Strip(renderModal(title, "ok"))
	lines := strings.Split(got, "\n")
	if !strings.Contains(lines[0], title) {
		t.Fatalf("top border = %q, want it to carry %q", lines[0], title)
	}
	if !strings.HasSuffix(lines[0], "═╗") {
		t.Fatalf("top border = %q, want a dash before the corner", lines[0])
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

// The app's surfaces are shades of the terminal's own background, so they have
// to move the right way whichever colorscheme they land in: lighter under a
// dark theme, darker under a light one, and in the background's own hue either
// way.
func TestShadeOffStepsAwayFromTheBackground(t *testing.T) {
	lightness := func(hex string) float64 {
		c, err := colorful.Hex(hex)
		if err != nil {
			t.Fatalf("colorful.Hex(%q): %v", hex, err)
		}
		_, _, l := c.Hsl()
		return l
	}
	for _, bg := range []string{"#000000", "#1e1e2e", "#282828"} {
		if got, want := lightness(shadeOff(bg, 0.1, 0.06)), lightness(bg); got <= want {
			t.Fatalf("shadeOff(%s) lightness = %.3f, want above %.3f", bg, got, want)
		}
	}
	for _, bg := range []string{"#ffffff", "#fdf6e3", "#eeeeee"} {
		if got, want := lightness(shadeOff(bg, 0.1, 0.06)), lightness(bg); got >= want {
			t.Fatalf("shadeOff(%s) lightness = %.3f, want below %.3f", bg, got, want)
		}
	}

	// A colored background shades in its own color rather than toward gray: a
	// blue theme's band is blue, a cream one's is cream.
	for _, bg := range []string{"#002b36", "#fdf6e3", "#300a24"} {
		base, _ := colorful.Hex(bg)
		shade, err := colorful.Hex(shadeOff(bg, 0.1, 0.06))
		if err != nil {
			t.Fatalf("shadeOff(%s) is not a color: %v", bg, err)
		}
		wantHue, _, _ := base.Hsl()
		gotHue, _, _ := shade.Hsl()
		if math.Abs(gotHue-wantHue) > 10 {
			t.Fatalf("shadeOff(%s) hue = %.1f, want near %.1f", bg, gotHue, wantHue)
		}
	}
}

// A bar's track has glyphs standing on it, so it sits further off the
// background than the cursor band, which only has to be felt under a row.
func TestTrackSitsFurtherOffTheBackgroundThanTheCursorBand(t *testing.T) {
	for _, bg := range []string{"#000000", "#1e1e2e", "#2e3440", "#ffffff", "#fdf6e3"} {
		base, _ := colorful.Hex(bg)
		band, _ := colorful.Hex(shadeOff(bg, tintDeltaDark, tintDeltaLight))
		track, _ := colorful.Hex(shadeOff(bg, trackDeltaDark, trackDeltaLight))
		if base.DistanceLab(track) <= base.DistanceLab(band) {
			t.Fatalf("on %s the track (%v) is no further off than the band (%v)",
				bg, track.Hex(), band.Hex())
		}
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
		{1 << 20, styleOK, "at the green threshold"},
		{(1 << 20) - 1, styleWarn, "just under it"},
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

func TestStatusbarBarStaysPutAcrossFilesAndNumberChanges(t *testing.T) {
	barAt := func(done, total int64, rate float64) int {
		snap := engine.Snapshot{
			ActiveID:    3,
			CurrentFile: "file.mkv",
			FileSize:    total,
			FileDone:    done,
			Rate:        rate,
		}
		return strings.IndexAny(ansi.Strip(statusbarLine(snap, "⠋", 100)),
			"█"+strings.Join(eighthBlocks[:], ""))
	}

	want := barAt(0, 30<<20, 0)
	for _, tc := range []struct {
		done  int64
		total int64
		rate  float64
	}{
		{512, 30 << 20, 0},                   // sub-unit done, no rate yet
		{5 << 20, 117 << 20, 100 << 10},      // next file has a wider MiB total
		{12 << 20, 2 << 30, 999 << 10},       // next file uses a different unit
		{998, 999, 2 << 20},                  // next file is measured in bytes
		{30 << 20, 30 << 20, 1023<<10 + 512}, // rate crosses the KiB/MiB boundary
	} {
		if got := barAt(tc.done, tc.total, tc.rate); got != want {
			t.Fatalf("bar starts at %d for done=%d total=%d rate=%.0f, want %d",
				got, tc.done, tc.total, tc.rate, want)
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
func (p *startedProc) RetryNow()                 {}

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
	plain := ansi.Strip(got)
	if !strings.Contains(plain, pausedGlyph+" episode-01.mkv") {
		t.Fatalf("statusbar = %q, want marker %q", plain, pausedGlyph)
	}
	if !strings.Contains(plain, "PAUSED") {
		t.Fatalf("statusbar = %q, want PAUSED in the rate column", plain)
	}
	pausedRate := fmt.Sprintf("%*s", len("1023.9 KiB/s"), "PAUSED")
	if !strings.Contains(got, styleWarn.Render(pausedRate)) {
		t.Fatalf("statusbar = %q, want yellow state %q", got, styleWarn.Render(pausedRate))
	}
	// The bar turns the same yellow as the marker.
	wantBar := progressBar(20, 0.4, true)
	if !strings.Contains(got, wantBar) {
		t.Fatalf("statusbar = %q, want the held bar %q", got, wantBar)
	}
	if running := progressBar(20, 0.4, false); running != wantBar &&
		strings.Contains(got, running) {
		t.Fatalf("statusbar = %q, want no green fill in a held bar", got)
	}
	if detail := ansi.Strip(app.downloads.detailView(app.width)); strings.Contains(detail, "PAUSED") {
		t.Fatalf("detail = %q, want no redundant paused notice above the file strip", detail)
	}
}

// pacedProc reports a couple of chunks landing and then goes quiet, the way a
// transfer held mid-file does. Unlike startedProc it exits on the way out —
// the engine only lets go of a download that says it ended — and the buffer
// leaves room for that event, since Stop is called with the engine's lock held
// and a send that blocked would be a send consume could never drain.
type pacedProc struct {
	events chan mega.Event
	once   sync.Once
}

func (p *pacedProc) Events() <-chan mega.Event { return p.events }

func (p *pacedProc) RetryNow() {}

func (p *pacedProc) Stop() {
	p.once.Do(func() {
		p.events <- mega.ExitEvent{}
		close(p.events)
	})
}

type pacedDriver struct{ path string }

func (d pacedDriver) List(context.Context, string) ([]mega.Node, error) { return nil, nil }

func (d pacedDriver) Start(context.Context, mega.DownloadArgs) (mega.Proc, error) {
	p := &pacedProc{events: make(chan mega.Event, 8)}
	p.events <- mega.FileStartEvent{Path: d.path, Size: 100}
	p.events <- mega.ProgressEvent{Done: -1, Total: 60}
	p.events <- mega.ProgressEvent{Done: 20, Total: 60}
	p.events <- mega.ProgressEvent{Done: 40, Total: 60}
	return p, nil
}

// Holding the queue cancels the transfer, but the file it stopped on has just
// as much left to fetch as it did a moment before, so the strip keeps saying
// how long that will take — at the speed the run was managing when it stopped.
func TestStatusbarKeepsTheEstimateWhileHeld(t *testing.T) {
	app, database, files := queueBarApp(t, 40)
	app.eng = engine.New(pacedDriver{path: files[0].LocalPath}, database)
	go app.eng.Run(t.Context())
	app.eng.Kick()

	deadline := time.Now().Add(2 * time.Second)
	for app.eng.Snapshot().AvgRate == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a rate to be measured")
		}
		time.Sleep(time.Millisecond)
	}
	app.eng.SetPaused(true)
	for app.eng.Snapshot().ActiveID != 0 {
		if time.Now().After(deadline) {
			t.Fatal("pause never cancelled the download in flight")
		}
		time.Sleep(time.Millisecond)
	}

	got := ansi.Strip(app.statusbarView())
	if !strings.Contains(got, "PAUSED") {
		t.Fatalf("statusbar = %q, want the held strip", got)
	}
	if !strings.Contains(got, "~") {
		t.Fatalf("statusbar = %q, want the estimate kept while held", got)
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

// An estimate is only as good as the last half minute of transfer, so it drops
// the digits that would tick without meaning anything.
func TestEtaTextDropsUnitsAsTheEstimateGrows(t *testing.T) {
	for _, tc := range []struct {
		left int64
		rate float64
		want string
	}{
		{0, 1 << 20, ""}, // nothing left to fetch
		{1 << 30, 0, ""}, // nothing moving to project from
		{100, 2, "~50s"}, // seconds while there are only seconds
		{45 << 20, 1 << 20, "~45s"},
		{90, 1, "~1m30s"},    // ...and while the minutes are few
		{45 * 60, 1, "~45m"}, // past ten minutes the seconds are noise
		{8 << 30, 3 << 20, "~45m"},
		{2*3600 + 5*60, 1, "~2h05m"},
		{10 * 24 * 3600, 1, "~10d00h"},
		{1 << 40, 1 << 10, ">99d"}, // past projecting usefully
	} {
		got := etaText(tc.left, tc.rate)
		if got != tc.want {
			t.Errorf("etaText(%d, %v) = %q, want %q", tc.left, tc.rate, got, tc.want)
		}
		if len(got) > etaW {
			t.Errorf("etaText(%d, %v) = %q, wider than its %d-cell column",
				tc.left, tc.rate, got, etaW)
		}
	}
}

// A countdown is a number the app knows rather than one it projects, so unlike
// the estimate it keeps its seconds all the way up: they are what is visibly
// ticking.
func TestCountdownTextKeepsItsSeconds(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{-time.Second, "0s"}, // a wait that has run out
		{14 * time.Second, "14s"},
		{13500 * time.Millisecond, "14s"}, // the part-second is still to wait
		{59500 * time.Millisecond, "1m 00s"},
		{3599500 * time.Millisecond, "1h 00m"},
		{3*time.Minute + 42*time.Second, "3m 42s"},
		{59 * time.Minute, "59m 00s"},
		{time.Hour + 9*time.Minute, "1h 09m"},
	} {
		if got := countdownText(tc.d); got != tc.want {
			t.Errorf("countdownText(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestStatusbarProjectsTheCurrentFilesFinish(t *testing.T) {
	snap := engine.Snapshot{
		ActiveID:    3,
		CurrentFile: "episode-01.mkv",
		FileSize:    100,
		FileDone:    25,
		Rate:        2 << 20,
		AvgRate:     1, // 75 bytes left at a byte a second
	}

	if got := ansi.Strip(statusbarLine(snap, "⠋", 100)); !strings.Contains(got, "~1m15s") {
		t.Fatalf("statusbar = %q, want the file's estimate", got)
	}

	// Nothing is moving, so there is nothing to project: an estimate frozen
	// where the bytes stopped would keep promising a finish that isn't coming.
	snap.Rate, snap.AvgRate = 0, 0
	if got := ansi.Strip(statusbarLine(snap, "⠋", 100)); strings.Contains(got, "~") {
		t.Fatalf("statusbar = %q, want no estimate for a stalled transfer", got)
	}
}

// The fields right of the bar give way widest-first, so the narrower the
// terminal the more of the line is the file's name.
func TestStatusbarNarrowDropsTheEstimateBeforeTheRate(t *testing.T) {
	snap := engine.Snapshot{
		ActiveID:    3,
		CurrentFile: "a-very-long-file-name.mkv",
		FileSize:    100,
		FileDone:    50,
		Rate:        1 << 20,
		AvgRate:     1,
	}

	got := ansi.Strip(statusbarLine(snap, "⠋", 80))
	if strings.Contains(got, " / ") || !strings.Contains(got, "~50s") ||
		!strings.Contains(got, "MiB/s") {
		t.Fatalf("statusbar at 80 = %q, want the estimate and rate without byte counts", got)
	}

	got = ansi.Strip(statusbarLine(snap, "⠋", 60))
	if strings.Contains(got, "~") || !strings.Contains(got, "MiB/s") {
		t.Fatalf("statusbar at 60 = %q, want the rate alone", got)
	}
	if w := lipgloss.Width(got); w > 60 {
		t.Fatalf("statusbar width = %d, want <= 60", w)
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
	if got := app.addlink.urlInput.TextStyle.GetForeground(); got != colorYellow {
		t.Fatalf("link hint = %v, want %v", got, colorYellow)
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

// cmdMsgs runs cmd and collects what it produces, flattening batches, so a
// test can look for one message among everything an update asked for.
func cmdMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, cmdMsgs(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// While the dialog is up the mouse belongs to the terminal, so the link in the
// prompt can be hovered, selected and clicked; the app takes it back on close.
func TestAddlinkDialogHandsTheMouseToTheTerminal(t *testing.T) {
	app, _ := openAddlinkTestApp(t)

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	app = model.(*App)
	if app.addlink == nil {
		t.Fatal("\"a\" should open the add-link dialog")
	}
	if !slices.Contains(cmdMsgs(cmd), tea.DisableMouse()) {
		t.Fatal("opening the dialog left mouse reporting on")
	}

	model, cmd = app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	app = model.(*App)
	if app.addlink != nil {
		t.Fatal("esc should close the add-link dialog")
	}
	if !slices.Contains(cmdMsgs(cmd), tea.EnableMouseCellMotion()) {
		t.Fatal("closing the dialog left mouse reporting off")
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
	if len(lines) != 6 {
		t.Fatalf("footer = %q, want divider, notice, divider, strip, divider and help", lines)
	}
	if !strings.Contains(ansi.Strip(lines[1]), "copied url") {
		t.Fatalf("footer line 1 = %q, want the notice", ansi.Strip(lines[1]))
	}
	// the notice sits above the strip's own rule rather than inside its band,
	// and the strip is fenced below as well so the shortcuts sit outside it
	for _, i := range []int{2, 4} {
		if got := ansi.Strip(lines[i]); got != strings.Repeat("─", app.width) {
			t.Fatalf("footer line %d = %q, want a rule fencing the strip", i, got)
		}
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
