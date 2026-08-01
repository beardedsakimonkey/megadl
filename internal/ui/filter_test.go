package ui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"megadl/internal/db"
)

// filterTestApp builds an app around four downloads whose names cover what the
// filter has to tell apart: a word shared by three of them, in two different
// cases. Rows come back newest first, so they are inserted in reverse and the
// fixture checks the order it promises.
func filterTestApp(t *testing.T) (*App, *db.DB) {
	t.Helper()
	app, database := openAddlinkTestApp(t)
	names := []string{"Planet Earth", "planet of the apes", "Blue Planet II", "Cosmos"}
	for i := len(names) - 1; i >= 0; i-- {
		_, err := database.InsertDownload(&db.Download{
			URL: "u" + names[i], Handle: "h" + names[i], LinkType: "folder",
			Name: names[i], DestPath: filepath.Join(app.cfg.DownloadDir, names[i]),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	app.downloads.reload()
	if got := rowNames(app.downloads.rows); !slices.Equal(got, names) {
		t.Fatalf("library = %q, want %q", got, names)
	}
	return app, database
}

func rowNames(rows []*db.Download) []string {
	out := make([]string, len(rows))
	for i, dl := range rows {
		out[i] = dl.Name
	}
	return out
}

// typeFilter sends text to the open prompt one key at a time, the way it is
// actually typed: the list is re-narrowed after every one of them.
func typeFilter(m *downloadsModel, text string) {
	for _, r := range text {
		m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func TestFilterMatchesSubstringsWithSmartCase(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		want        bool
	}{
		{"Planet Earth", "", true},       // an empty query keeps everything
		{"Planet Earth", "planet", true}, // all lower case: case is ignored
		{"planet of the apes", "planet", true},
		{"Planet Earth", "Planet", true},
		{"planet of the apes", "Planet", false}, // one capital: case matters
		{"Planet Earth", "PLANET", false},
		{"Blue Planet II", "net I", true}, // substrings, spaces and all
		{"Blue Planet II", "bpii", false}, // nothing fuzzy about it
		{"Cosmos", "planet", false},
	} {
		if got := matchesQuery(tc.name, tc.query); got != tc.want {
			t.Errorf("matchesQuery(%q, %q) = %v, want %v", tc.name, tc.query, got, tc.want)
		}
	}
}

// A row on screen has to say why it is there, so every run of the name the
// query picked out is marked — under the same smart case that decided the row
// survived at all.
func TestFilterMarksEveryRunThatMatched(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		want        []string // the marked runs, in the order they are drawn
		at          []int    // where each of them starts
	}{
		{name: "Planet Earth", query: "", want: nil, at: nil},
		{name: "Planet Earth", query: "lan", want: []string{"lan"}, at: []int{1}},
		{name: "Planet Earth", query: "planet", want: []string{"Planet"}, at: []int{0}},
		{name: "Blue Planet II", query: "l", want: []string{"l", "l"}, at: []int{1, 6}},
		// a repeat is marked as many times as it appears, without overlapping
		{name: "aaaa", query: "aa", want: []string{"aa", "aa"}, at: []int{0, 2}},
		// one capital and only the exact case is a match, so only it is marked
		{name: "Planet of the planets", query: "Planet", want: []string{"Planet"}, at: []int{0}},
		{name: "Cosmos", query: "planet", want: nil, at: nil},
		// offsets are bytes into the name, which multi-byte runes shift
		{name: "Naïve planet", query: "planet", want: []string{"planet"}, at: []int{7}},
		{name: "Naïve planet", query: "ïve", want: []string{"ïve"}, at: []int{2}},
	} {
		ranges := matchRanges(tc.name, tc.query)
		var got []string
		var at []int
		for _, r := range ranges {
			got = append(got, tc.name[r[0]:r[1]])
			at = append(at, r[0])
		}
		if !slices.Equal(got, tc.want) || !slices.Equal(at, tc.at) {
			t.Errorf("matchRanges(%q, %q) marked %q at %v, want %q at %v",
				tc.name, tc.query, got, at, tc.want, tc.at)
		}
		// The marks are styling laid over the name, never an edit to it: what
		// the row draws has to be the name it would have drawn regardless.
		if got := ansi.Strip(highlightQuery(tc.name, tc.query)); got != tc.name {
			t.Errorf("highlightQuery(%q, %q) drew %q", tc.name, tc.query, got)
		}
	}
}

// The name is cut to the column before it is marked, so a match past the cut
// can't leave the highlight pointing at bytes the pane never drew.
func TestFilterMarksTheNameAsTruncated(t *testing.T) {
	const name = "Planet Earth and the planets beyond"
	drawn := truncate(name, 12)
	ranges := matchRanges(drawn, "planet")
	if len(ranges) != 1 || drawn[ranges[0][0]:ranges[0][1]] != "Planet" {
		t.Fatalf("marks on %q = %v, want the one match still on screen", drawn, ranges)
	}
}

func TestSlashNarrowsTheListAsItIsTyped(t *testing.T) {
	app, _ := filterTestApp(t)
	m := &app.downloads

	pressKey(m, "/")
	if !m.filtering() {
		t.Fatal("/ did not open the filter prompt")
	}
	typeFilter(m, "planet")

	want := []string{"Planet Earth", "planet of the apes", "Blue Planet II"}
	if got := rowNames(m.rows); !slices.Equal(got, want) {
		t.Fatalf("filtered list = %q, want %q", got, want)
	}
	if len(m.all) != 4 {
		t.Fatalf("library = %d rows, want the filter to leave all 4 alone", len(m.all))
	}

	// one capital and the match turns strict
	m.update(tea.KeyMsg{Type: tea.KeyEsc})
	pressKey(m, "/")
	typeFilter(m, "Planet")
	want = []string{"Planet Earth", "Blue Planet II"}
	if got := rowNames(m.rows); !slices.Equal(got, want) {
		t.Fatalf("case-sensitive list = %q, want %q", got, want)
	}
}

// The pane says what it is showing and how much of the library that is, so a
// narrowed list is never mistaken for a short one.
func TestFilterPromptShowsQueryAndMatchCount(t *testing.T) {
	app, _ := filterTestApp(t)
	m := &app.downloads

	pressKey(m, "/")
	typeFilter(m, "planet")
	out := ansi.Strip(m.view(60, 8))
	first, _, _ := strings.Cut(out, "\n")
	if !strings.Contains(first, "/planet") {
		t.Fatalf("first pane row = %q, want the query in it", first)
	}
	if !strings.Contains(first, "3/4") {
		t.Fatalf("first pane row = %q, want a 3/4 match count", first)
	}
	if !strings.Contains(out, "Planet Earth") || strings.Contains(out, "Cosmos") {
		t.Fatalf("pane = %q, want only the matches drawn", out)
	}
}

func TestFilterWithNoMatchesSaysSoAndKeepsThePromptUp(t *testing.T) {
	app, _ := filterTestApp(t)
	m := &app.downloads

	pressKey(m, "/")
	typeFilter(m, "zzz")
	out := ansi.Strip(m.view(60, 8))
	if !strings.Contains(out, "/zzz") || !strings.Contains(out, "0/4") {
		t.Fatalf("pane = %q, want the prompt and a zero match count", out)
	}
	if !strings.Contains(out, "no matches") {
		t.Fatalf("pane = %q, want it to say nothing matched", out)
	}
	if strings.Contains(out, "no downloads yet") {
		t.Fatal("an empty result read as an empty library")
	}
}

// Typing another character must not shuffle the selection: the row under the
// cursor keeps it for as long as it survives the query.
func TestFilterKeepsTheCursorOnItsRow(t *testing.T) {
	app, _ := filterTestApp(t)
	m := &app.downloads
	m.cursor = 2 // Blue Planet II

	pressKey(m, "/")
	typeFilter(m, "planet")
	if got := m.rows[m.cursor].Name; got != "Blue Planet II" {
		t.Fatalf("cursor on %q, want it left on Blue Planet II", got)
	}

	// the row it was on drops out, so the cursor goes to the top match
	typeFilter(m, " o")
	if got := rowNames(m.rows); !slices.Equal(got, []string{"planet of the apes"}) {
		t.Fatalf("filtered list = %q, want only the one match", got)
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want the top match", m.cursor)
	}
}

func TestEnterAcceptsTheFilterAndEscClearsIt(t *testing.T) {
	app, _ := filterTestApp(t)
	m := &app.downloads

	pressKey(m, "/")
	typeFilter(m, "planet")
	m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.filtering() {
		t.Fatal("enter left the prompt holding the keyboard")
	}
	if !m.filterShown() || len(m.rows) != 3 {
		t.Fatalf("enter dropped the filter: shown %v, %d rows", m.filterShown(), len(m.rows))
	}
	// the list has its shortcuts back
	pressKey(m, "j")
	if got := m.rows[m.cursor].Name; got != "planet of the apes" {
		t.Fatalf("j landed on %q, want the second match", got)
	}

	m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.filterShown() || m.query() != "" {
		t.Fatalf("esc left the filter up: query %q", m.query())
	}
	if got := len(m.rows); got != 4 {
		t.Fatalf("list = %d rows, want the whole library back", got)
	}
	if got := m.rows[m.cursor].Name; got != "planet of the apes" {
		t.Fatalf("esc moved the cursor to %q", got)
	}
}

// Reopening the prompt edits the query that is already narrowing the list
// rather than starting from nothing.
func TestSlashReopensTheFilterOnItsQuery(t *testing.T) {
	app, _ := filterTestApp(t)
	m := &app.downloads

	pressKey(m, "/")
	typeFilter(m, "planet")
	m.update(tea.KeyMsg{Type: tea.KeyEnter})

	pressKey(m, "/")
	if m.query() != "planet" {
		t.Fatalf("reopened prompt holds %q, want the query it was left with", m.query())
	}
	m.update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got, want := rowNames(m.rows), []string{"Planet Earth", "planet of the apes", "Blue Planet II"}; !slices.Equal(got, want) {
		t.Fatalf("list = %q, want %q for \"plane\"", got, want)
	}
}

// While the prompt has focus every key is text for it, so the single-key
// shortcuts the app answers first — quit included — don't get a look in.
func TestFilterPromptTakesKeysAheadOfShortcuts(t *testing.T) {
	app, _ := filterTestApp(t)
	m := &app.downloads
	pressKey(m, "/")

	for _, key := range []string{"q", "a", "p", "d", "y"} {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	}
	if app.addlink != nil {
		t.Fatal("a opened the add-link dialog while the prompt was open")
	}
	if app.eng.Paused() {
		t.Fatal("p paused the queue while the prompt was open")
	}
	// every one of them is in the prompt, which is only reachable past the
	// shortcuts the app answers first — q above all, which would have quit
	if m.query() != "qapdy" {
		t.Fatalf("prompt holds %q, want every key typed into it", m.query())
	}

	// ctrl+c still quits, since nothing else can
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c produced no command while the prompt was open")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not quit while the prompt was open")
	}
	if m.query() != "qapdy" {
		t.Fatalf("prompt holds %q, want ctrl+c to have gone to the app", m.query())
	}
}

// The filter is a view on the library, not a change to it: the queue keeps
// running through rows it hides, and a jump to the head brings the row back
// rather than reporting there is nothing to jump to.
func TestFilterLeavesTheQueueAloneAndFocusBringsTheHeadBack(t *testing.T) {
	app, _, _ := toggleTestApp(t)
	m := &app.downloads

	pressKey(m, "/")
	typeFilter(m, "zzz")
	m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.rows) != 0 {
		t.Fatalf("list = %d rows, want the download hidden", len(m.rows))
	}
	if m.head.dl == nil || m.head.file == nil {
		t.Fatal("the queue head went missing behind the filter")
	}

	pressKey(m, "f")
	if m.filterShown() {
		t.Fatalf("f left the filter up: query %q", m.query())
	}
	if len(m.rows) != 1 || m.rows[m.cursor].Name != "Folder" {
		t.Fatalf("f landed on %q, want the queue head", rowNames(m.rows))
	}
}

// Adding a link puts the cursor on the new row, which a filter would as likely
// as not be hiding.
func TestAddingADownloadDropsTheFilter(t *testing.T) {
	app, database := filterTestApp(t)
	m := &app.downloads

	pressKey(m, "/")
	typeFilter(m, "cosmos")
	m.update(tea.KeyMsg{Type: tea.KeyEnter})

	id, err := database.InsertDownload(&db.Download{
		URL: "u5", Handle: "h5", LinkType: "folder", Name: "Chernobyl",
		DestPath: filepath.Join(app.cfg.DownloadDir, "Chernobyl"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.selectNewDownload(id)

	if m.filterShown() {
		t.Fatalf("the filter survived a new download: query %q", m.query())
	}
	if got := m.rows[m.cursor].Name; got != "Chernobyl" {
		t.Fatalf("cursor on %q, want the download just added", got)
	}
}

// The prompt is a row of the pane, so everything the pane measures in rows has
// to count it: a click lands on the download it points at, and a page is one
// row shorter.
func TestFilterPromptTakesARowOffTheList(t *testing.T) {
	app, _ := filterTestApp(t)
	m := &app.downloads
	m.view(60, 8)
	if got := m.listPageSize(); got != 8 {
		t.Fatalf("page = %d rows, want the whole pane while there is no prompt", got)
	}

	pressKey(m, "/")
	m.view(60, 8)
	if got := m.listPageSize(); got != 7 {
		t.Fatalf("page = %d rows, want one less for the prompt", got)
	}

	m.cursor = 2
	m.clickDownload(0) // the prompt itself is not a row to land on
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want a click on the prompt to select nothing", m.cursor)
	}
	m.clickDownload(1)
	if got := m.rows[m.cursor].Name; got != "Planet Earth" {
		t.Fatalf("click landed on %q, want the first download", got)
	}
}
