package ui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"megadl/internal/db"
	"megadl/internal/engine"
)

func TestProgressBarUsesGreenProgressStyle(t *testing.T) {
	got := progressBar(4, 0.5)
	want := styleProgress.Render("██") + styleDim.Render("░░")
	if got != want {
		t.Fatalf("progressBar() = %q, want %q", got, want)
	}
}

func TestFileProgressBarUsesCenteredGlyphs(t *testing.T) {
	got := fileProgressBar(4, 0.5)
	want := styleProgress.Render("━━") + styleDim.Render("──")
	if got != want {
		t.Fatalf("fileProgressBar() = %q, want %q", got, want)
	}
}

func TestDownloadRowOmitsCreationDate(t *testing.T) {
	m := &downloadsModel{pane: paneList, app: NewApp(nil, nil, nil, nil)}
	dl := &db.Download{
		Name:       "Show",
		Status:     db.StatusQueued,
		TotalBytes: 1024,
		CreatedAt:  time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC),
	}

	got := ansi.Strip(m.rowView(dl, engine.Snapshot{}, false, 80))
	if strings.Contains(got, "2026-07-23") {
		t.Fatalf("download row still contains creation date: %q", got)
	}
	if !strings.Contains(got, "Show") {
		t.Fatalf("download row lost its name: %q", got)
	}
}

func TestDownloadRowOmitsSize(t *testing.T) {
	m := &downloadsModel{pane: paneList, app: NewApp(nil, nil, nil, nil)}
	dl := &db.Download{
		Name:       "Show",
		Status:     db.StatusQueued,
		TotalBytes: 1024,
	}

	got := ansi.Strip(m.rowView(dl, engine.Snapshot{}, false, 80))
	if strings.Contains(got, humanBytes(dl.TotalBytes)) {
		t.Fatalf("download row still contains folder size: %q", got)
	}
	sel := ansi.Strip(m.rowView(dl, engine.Snapshot{}, true, 80))
	if strings.Contains(sel, humanBytes(dl.TotalBytes)) {
		t.Fatalf("selected download row still contains folder size: %q", sel)
	}
}

func TestFileMarkerShowsPartialProgress(t *testing.T) {
	tests := []struct {
		name     string
		file     db.File
		fetching bool
		frac     float64
		wantText string
		want     string
	}{
		{name: "unwanted no partial", file: db.File{Status: db.FilePending}, wantText: "○", want: styleDim.Render("○")},
		{name: "unwanted partial", file: db.File{Status: db.FilePending}, frac: 0.3, wantText: "◔", want: stylePartial.Render("◔")},
		{name: "wanted partial", file: db.File{Wanted: true, Status: db.FilePending}, frac: 0.3, wantText: "◔", want: stylePartial.Render("◔")},
		{name: "wanted no partial", file: db.File{Wanted: true, Status: db.FilePending}, wantText: "·", want: styleDim.Render("·")},
		{name: "error keeps cross despite partial", file: db.File{Wanted: true, Status: db.FileError}, frac: 0.3, wantText: "✗", want: styleError.Render("✗")},
		{name: "fetching spins", file: db.File{Wanted: true, Status: db.FilePending}, fetching: true, frac: 0.3, wantText: "⠋", want: styleSpinner.Render("⠋")},
		{name: "done keeps check", file: db.File{Wanted: true, Status: db.FileDone}, frac: 1, wantText: "✓", want: styleOK.Render("✓")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fileMarkerText(tt.file, tt.fetching, tt.frac, "⠋"); got != tt.wantText {
				t.Fatalf("fileMarkerText() = %q, want %q", got, tt.wantText)
			}
			if got := fileMarker(tt.file, tt.fetching, tt.frac, "⠋"); got != tt.want {
				t.Fatalf("fileMarker() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The list column budgets exactly one cell for the status marker, so a glyph
// that renders wider would shift every name in the pane.
func TestStatusIconIsOneCellWide(t *testing.T) {
	statuses := []string{db.StatusQueued, db.StatusRunning, db.StatusStopped,
		db.StatusDone, db.StatusError, db.StatusQuota, "somethingelse"}
	for _, status := range statuses {
		for _, active := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				got := statusIconText(status, active, partial, "⠋")
				if w := lipgloss.Width(got); w != 1 {
					t.Errorf("statusIconText(%q, active=%v, partial=%v) = %q, width %d, want 1",
						status, active, partial, got, w)
				}
			}
		}
	}
}

// A finished download that only covers part of its folder must not claim the
// whole folder is on disk.
func TestStatusIconMarksPartiallyFinishedDownloads(t *testing.T) {
	if got := statusIconText(db.StatusDone, false, false, "⠋"); got != "✓" {
		t.Errorf("complete download = %q, want ✓", got)
	}
	if got := statusIconText(db.StatusDone, false, true, "⠋"); got != "◔" {
		t.Errorf("partial download = %q, want ◔", got)
	}
	if got := statusIcon(db.StatusDone, false, true, "⠋"); got != stylePartial.Render("◔") {
		t.Errorf("partial download = %q, want it styled as partial", got)
	}
	// the spinner still wins while the engine is on this row
	if got := statusIcon(db.StatusDone, true, true, "⠋"); got != styleSpinner.Render("⠋") {
		t.Errorf("active download = %q, want the spinner frame", got)
	}
}

func TestFileProgress(t *testing.T) {
	tests := []struct {
		name     string
		file     db.File
		snap     engine.Snapshot
		fetching bool
		partial  int64
		want     float64
	}{
		{name: "pending", file: db.File{Size: 100, Status: db.FilePending}, snap: engine.Snapshot{FileDone: 50}, want: 0},
		{name: "active", file: db.File{Size: 100, Status: db.FilePending}, snap: engine.Snapshot{FileDone: 50}, fetching: true, want: 0.5},
		{name: "active clamps low", file: db.File{Size: 100, Status: db.FilePending}, snap: engine.Snapshot{FileDone: -10}, fetching: true, want: 0},
		{name: "active clamps high", file: db.File{Size: 100, Status: db.FilePending}, snap: engine.Snapshot{FileDone: 110}, fetching: true, want: 1},
		{name: "done", file: db.File{Size: 100, Status: db.FileDone}, want: 1},
		{name: "skipped", file: db.File{Size: 100, Status: db.FileSkipped}, want: 1},
		{name: "stopped with partial", file: db.File{Size: 100, Status: db.FilePending}, partial: 30, want: 0.3},
		{name: "errored with partial", file: db.File{Size: 100, Status: db.FileError}, partial: 30, want: 0.3},
		{name: "partial clamps high", file: db.File{Size: 100, Status: db.FilePending}, partial: 150, want: 1},
		{name: "resume holds partial until live count catches up", file: db.File{Size: 100, Status: db.FilePending}, snap: engine.Snapshot{FileDone: 0}, fetching: true, partial: 40, want: 0.4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fileProgress(tt.file, tt.snap, tt.fetching, tt.partial); got != tt.want {
				t.Fatalf("fileProgress() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileNamePadsAndTruncates(t *testing.T) {
	if got := fileName("movie.mkv", 12); got != "movie.mkv   " {
		t.Fatalf("fileName() = %q, want padded name", got)
	}
	long := fileName("a-very-long-name.mkv", 8)
	if lipgloss.Width(long) != 8 || !strings.HasSuffix(long, "…") {
		t.Fatalf("fileName() = %q, want 8-wide truncated name", long)
	}
}

func TestFileBytesRoundsWholeUnitsThroughOneGiB(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want string
	}{
		{name: "bytes remain exact", size: 512, want: "512 B"},
		{name: "KiB rounds", size: 1536, want: "2 KiB"},
		{name: "MiB rounds", size: 50*1024*1024 + 512*1024, want: "51 MiB"},
		{name: "one GiB has no decimal", size: 1024 * 1024 * 1024, want: "1 GiB"},
		{name: "larger than one GiB keeps decimal", size: 1536 * 1024 * 1024, want: "1.5 GiB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fileBytes(tt.size); got != tt.want {
				t.Fatalf("fileBytes(%d) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

// Every file row carries a progress bar column, including files that have
// not downloaded a single byte yet.
func TestFileRowShowsProgressBarColumn(t *testing.T) {
	m := &downloadsModel{partials: map[int64]int64{2: 50}}
	dl := &db.Download{ID: 7}
	tests := []struct {
		name string
		file db.File
		bar  string
		pct  string
	}{
		{"undownloaded", db.File{ID: 1, LocalPath: "/dl/e1.mkv", Size: 100, Status: db.FilePending, Wanted: true},
			strings.Repeat("─", 10), "  0%"},
		{"half fetched", db.File{ID: 2, LocalPath: "/dl/e2.mkv", Size: 100, Status: db.FilePending, Wanted: true},
			strings.Repeat("━", 5) + strings.Repeat("─", 5), " 50%"},
		{"done", db.File{ID: 3, LocalPath: "/dl/e3.mkv", Size: 100, Status: db.FileDone, Wanted: true},
			strings.Repeat("━", 10), "100%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ansi.Strip(m.fileRowView(tt.file, dl, engine.Snapshot{}, false, 0, 60, 5))
			want := tt.bar + " " + tt.pct
			if !strings.Contains(got, want) {
				t.Fatalf("file row = %q, want progress %q", got, want)
			}
		})
	}
}

func TestFileRowKeepsBarWhenSelected(t *testing.T) {
	m := &downloadsModel{pane: paneFiles, partials: map[int64]int64{2: 50}}
	f := db.File{ID: 2, LocalPath: "/dl/e2.mkv", Size: 100, Status: db.FilePending, Wanted: true}

	got := ansi.Strip(m.fileRowView(f, &db.Download{ID: 7}, engine.Snapshot{}, true, 0, 60, 5))
	if !strings.Contains(got, strings.Repeat("━", 5)+strings.Repeat("─", 5)+"  50%") {
		t.Fatalf("selected file row lost its bar: %q", got)
	}
}

func TestFileRowHidesProgressBarAndPercentageTogether(t *testing.T) {
	m := &downloadsModel{partials: map[int64]int64{2: 50}}
	f := db.File{ID: 2, LocalPath: "/dl/episode.mkv", Size: 100, Status: db.FilePending, Wanted: true}

	got := ansi.Strip(m.fileRowView(f, &db.Download{ID: 7}, engine.Snapshot{}, false, 0, 25, 5))
	if strings.ContainsAny(got, "━─%") {
		t.Fatalf("narrow file row retained part of its progress display: %q", got)
	}
}

func TestFilesTitlePlacesFolderProgressOnRight(t *testing.T) {
	dl := &db.Download{ID: 7, Name: "Rick and Morty"}
	got := filesTitle(dl, 3, 10, 300, 400, 56)

	if width := lipgloss.Width(got); width != 56 {
		t.Fatalf("title width = %d, want 56: %q", width, got)
	}
	if !strings.Contains(got, "Rick and Morty") || !strings.Contains(got, "3/10 files") {
		t.Fatalf("title is missing folder details: %q", got)
	}
	if !strings.Contains(got, "████") || !strings.Contains(got, "75%") {
		t.Fatalf("title is missing folder progress: %q", got)
	}
}

func TestFilesTitleKeepsProgressInNarrowPane(t *testing.T) {
	dl := &db.Download{ID: 7, Name: "A very long folder name"}
	got := filesTitle(dl, 1, 20, 50, 100, 24)

	if width := lipgloss.Width(got); width != 24 {
		t.Fatalf("title width = %d, want 24: %q", width, got)
	}
	if !strings.Contains(got, "50%") || !strings.Contains(got, "████") {
		t.Fatalf("narrow title dropped folder progress: %q", got)
	}
}

func TestFilesTitleFallsBackToCountsWithoutSizes(t *testing.T) {
	dl := &db.Download{ID: 7, Name: "Rick and Morty"}
	got := filesTitle(dl, 3, 10, 0, 0, 56)

	if !strings.Contains(got, "3/10 files") {
		t.Fatalf("title is missing file counts: %q", got)
	}
	if strings.Contains(got, "%") {
		t.Fatalf("title shows progress without a known total: %q", got)
	}
}

func TestFileTreeRows(t *testing.T) {
	files := []db.File{
		{LocalPath: "/dl/Show/Season 01/Extras/bloopers.mkv"},
		{LocalPath: "/dl/Show/Season 01/e1.mkv"},
		{LocalPath: "/dl/Show/Season 02/e1.mkv"},
		{LocalPath: "/dl/Show/readme.txt"},
	}
	got := fileTreeRows(files, "/dl/Show")
	want := []fileTreeRow{
		{dir: "Season 01", depth: 0},
		{dir: "Extras", depth: 1},
		{file: 0, depth: 2},
		{file: 1, depth: 1},
		{dir: "Season 02", depth: 0},
		{file: 2, depth: 1},
		{file: 3, depth: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("fileTreeRows() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFilesViewRendersDirectoryTree(t *testing.T) {
	m := &downloadsModel{
		app: &App{eng: engine.New(nil, nil)},
		rows: []*db.Download{{ID: 7, Name: "Skins", DestPath: "/dl/Skins",
			Status: db.StatusDone, TotalBytes: 100}},
		files: []db.File{
			{LocalPath: "/dl/Skins/Season 01/Skins - S01E01.mkv", Size: 50, Wanted: true},
			{LocalPath: "/dl/Skins/Season 01/Skins - S01E02.mkv", Size: 50, Wanted: true},
		},
	}
	got := ansi.Strip(m.filesView(60, 10))

	if strings.Count(got, "Season 01") != 1 {
		t.Fatalf("files view should render one folder header:\n%s", got)
	}
	if strings.Contains(got, "Season 01/Skins") {
		t.Fatalf("files view still renders flat paths:\n%s", got)
	}
	lines := strings.Split(got, "\n")
	var header, file string
	for _, l := range lines {
		if strings.Contains(l, "Season 01") {
			header = l
		}
		if strings.Contains(l, "S01E01") {
			file = l
		}
	}
	if header == "" || file == "" {
		t.Fatalf("missing header or file row:\n%s", got)
	}
	// file rows are indented one level under their folder header; both sit
	// past the pane gutter and the two-cell cursor column, which carries the
	// bar on the cursor row (S01E01 here)
	if !strings.HasPrefix(file, "│ ▌   · ") {
		t.Fatalf("file row is not indented under its folder: %q", file)
	}
	if !strings.HasPrefix(header, "│   Season 01/") {
		t.Fatalf("folder header misrendered: %q", header)
	}
}

// A finished download with a partial selection still measures against the
// whole folder, so the bar reports how much of it exists locally.
func TestFilesViewMeasuresProgressAgainstWholeFolder(t *testing.T) {
	m := &downloadsModel{
		app:  &App{eng: engine.New(nil, nil)},
		rows: []*db.Download{{ID: 7, Name: "Show", Status: db.StatusDone, TotalBytes: 100}},
		files: []db.File{
			{Size: 100, Status: db.FileDone, Wanted: true},
			{Size: 100, Status: db.FilePending},
		},
	}
	got := m.filesView(60, 10)

	if !strings.Contains(got, "50%") {
		t.Fatalf("files view = %q, want whole-folder 50%%", got)
	}
}

func TestPartialSizesStatsUnfinishedFilesOnly(t *testing.T) {
	dir := t.TempDir()
	for handle, size := range map[string]int{"h1": 30, "h2": 100} {
		p := filepath.Join(dir, ".megatmp."+handle)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []db.File{
		{ID: 1, NodeHandle: "h1", LocalPath: filepath.Join(dir, "e1.mkv"), Status: db.FilePending},
		{ID: 2, NodeHandle: "h2", LocalPath: filepath.Join(dir, "e2.mkv"), Status: db.FileDone},
		{ID: 3, NodeHandle: "h3", LocalPath: filepath.Join(dir, "e3.mkv"), Status: db.FilePending},
	}

	got := partialSizes(files)
	want := map[int64]int64{1: 30}
	if len(got) != len(want) || got[1] != want[1] {
		t.Fatalf("partialSizes() = %v, want %v", got, want)
	}
}

// A stopped download still shows a half-filled bar on a half-fetched file
// and counts its on-disk bytes toward the folder total.
func TestFilesViewShowsPartialProgressWhenStopped(t *testing.T) {
	m := &downloadsModel{
		app: &App{eng: engine.New(nil, nil)},
		rows: []*db.Download{{ID: 7, Name: "Show", DestPath: "/dl/Show",
			Status: db.StatusStopped, TotalBytes: 100}},
		files: []db.File{
			{ID: 1, LocalPath: "/dl/Show/e1.mkv", Size: 100, Status: db.FilePending, Wanted: true},
		},
		partials: map[int64]int64{1: 50},
	}
	got := m.filesView(60, 10)

	if !strings.Contains(got, "50%") {
		t.Fatalf("folder title ignores partial bytes:\n%s", got)
	}
	var fileRow string
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "e1") {
			fileRow = l
		}
	}
	if !strings.Contains(ansi.Strip(fileRow), strings.Repeat("━", 5)+strings.Repeat("─", 5)+"  50%") {
		t.Fatalf("file row is missing its half-filled bar: %q", fileRow)
	}
}
func playerModel(t *testing.T, files []db.File) (*downloadsModel, *[]string) {
	t.Helper()
	opened := &[]string{}
	return &downloadsModel{
		pane:  paneFiles,
		files: files,
		openFile: func(paths []string) error {
			*opened = append(*opened, paths...)
			return nil
		},
	}, opened
}

func TestEnterPlaysDownloadedFile(t *testing.T) {
	dir := t.TempDir()
	done := filepath.Join(dir, "e1.mkv")
	skipped := filepath.Join(dir, "e2.mkv")
	for _, p := range []string{done, skipped} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, opened := playerModel(t, []db.File{
		{LocalPath: done, Status: db.FileDone, Wanted: true},
		{LocalPath: skipped, Status: db.FileSkipped, Wanted: true},
	})
	m.fileCursor = 1

	cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a downloaded file returned no command")
	}
	msg := cmd()
	if len(*opened) != 1 || (*opened)[0] != skipped {
		t.Fatalf("opened files = %v, want %q", *opened, skipped)
	}
	m.update(msg)
	if !strings.Contains(m.notice, "playing e2.mkv") {
		t.Fatalf("notice = %q, want playing confirmation", m.notice)
	}
}

func TestEnterQueuesLaterSiblingsAsPlaylist(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "featurettes")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []db.File{
		{LocalPath: filepath.Join(dir, "e4.mkv"), Status: db.FileDone, Wanted: true},
		{LocalPath: filepath.Join(dir, "e5.mkv"), Status: db.FileDone, Wanted: true},
		{LocalPath: filepath.Join(dir, "e6.mkv"), Status: db.FileDone, Wanted: true},
		{LocalPath: filepath.Join(dir, "e7.mkv"), Status: db.FilePending, Wanted: true},
		{LocalPath: filepath.Join(dir, "e8.mkv"), Status: db.FileSkipped, Wanted: true},
		{LocalPath: filepath.Join(dir, "season.nfo"), Status: db.FileDone, Wanted: true},
		{LocalPath: filepath.Join(sub, "bloopers.mkv"), Status: db.FileDone, Wanted: true},
	}
	for _, f := range files {
		if f.Status == db.FilePending {
			continue // e7 has not landed yet
		}
		if err := os.WriteFile(f.LocalPath, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, opened := playerModel(t, files)
	m.fileCursor = 1 // e5

	cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a downloaded file returned no command")
	}
	msg := cmd()
	want := []string{
		filepath.Join(dir, "e5.mkv"),
		filepath.Join(dir, "e6.mkv"),
		filepath.Join(dir, "e8.mkv"),
	}
	if !slices.Equal(*opened, want) {
		t.Fatalf("opened files = %v, want %v", *opened, want)
	}
	m.update(msg)
	if !strings.Contains(m.notice, "playing e5.mkv (+2 queued)") {
		t.Fatalf("notice = %q, want queued-count confirmation", m.notice)
	}
}

func TestEnterOnPartialQueuesNothingUnplayable(t *testing.T) {
	dir := t.TempDir()
	partial := filepath.Join(dir, ".megatmp.h1")
	sibling := filepath.Join(dir, "e2.mkv")
	for _, p := range []string{partial, sibling} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, opened := playerModel(t, []db.File{
		{NodeHandle: "h1", LocalPath: filepath.Join(dir, "e1.mkv"),
			Status: db.FilePending, Wanted: true},
		{LocalPath: sibling, Status: db.FileDone, Wanted: true},
	})

	cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on an incomplete file returned no command")
	}
	cmd()
	want := []string{partial, sibling}
	if !slices.Equal(*opened, want) {
		t.Fatalf("opened files = %v, want %v", *opened, want)
	}
}

func TestEnterPlaysPartialOfIncompleteFile(t *testing.T) {
	dir := t.TempDir()
	partial := filepath.Join(dir, ".megatmp.h1")
	if err := os.WriteFile(partial, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, opened := playerModel(t, []db.File{{
		NodeHandle: "h1", LocalPath: filepath.Join(dir, "e1.mkv"),
		Status: db.FilePending, Wanted: true,
	}})

	cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on an incomplete file returned no command")
	}
	msg := cmd()
	if len(*opened) != 1 || (*opened)[0] != partial {
		t.Fatalf("opened files = %v, want the partial %q", *opened, partial)
	}
	m.update(msg)
	if !strings.Contains(m.notice, "playing e1.mkv (partial)") {
		t.Fatalf("notice = %q, want partial playing confirmation", m.notice)
	}
}

func TestEnterWithNothingOnDiskShowsNotice(t *testing.T) {
	m, opened := playerModel(t, []db.File{{
		NodeHandle: "h1", LocalPath: filepath.Join(t.TempDir(), "e1.mkv"),
		Status: db.FilePending, Wanted: true,
	}})

	cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
	msg := cmd()
	if len(*opened) != 0 {
		t.Fatalf("opened files = %v, want none", *opened)
	}
	m.update(msg)
	if !strings.Contains(m.notice, "not on disk yet") {
		t.Fatalf("notice = %q, want not-on-disk notice", m.notice)
	}
}

func TestEnterReportsPlayerSpawnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e1.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &downloadsModel{
		pane:  paneFiles,
		files: []db.File{{LocalPath: path, Status: db.FileDone, Wanted: true}},
		openFile: func([]string) error {
			return errors.New("mpv executable not found")
		},
	}

	cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a downloaded file returned no command")
	}
	m.update(cmd())
	if !strings.Contains(m.notice, "mpv executable not found") {
		t.Fatalf("notice = %q, want spawn error", m.notice)
	}
}

// toggleTestApp builds an app around one queued folder download with two
// wanted files, focused on the first row.
func toggleTestApp(t *testing.T) (*App, *db.DB, int64) {
	t.Helper()
	app, database := openAddlinkTestApp(t)
	app.eng = engine.New(nil, database)
	id, err := database.InsertDownload(&db.Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "Folder",
		DestPath: "/dl/Folder",
	}, []db.File{
		{NodeHandle: "a", RemotePath: "/Folder/a", LocalPath: "/dl/Folder/a", Wanted: true},
		{NodeHandle: "b", RemotePath: "/Folder/b", LocalPath: "/dl/Folder/b", Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()
	return app, database, id
}

func pressKey(m *downloadsModel, key string) {
	m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

func TestStopKeyTogglesDownloadBetweenStoppedAndQueued(t *testing.T) {
	app, database, id := toggleTestApp(t)
	m := &app.downloads

	pressKey(m, "s")
	if dl, err := database.Download(id); err != nil || dl.Status != db.StatusStopped {
		t.Fatalf("status after first s = %v (err %v), want %q", dl, err, db.StatusStopped)
	}

	m.reload()
	pressKey(m, "s")
	if dl, err := database.Download(id); err != nil || dl.Status != db.StatusQueued {
		t.Fatalf("status after second s = %v (err %v), want %q", dl, err, db.StatusQueued)
	}
}

func TestStopKeyOnCompletedDownloadNotices(t *testing.T) {
	app, database, id := toggleTestApp(t)
	if err := database.MarkCompleted(id, db.StatusDone); err != nil {
		t.Fatal(err)
	}
	m := &app.downloads
	m.reload()

	pressKey(m, "s")
	if !strings.Contains(m.notice, "already complete") {
		t.Fatalf("notice = %q, want already-complete notice", m.notice)
	}
	if dl, _ := database.Download(id); dl.Status != db.StatusDone {
		t.Fatalf("status = %q, want it left alone", dl.Status)
	}
}

func TestStopKeyTogglesFileWantedWhileDownloadRuns(t *testing.T) {
	app, database, id := toggleTestApp(t)
	m := &app.downloads
	m.pane = paneFiles
	fileID := m.files[0].ID

	pressKey(m, "s")
	if f, err := database.File(fileID); err != nil || f.Wanted {
		t.Fatalf("file after first s = %v (err %v), want unwanted", f, err)
	}

	pressKey(m, "s")
	if f, err := database.File(fileID); err != nil || !f.Wanted {
		t.Fatalf("file after second s = %v (err %v), want wanted", f, err)
	}
	if dl, _ := database.Download(id); dl.Status != db.StatusQueued {
		t.Fatalf("download status = %q, want %q", dl.Status, db.StatusQueued)
	}
}

// A wanted file in a stopped download reads as "not started", so s starts the
// download rather than dropping the file from the wanted set.
func TestStopKeyOnFileOfStoppedDownloadStartsIt(t *testing.T) {
	app, database, id := toggleTestApp(t)
	if err := database.SetStatus(id, db.StatusStopped, ""); err != nil {
		t.Fatal(err)
	}
	m := &app.downloads
	m.reload()
	m.pane = paneFiles
	fileID := m.files[0].ID

	pressKey(m, "s")
	if f, _ := database.File(fileID); !f.Wanted {
		t.Fatalf("file = %v, want still wanted", f)
	}
	if dl, _ := database.Download(id); dl.Status != db.StatusQueued {
		t.Fatalf("download status = %q, want %q", dl.Status, db.StatusQueued)
	}
}

func TestStopKeyOnDownloadedFileNotices(t *testing.T) {
	app, database, _ := toggleTestApp(t)
	m := &app.downloads
	m.pane = paneFiles
	if err := database.SetFileStatusByHandle(m.rows[0].ID, "a", db.FileDone); err != nil {
		t.Fatal(err)
	}
	m.reload()
	fileID := m.files[0].ID

	pressKey(m, "s")
	if !strings.Contains(m.notice, "already downloaded") {
		t.Fatalf("notice = %q, want already-downloaded notice", m.notice)
	}
	if f, _ := database.File(fileID); !f.Wanted {
		t.Fatalf("file = %v, want left wanted", f)
	}
}

func TestToggleLabelFollowsSelection(t *testing.T) {
	app, database, id := toggleTestApp(t)
	m := &app.downloads

	if got := m.toggleLabel(); got != "stop" {
		t.Fatalf("label for queued download = %q, want %q", got, "stop")
	}
	if err := database.SetStatus(id, db.StatusStopped, ""); err != nil {
		t.Fatal(err)
	}
	m.reload()
	if got := m.toggleLabel(); got != "start" {
		t.Fatalf("label for stopped download = %q, want %q", got, "start")
	}

	if err := database.SetStatus(id, db.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	m.reload()
	m.pane = paneFiles
	if got := m.toggleLabel(); got != "stop" {
		t.Fatalf("label for wanted file = %q, want %q", got, "stop")
	}
	if err := database.SetFileWanted(m.files[0].ID, false); err != nil {
		t.Fatal(err)
	}
	m.reload()
	if got := m.toggleLabel(); got != "start" {
		t.Fatalf("label for unwanted file = %q, want %q", got, "start")
	}
}

func TestDownloadsRememberLastSelectedFilePerFolder(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	firstID, err := database.InsertDownload(&db.Download{
		URL: "first", Handle: "first", LinkType: "folder", Name: "First",
		DestPath: "/dl/First",
	}, []db.File{
		{NodeHandle: "a", RemotePath: "/First/a", LocalPath: "/dl/First/a", Wanted: true},
		{NodeHandle: "b", RemotePath: "/First/b", LocalPath: "/dl/First/b", Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := database.InsertDownload(&db.Download{
		URL: "second", Handle: "second", LinkType: "folder", Name: "Second",
		DestPath: "/dl/Second",
	}, []db.File{
		{NodeHandle: "c", RemotePath: "/Second/c", LocalPath: "/dl/Second/c", Wanted: true},
		{NodeHandle: "d", RemotePath: "/Second/d", LocalPath: "/dl/Second/d", Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.downloads.reload()
	selectFolder := func(id int64) {
		t.Helper()
		for i, dl := range app.downloads.rows {
			if dl.ID == id {
				app.downloads.cursor = i
				app.downloads.loadFiles()
				return
			}
		}
		t.Fatalf("download %d not found", id)
	}

	selectFolder(firstID)
	app.downloads.fileCursor = 1
	firstFileID := app.downloads.files[1].ID

	selectFolder(secondID)
	app.downloads.fileCursor = 1
	secondFileID := app.downloads.files[1].ID

	selectFolder(firstID)
	if got := app.downloads.files[app.downloads.fileCursor].ID; got != firstFileID {
		t.Fatalf("restored file for first folder = %d, want %d", got, firstFileID)
	}

	if _, err := database.MergeFiles(firstID, []db.File{{
		NodeHandle: "aa", RemotePath: "/First/0", LocalPath: "/dl/First/0",
	}}); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()
	if got := app.downloads.files[app.downloads.fileCursor].ID; got != firstFileID {
		t.Fatalf("selected file after listing reorder = %d, want %d", got, firstFileID)
	}

	selectFolder(secondID)
	if got := app.downloads.files[app.downloads.fileCursor].ID; got != secondFileID {
		t.Fatalf("restored file for second folder = %d, want %d", got, secondFileID)
	}
}

func TestSelectionIsRestoredInANewSession(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	for _, name := range []string{"First", "Second"} {
		if _, err := database.InsertDownload(&db.Download{
			URL: name, Handle: name, LinkType: "folder", Name: name,
			DestPath: "/dl/" + name,
		}, []db.File{
			{NodeHandle: name + "a", RemotePath: "/" + name + "/a", LocalPath: "/dl/" + name + "/a", Wanted: true},
			{NodeHandle: name + "b", RemotePath: "/" + name + "/b", LocalPath: "/dl/" + name + "/b", Wanted: true},
		}); err != nil {
			t.Fatal(err)
		}
	}

	m := &app.downloads
	m.reload()
	pressKey(m, "j") // second row
	pressKey(m, "l") // into its files
	pressKey(m, "j") // second file
	wantDownload, wantFile := m.rows[m.cursor].ID, m.files[m.fileCursor].ID

	// a fresh app over the same database, as after a restart
	next := &App{cfg: app.cfg, db: database}
	next.downloads = newDownloadsModel(next)
	next.downloads.restore()

	if got := next.downloads.rows[next.downloads.cursor].ID; got != wantDownload {
		t.Fatalf("restored download = %d, want %d", got, wantDownload)
	}
	if got := next.downloads.files[next.downloads.fileCursor].ID; got != wantFile {
		t.Fatalf("restored file = %d, want %d", got, wantFile)
	}
}
