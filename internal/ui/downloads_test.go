package ui

import (
	"context"
	"errors"
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

	"megadl/internal/db"
	"megadl/internal/engine"
	"megadl/internal/mega"
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
		Status:     db.StatusPending,
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
		Status:     db.StatusPending,
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

func TestFileMarker(t *testing.T) {
	tests := []struct {
		name     string
		file     db.File
		fetching bool
		paused   bool
		frac     float64
		wantText string
		want     string
	}{
		{name: "not queued, nothing on disk", file: db.File{Status: db.FilePending},
			wantText: emptyGlyph, want: styleDim.Render(emptyGlyph)},
		{name: "not queued, part on disk", file: db.File{Status: db.FilePending}, frac: 0.3,
			wantText: partialGlyph, want: stylePartial.Render(partialGlyph)},
		{name: "queued says so rather than showing progress",
			file: db.File{Queued: true, Status: db.FilePending}, frac: 0.3,
			wantText: queuedGlyph, want: styleDim.Render(queuedGlyph)},
		{name: "queued, nothing on disk yet", file: db.File{Queued: true, Status: db.FilePending},
			wantText: queuedGlyph, want: styleDim.Render(queuedGlyph)},
		{name: "the file a paused queue holds at", file: db.File{Queued: true, Status: db.FilePending},
			paused: true, frac: 0.3, wantText: pausedGlyph, want: styleWarn.Render(pausedGlyph)},
		{name: "error keeps cross despite partial", file: db.File{Status: db.FileError}, frac: 0.3,
			wantText: "✗", want: styleError.Render("✗")},
		{name: "fetching spins", file: db.File{Queued: true, Status: db.FilePending},
			fetching: true, frac: 0.3, wantText: "⠋", want: styleSpinner.Render("⠋")},
		{name: "done keeps check", file: db.File{Queued: true, Status: db.FileDone}, frac: 1,
			wantText: "✓", want: styleOK.Render("✓")},
		{name: "already on disk counts as done", file: db.File{Status: db.FileSkipped},
			wantText: "✓", want: styleOK.Render("✓")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := fileMarkerStateOf(tt.file, tt.fetching, tt.paused, tt.frac)
			if got := fileMarkerText(st, "⠋"); got != tt.wantText {
				t.Fatalf("fileMarkerText() = %q, want %q", got, tt.wantText)
			}
			if got := fileMarker(st, "⠋"); got != tt.want {
				t.Fatalf("fileMarker() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDownloadMarker(t *testing.T) {
	tests := []struct {
		name     string
		state    dlMarkerState
		wantText string
		want     string
	}{
		{name: "fetching now", state: dlMarkerState{active: true, queued: true, head: true},
			wantText: "⠋", want: styleSpinner.Render("⠋")},
		{name: "head of a paused queue",
			state:    dlMarkerState{queued: true, head: true, paused: true, anyBytes: true},
			wantText: pausedGlyph, want: styleWarn.Render(pausedGlyph)},
		{name: "waiting behind the head of a paused queue",
			state:    dlMarkerState{queued: true, paused: true},
			wantText: queuedGlyph, want: styleDim.Render(queuedGlyph)},
		{name: "waiting its turn", state: dlMarkerState{queued: true, anyBytes: true},
			wantText: queuedGlyph, want: styleDim.Render(queuedGlyph)},
		{name: "whole folder on disk", state: dlMarkerState{complete: true, anyBytes: true},
			wantText: "✓", want: styleOK.Render("✓")},
		{name: "part of the folder on disk", state: dlMarkerState{anyBytes: true},
			wantText: partialGlyph, want: stylePartial.Render(partialGlyph)},
		{name: "failed before anything landed", state: dlMarkerState{failed: true},
			wantText: "✗", want: styleError.Render("✗")},
		{name: "failed after part of it landed", state: dlMarkerState{failed: true, anyBytes: true},
			wantText: partialGlyph, want: stylePartial.Render(partialGlyph)},
		{name: "nothing on disk", state: dlMarkerState{},
			wantText: emptyGlyph, want: styleDim.Render(emptyGlyph)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dlMarkerText(tt.state, "⠋"); got != tt.wantText {
				t.Fatalf("dlMarkerText() = %q, want %q", got, tt.wantText)
			}
			if got := dlMarker(tt.state, "⠋"); got != tt.want {
				t.Fatalf("dlMarker() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Both columns budget exactly one cell for the marker, so a glyph that renders
// wider would shift every name in the pane.
func TestMarkersAreOneCellWide(t *testing.T) {
	for _, glyph := range []string{pausedGlyph, queuedGlyph, partialGlyph, emptyGlyph, "✓", "✗"} {
		if w := lipgloss.Width(glyph); w != 1 {
			t.Errorf("glyph %q is %d cells wide, want 1", glyph, w)
		}
	}
	// and every combination of state reaches one of them
	for _, active := range []bool{false, true} {
		for _, paused := range []bool{false, true} {
			for _, head := range []bool{false, true} {
				for _, queued := range []bool{false, true} {
					for _, complete := range []bool{false, true} {
						for _, anyBytes := range []bool{false, true} {
							for _, failed := range []bool{false, true} {
								st := dlMarkerState{active: active, paused: paused,
									head: head, queued: queued, complete: complete,
									anyBytes: anyBytes, failed: failed}
								if got := dlMarkerText(st, "⠋"); lipgloss.Width(got) != 1 {
									t.Errorf("dlMarkerText(%+v) = %q, want one cell", st, got)
								}
							}
						}
					}
				}
			}
		}
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
		{"undownloaded", db.File{ID: 1, LocalPath: "/dl/e1.mkv", Size: 100, Status: db.FilePending, Queued: true},
			strings.Repeat("─", 10), "  0%"},
		{"half fetched", db.File{ID: 2, LocalPath: "/dl/e2.mkv", Size: 100, Status: db.FilePending, Queued: true},
			strings.Repeat("━", 5) + strings.Repeat("─", 5), " 50%"},
		{"done", db.File{ID: 3, LocalPath: "/dl/e3.mkv", Size: 100, Status: db.FileDone, Queued: true},
			strings.Repeat("━", 10), "100%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ansi.Strip(m.fileRowView(tt.file, dl, engine.Snapshot{}, 0, false, 0, 60, 5))
			want := tt.bar + " " + tt.pct
			if !strings.Contains(got, want) {
				t.Fatalf("file row = %q, want progress %q", got, want)
			}
		})
	}
}

func TestFileRowKeepsBarWhenSelected(t *testing.T) {
	m := &downloadsModel{pane: paneFiles, partials: map[int64]int64{2: 50}}
	f := db.File{ID: 2, LocalPath: "/dl/e2.mkv", Size: 100, Status: db.FilePending, Queued: true}

	got := ansi.Strip(m.fileRowView(f, &db.Download{ID: 7}, engine.Snapshot{}, 0, true, 0, 60, 5))
	if !strings.Contains(got, strings.Repeat("━", 5)+strings.Repeat("─", 5)+"  50%") {
		t.Fatalf("selected file row lost its bar: %q", got)
	}
}

func TestFileRowHidesProgressBarAndPercentageTogether(t *testing.T) {
	m := &downloadsModel{partials: map[int64]int64{2: 50}}
	f := db.File{ID: 2, LocalPath: "/dl/episode.mkv", Size: 100, Status: db.FilePending, Queued: true}

	got := ansi.Strip(m.fileRowView(f, &db.Download{ID: 7}, engine.Snapshot{}, 0, false, 0, 25, 5))
	if strings.ContainsAny(got, "━─%") {
		t.Fatalf("narrow file row retained part of its progress display: %q", got)
	}
}

func TestFilesTitlePlacesFolderProgressOnRight(t *testing.T) {
	dl := &db.Download{ID: 7, Name: "Rick and Morty"}
	got := filesTitle(dl, 10, 300, 400, 56)

	if width := lipgloss.Width(got); width != 56 {
		t.Fatalf("title width = %d, want 56: %q", width, got)
	}
	if !strings.Contains(got, "Rick and Morty") || !strings.Contains(got, "10 files") {
		t.Fatalf("title is missing folder details: %q", got)
	}
	if !strings.Contains(got, "████") || !strings.Contains(got, "75%") {
		t.Fatalf("title is missing folder progress: %q", got)
	}
}

func TestFilesTitleKeepsProgressInNarrowPane(t *testing.T) {
	dl := &db.Download{ID: 7, Name: "A very long folder name"}
	got := filesTitle(dl, 20, 50, 100, 24)

	if width := lipgloss.Width(got); width != 24 {
		t.Fatalf("title width = %d, want 24: %q", width, got)
	}
	if !strings.Contains(got, "50%") || !strings.Contains(got, "████") {
		t.Fatalf("narrow title dropped folder progress: %q", got)
	}
}

func TestFilesTitleFallsBackToCountWithoutSizes(t *testing.T) {
	dl := &db.Download{ID: 7, Name: "Rick and Morty"}
	got := filesTitle(dl, 10, 0, 0, 56)

	if !strings.Contains(got, "10 files") {
		t.Fatalf("title is missing file count: %q", got)
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
			{LocalPath: "/dl/Skins/Season 01/Skins - S01E01.mkv", Size: 50, Queued: true},
			{LocalPath: "/dl/Skins/Season 01/Skins - S01E02.mkv", Size: 50, Queued: true},
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
	if !strings.HasPrefix(file, "│ ▌   "+queuedGlyph+" ") {
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
			{Size: 100, Status: db.FileDone, Queued: true},
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

// A download that is not being fetched still shows a half-filled bar on a
// half-fetched file, and counts its on-disk bytes toward the folder total.
func TestFilesViewShowsPartialProgressWhenIdle(t *testing.T) {
	m := &downloadsModel{
		app: &App{eng: engine.New(nil, nil)},
		rows: []*db.Download{{ID: 7, Name: "Show", DestPath: "/dl/Show",
			Status: db.StatusPending, TotalBytes: 100}},
		files: []db.File{
			{ID: 1, LocalPath: "/dl/Show/e1.mkv", Size: 100, Status: db.FilePending, Queued: true},
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
		{LocalPath: done, Status: db.FileDone, Queued: true},
		{LocalPath: skipped, Status: db.FileSkipped, Queued: true},
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
		{LocalPath: filepath.Join(dir, "e4.mkv"), Status: db.FileDone, Queued: true},
		{LocalPath: filepath.Join(dir, "e5.mkv"), Status: db.FileDone, Queued: true},
		{LocalPath: filepath.Join(dir, "e6.mkv"), Status: db.FileDone, Queued: true},
		{LocalPath: filepath.Join(dir, "e7.mkv"), Status: db.FilePending, Queued: true},
		{LocalPath: filepath.Join(dir, "e8.mkv"), Status: db.FileSkipped, Queued: true},
		{LocalPath: filepath.Join(dir, "season.nfo"), Status: db.FileDone, Queued: true},
		{LocalPath: filepath.Join(sub, "bloopers.mkv"), Status: db.FileDone, Queued: true},
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
			Status: db.FilePending, Queued: true},
		{LocalPath: sibling, Status: db.FileDone, Queued: true},
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
		Status: db.FilePending, Queued: true,
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
		Status: db.FilePending, Queued: true,
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
		files: []db.File{{LocalPath: path, Status: db.FileDone, Queued: true}},
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
// queued files, focused on the first row.
func toggleTestApp(t *testing.T) (*App, *db.DB, int64) {
	t.Helper()
	app, database := openAddlinkTestApp(t)
	app.eng = engine.New(nil, database)
	id, err := database.InsertDownload(&db.Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "Folder",
		DestPath: "/dl/Folder",
	}, []db.File{
		{NodeHandle: "a", RemotePath: "/Folder/a", LocalPath: "/dl/Folder/a", Queued: true},
		{NodeHandle: "b", RemotePath: "/Folder/b", LocalPath: "/dl/Folder/b", Queued: true},
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

func queued(t *testing.T, database *db.DB, id int64) bool {
	t.Helper()
	queue, err := database.Queue()
	if err != nil {
		t.Fatal(err)
	}
	return slices.Contains(queue, id)
}

func TestSKeyTogglesDownloadQueueMembership(t *testing.T) {
	app, database, id := toggleTestApp(t)
	m := &app.downloads

	pressKey(m, "s")
	if queued(t, database, id) {
		t.Fatal("download still queued after the first s")
	}
	files, _ := database.Files(id)
	for _, f := range files {
		if f.Queued {
			t.Fatalf("file left queued after its download was: %+v", f)
		}
	}

	m.reload()
	pressKey(m, "s")
	if !queued(t, database, id) {
		t.Fatal("download not queued again after the second s")
	}
}

func TestSKeyOnCompletedDownloadNotices(t *testing.T) {
	app, database, id := toggleTestApp(t)
	// every file on disk, so there is nothing left to queue
	for _, handle := range []string{"a", "b"} {
		if err := database.SetFileStatusByHandle(id, handle, db.FileDone); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.MarkCompleted(id, db.StatusDone); err != nil {
		t.Fatal(err)
	}
	m := &app.downloads
	m.reload()

	pressKey(m, "s")
	if !strings.Contains(m.notice, "already complete") {
		t.Fatalf("notice = %q, want already-complete notice", m.notice)
	}
	if queued(t, database, id) {
		t.Fatal("completed download was put back in the queue")
	}
}

func TestSKeyTogglesFileQueueMembership(t *testing.T) {
	app, database, id := toggleTestApp(t)
	m := &app.downloads
	m.pane = paneFiles
	fileID := m.files[0].ID

	pressKey(m, "s")
	if f, err := database.File(fileID); err != nil || f.Queued {
		t.Fatalf("file after first s = %v (err %v), want out of the queue", f, err)
	}
	// its sibling is still queued, so the download stays in the queue
	if !queued(t, database, id) {
		t.Fatal("download left the queue when only one of its files did")
	}

	pressKey(m, "s")
	if f, err := database.File(fileID); err != nil || !f.Queued {
		t.Fatalf("file after second s = %v (err %v), want queued", f, err)
	}
}

// Queueing a single file is enough to put its download back in the queue,
// without the file pane having to know anything about the download's state.
func TestSKeyOnFileOfUnqueuedDownloadQueuesItAgain(t *testing.T) {
	app, database, id := toggleTestApp(t)
	if err := database.SetDownloadQueued(id, false); err != nil {
		t.Fatal(err)
	}
	m := &app.downloads
	m.reload()
	m.pane = paneFiles
	fileID := m.files[0].ID

	pressKey(m, "s")
	if f, _ := database.File(fileID); !f.Queued {
		t.Fatalf("file = %v, want queued", f)
	}
	if !queued(t, database, id) {
		t.Fatal("download not back in the queue")
	}
}

// Pausing is the user's decision, so adding to the queue must not quietly
// release it — but the notice has to say why nothing started.
func TestQueueingWhilePausedExplainsItself(t *testing.T) {
	app, database, id := toggleTestApp(t)
	if err := database.SetDownloadQueued(id, false); err != nil {
		t.Fatal(err)
	}
	app.eng.SetPaused(true)
	m := &app.downloads
	m.reload()

	pressKey(m, "s")
	if !queued(t, database, id) {
		t.Fatal("download not queued")
	}
	if !app.eng.Paused() {
		t.Fatal("queueing released the pause")
	}
	if !strings.Contains(m.notice, "paused") {
		t.Fatalf("notice = %q, want it to mention the pause", m.notice)
	}
}

// Both pause keys hold and release the queue wherever the cursor happens to be.
func TestPauseKeysTogglePause(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeySpace, Runes: []rune(" ")},
		{Type: tea.KeyRunes, Runes: []rune("p")},
	} {
		t.Run(key.String(), func(t *testing.T) {
			app, database, _ := toggleTestApp(t)

			app.Update(key)
			if !app.eng.Paused() {
				t.Fatalf("%s did not pause the queue", key.String())
			}
			if paused, _, _ := database.Paused(); !paused {
				t.Fatal("pause was not persisted")
			}

			app.Update(key)
			if app.eng.Paused() {
				t.Fatalf("%s did not resume the queue", key.String())
			}
			if paused, _, _ := database.Paused(); paused {
				t.Fatal("resume was not persisted")
			}
		})
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
	if f, _ := database.File(fileID); !f.Queued {
		t.Fatalf("file = %v, want left queued", f)
	}
}

func TestEscDismissesNoticeAndKeepsSelection(t *testing.T) {
	app, _, _ := toggleTestApp(t)
	m := &app.downloads
	m.pane = paneFiles
	m.fileCursor = 1
	m.notice = "something happened"

	m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.notice != "" {
		t.Fatalf("notice = %q, want it dismissed", m.notice)
	}
	if m.pane != paneFiles || m.fileCursor != 1 {
		t.Fatalf("esc moved the selection: pane %v, file cursor %d", m.pane, m.fileCursor)
	}

	// esc with nothing to dismiss still leaves the panes alone; h goes back
	m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.pane != paneFiles {
		t.Fatal("esc left the files pane")
	}
	pressKey(m, "h")
	if m.pane != paneList {
		t.Fatal("h did not leave the files pane")
	}
}

// stalledDriver reports a quota stall on the file it starts, then waits to be
// stopped, so the engine keeps the stall in its snapshot the way a real 509
// does.
type stalledDriver struct{}

func (stalledDriver) List(context.Context, string) ([]mega.Node, error) { return nil, nil }

func (stalledDriver) Start(context.Context, mega.DownloadArgs) (mega.Proc, error) {
	p := &stalledProc{events: make(chan mega.Event, 4), stop: make(chan struct{})}
	go func() {
		p.events <- mega.FileStartEvent{Path: "/dl/Folder/a", Remote: "/Folder/a", Size: 100}
		p.events <- mega.QuotaEvent{Line: "Server returned 509 (over quota)"}
		<-p.stop
		p.events <- mega.ExitEvent{}
		close(p.events)
	}()
	return p, nil
}

type stalledProc struct {
	events   chan mega.Event
	stop     chan struct{}
	stopOnce sync.Once
}

func (p *stalledProc) Events() <-chan mega.Event { return p.events }
func (p *stalledProc) Stop()                     { p.stopOnce.Do(func() { close(p.stop) }) }

func TestEscDismissesQuotaBanner(t *testing.T) {
	app, database, id := toggleTestApp(t)
	app.eng = engine.New(stalledDriver{}, database)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.eng.Run(ctx)
	app.eng.Kick()
	t.Cleanup(func() { app.eng.Dequeue(id) })

	m := &app.downloads
	deadline := time.Now().Add(2 * time.Second)
	for !app.eng.Snapshot().QuotaStalled {
		if time.Now().After(deadline) {
			t.Fatal("engine never reported the quota stall")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.reload()
	if !strings.Contains(m.detailView(80), "QUOTA") {
		t.Fatal("detail strip should show the quota banner while stalled")
	}

	m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(m.detailView(80), "QUOTA") {
		t.Fatal("esc did not dismiss the quota banner")
	}
	// still hidden on later frames, since the engine keeps reporting the stall
	pressKey(m, "j")
	if strings.Contains(m.detailView(80), "QUOTA") {
		t.Fatal("quota banner came back after being dismissed")
	}
}

// pushDriver hands the running proc back to the test, so the test decides when
// the stall it reported ends.
type pushDriver struct{ proc *pushProc }

func (pushDriver) List(context.Context, string) ([]mega.Node, error) { return nil, nil }

func (d pushDriver) Start(context.Context, mega.DownloadArgs) (mega.Proc, error) {
	return d.proc, nil
}

type pushProc struct {
	events   chan mega.Event
	stopOnce sync.Once
}

func (p *pushProc) Events() <-chan mega.Event { return p.events }

// Stop exits from a goroutine: the engine calls it holding its lock, which the
// event pump needs before it can take the exit off the channel.
func (p *pushProc) Stop() {
	p.stopOnce.Do(func() {
		go func() {
			p.events <- mega.ExitEvent{}
			close(p.events)
		}()
	})
}

func waitStall(t *testing.T, eng *engine.Engine, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for eng.Snapshot().QuotaStalled != want {
		if time.Now().After(deadline) {
			t.Fatalf("engine quota stall = %v, want %v", !want, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The banner describes a stall that is happening now, so bytes landing again
// take it off screen — and take the esc dismissal with it, since the next stall
// is news the user has not seen.
func TestQuotaBannerClearsWhenBytesResume(t *testing.T) {
	app, database, id := toggleTestApp(t)
	proc := &pushProc{events: make(chan mega.Event, 8)}
	app.eng = engine.New(pushDriver{proc}, database)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.eng.Run(ctx)
	app.eng.Kick()
	t.Cleanup(func() { app.eng.Dequeue(id) })

	m := &app.downloads
	proc.events <- mega.FileStartEvent{Path: "/dl/Folder/a", Remote: "/Folder/a", Size: 100}
	proc.events <- mega.QuotaEvent{Line: "Server returned 509 (over quota)"}
	waitStall(t, app.eng, true)
	m.update(tea.KeyMsg{Type: tea.KeyEsc})

	proc.events <- mega.ProgressEvent{Done: -1, Total: 100}
	proc.events <- mega.ProgressEvent{Done: 40, Total: 100}
	waitStall(t, app.eng, false)
	m.reload()
	if strings.Contains(m.detailView(80), "QUOTA") {
		t.Fatal("banner is still up after the download resumed")
	}

	proc.events <- mega.QuotaEvent{Line: "Server returned 509 (over quota)"}
	waitStall(t, app.eng, true)
	if !strings.Contains(m.detailView(80), "QUOTA") {
		t.Fatal("a fresh stall stayed hidden behind the earlier dismissal")
	}
}

func TestToggleLabelFollowsSelection(t *testing.T) {
	app, database, id := toggleTestApp(t)
	m := &app.downloads

	if got := m.toggleLabel(); got != "unqueue" {
		t.Fatalf("label for queued download = %q, want %q", got, "unqueue")
	}
	if err := database.SetDownloadQueued(id, false); err != nil {
		t.Fatal(err)
	}
	m.reload()
	if got := m.toggleLabel(); got != "queue" {
		t.Fatalf("label for unqueued download = %q, want %q", got, "queue")
	}

	if err := database.SetDownloadQueued(id, true); err != nil {
		t.Fatal(err)
	}
	m.reload()
	m.pane = paneFiles
	if got := m.toggleLabel(); got != "unqueue" {
		t.Fatalf("label for queued file = %q, want %q", got, "unqueue")
	}
	if err := database.SetFileQueued(m.files[0].ID, false); err != nil {
		t.Fatal(err)
	}
	m.reload()
	if got := m.toggleLabel(); got != "queue" {
		t.Fatalf("label for unqueued file = %q, want %q", got, "queue")
	}
}

func TestPauseLabelFollowsTheQueue(t *testing.T) {
	app, _, _ := toggleTestApp(t)
	m := &app.downloads

	if got := m.pauseLabel(); got != "pause" {
		t.Fatalf("label for a running queue = %q, want %q", got, "pause")
	}
	app.eng.SetPaused(true)
	if got := m.pauseLabel(); got != "resume" {
		t.Fatalf("label for a paused queue = %q, want %q", got, "resume")
	}
}

func TestDownloadsRememberLastSelectedFilePerFolder(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	firstID, err := database.InsertDownload(&db.Download{
		URL: "first", Handle: "first", LinkType: "folder", Name: "First",
		DestPath: "/dl/First",
	}, []db.File{
		{NodeHandle: "a", RemotePath: "/First/a", LocalPath: "/dl/First/a", Queued: true},
		{NodeHandle: "b", RemotePath: "/First/b", LocalPath: "/dl/First/b", Queued: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := database.InsertDownload(&db.Download{
		URL: "second", Handle: "second", LinkType: "folder", Name: "Second",
		DestPath: "/dl/Second",
	}, []db.File{
		{NodeHandle: "c", RemotePath: "/Second/c", LocalPath: "/dl/Second/c", Queued: true},
		{NodeHandle: "d", RemotePath: "/Second/d", LocalPath: "/dl/Second/d", Queued: true},
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
			{NodeHandle: name + "a", RemotePath: "/" + name + "/a", LocalPath: "/dl/" + name + "/a", Queued: true},
			{NodeHandle: name + "b", RemotePath: "/" + name + "/b", LocalPath: "/dl/" + name + "/b", Queued: true},
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
	if next.downloads.pane != paneFiles {
		t.Fatalf("restored pane = %v, want files pane", next.downloads.pane)
	}

	pressKey(&next.downloads, "h") // back to the downloads pane
	last := &App{cfg: app.cfg, db: database}
	last.downloads = newDownloadsModel(last)
	last.downloads.restore()
	if last.downloads.pane != paneList {
		t.Fatalf("restored pane after leaving files = %v, want downloads pane", last.downloads.pane)
	}
}

// The bar fills whole cells, so the percentage next to it has to round the
// same way: a transfer one chunk short must not read "100%" beside a bar with
// an empty cell left in it.
func TestPercentTextNeverReadsCompleteBeforeTheBarFills(t *testing.T) {
	for _, frac := range []float64{0.996, 0.9999} {
		if got := percentText(frac); got != " 99%" {
			t.Fatalf("percentText(%v) = %q, want %q", frac, got, " 99%")
		}
		if bar := ansi.Strip(progressBar(20, frac)); !strings.Contains(bar, "░") {
			t.Fatalf("progressBar(20, %v) = %q, want an unfilled cell", frac, bar)
		}
	}
	if got := percentText(1); got != "100%" {
		t.Fatalf("percentText(1) = %q, want %q", got, "100%")
	}
	if bar := ansi.Strip(progressBar(20, 1)); strings.Contains(bar, "░") {
		t.Fatalf("progressBar(20, 1) = %q, want it full", bar)
	}
}
