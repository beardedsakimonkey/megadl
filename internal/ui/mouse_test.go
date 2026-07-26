package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"megadl/internal/db"
	"megadl/internal/engine"
)

func leftClick(x, y int) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y}
}

func wheel(button tea.MouseButton, x, y int) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: button, X: x, Y: y}
}

// screenRow returns the terminal row a rendered frame drew want on.
func screenRow(t *testing.T, frame, want string) int {
	t.Helper()
	for i, line := range strings.Split(frame, "\n") {
		if strings.Contains(ansi.Strip(line), want) {
			return i
		}
	}
	t.Fatalf("frame does not contain %q:\n%s", want, frame)
	return -1
}

// fakeClock drives the double-click window without sleeping.
func fakeClock() (now func() time.Time, advance func(time.Duration)) {
	t := time.Now()
	return func() time.Time { return t }, func(d time.Duration) { t = t.Add(d) }
}

func TestClickTrackerPairsOnlyNearbyPressesOnTheSameRow(t *testing.T) {
	now, advance := fakeClock()
	c := clickTracker{now: now}

	if c.press(clickDownload, 1) {
		t.Fatal("first press reported a double click")
	}
	if !c.press(clickDownload, 1) {
		t.Fatal("second press on the same row is not a double click")
	}
	// a third press starts a new pair rather than firing again
	if c.press(clickDownload, 1) {
		t.Fatal("third press fired another double click")
	}

	c.press(clickDownload, 1)
	advance(doubleClickInterval + time.Millisecond)
	if c.press(clickDownload, 1) {
		t.Fatal("presses outside the interval paired up")
	}
	if c.press(clickFile, 1) {
		t.Fatal("presses in different lists paired up")
	}
	c.press(clickDownload, 2)
	if c.press(clickDownload, 3) {
		t.Fatal("presses on different rows paired up")
	}
}

// mouseApp is a two-folder library laid out at a fixed size, so tests can
// address rows with the coordinates the view just rendered.
func mouseApp(t *testing.T) *App {
	t.Helper()
	app, database := openAddlinkTestApp(t)
	app.eng = engine.New(nil, database)
	app.width, app.height = 80, 15

	for _, name := range []string{"First", "Second"} {
		dest := "/dl/" + name
		if _, err := database.InsertDownload(&db.Download{
			URL: name, Handle: name, LinkType: "folder", Name: name, DestPath: dest,
		}, []db.File{
			{NodeHandle: name + "a", RemotePath: "/" + name + "/a",
				LocalPath: filepath.Join(dest, "a.mkv"), Size: 10, Queued: true},
			{NodeHandle: name + "b", RemotePath: "/" + name + "/b",
				LocalPath: filepath.Join(dest, "b.mkv"), Size: 10, Queued: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	app.downloads.reload()
	app.View() // records the geometry mouse events are resolved against
	return app
}

func TestClickSelectsDownloadRow(t *testing.T) {
	app := mouseApp(t)
	m := &app.downloads
	if m.cursor != 0 {
		t.Fatalf("cursor starts at %d, want 0", m.cursor)
	}

	app.Update(leftClick(2, app.bodyTop+1))

	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want the clicked row 1", m.cursor)
	}
	if m.pane != paneList {
		t.Fatalf("pane = %v, want the downloads pane focused", m.pane)
	}
	if m.filesFor != m.rows[1].ID {
		t.Fatalf("file pane shows download %d, want %d", m.filesFor, m.rows[1].ID)
	}
}

func TestClicksOutsideTheBodyAreIgnored(t *testing.T) {
	app := mouseApp(t)
	m := &app.downloads

	app.Update(leftClick(2, 0))             // header
	app.Update(leftClick(2, app.height-1))  // footer
	app.Update(leftClick(2, app.bodyTop-1)) // rule under the header
	if m.cursor != 0 || m.pane != paneList {
		t.Fatalf("chrome click moved the selection: cursor=%d pane=%v", m.cursor, m.pane)
	}
}

func TestDoubleClickOnDownloadOpensItsFiles(t *testing.T) {
	app := mouseApp(t)
	m := &app.downloads
	now, advance := fakeClock()
	m.clicks.now = now

	app.Update(leftClick(2, app.bodyTop))
	if m.pane != paneList {
		t.Fatalf("single click focused %v, want the downloads pane", m.pane)
	}
	advance(doubleClickInterval / 2)
	app.Update(leftClick(2, app.bodyTop))

	if m.pane != paneFiles {
		t.Fatalf("pane = %v, want the file pane after a double click", m.pane)
	}
}

func TestSlowSecondClickOnDownloadDoesNotOpenFiles(t *testing.T) {
	app := mouseApp(t)
	m := &app.downloads
	now, advance := fakeClock()
	m.clicks.now = now

	app.Update(leftClick(2, app.bodyTop))
	advance(doubleClickInterval + time.Millisecond)
	app.Update(leftClick(2, app.bodyTop))

	if m.pane != paneList {
		t.Fatalf("pane = %v, want the downloads pane to keep focus", m.pane)
	}
}

func TestWheelMovesTheCursorOfThePaneUnderThePointer(t *testing.T) {
	app := mouseApp(t)
	m := &app.downloads

	app.Update(wheel(tea.MouseButtonWheelDown, 2, app.bodyTop))
	if m.cursor != 1 || m.pane != paneList {
		t.Fatalf("wheel over the list: cursor=%d pane=%v, want 1 and the list", m.cursor, m.pane)
	}
	app.Update(wheel(tea.MouseButtonWheelUp, 2, app.bodyTop))
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after scrolling back up", m.cursor)
	}
	// the top row holds: the cursor stays put instead of wrapping
	app.Update(wheel(tea.MouseButtonWheelUp, 2, app.bodyTop))
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want it clamped at 0", m.cursor)
	}

	app.Update(wheel(tea.MouseButtonWheelDown, m.listW+4, app.bodyTop))
	if m.pane != paneFiles || m.fileCursor != 1 {
		t.Fatalf("wheel over the file pane: pane=%v fileCursor=%d, want the file pane and 1",
			m.pane, m.fileCursor)
	}
}

// filePaneModel renders a folder with one subdirectory so tests can click
// both a directory header and the file rows under it.
func filePaneModel(t *testing.T, dir string) (*downloadsModel, *[]string) {
	t.Helper()
	opened := &[]string{}
	app, _ := openAddlinkTestApp(t)
	app.eng = engine.New(nil, nil)
	m := &downloadsModel{
		app:  app,
		rows: []*db.Download{{ID: 7, Name: "Show", DestPath: dir}},
		files: []db.File{
			{ID: 1, LocalPath: filepath.Join(dir, "Season 01", "e1.mkv"),
				Size: 10, Status: db.FileDone, Queued: true},
			{ID: 2, LocalPath: filepath.Join(dir, "Season 01", "e2.mkv"),
				Size: 10, Status: db.FileDone, Queued: true},
			{ID: 3, LocalPath: filepath.Join(dir, "readme.txt"),
				Size: 10, Status: db.FileDone, Queued: true},
		},
		filesFor: 7,
		openFile: func(paths []string) error {
			*opened = append(*opened, paths...)
			return nil
		},
	}
	m.view(80, 12) // tree rows: "Season 01/", e1, e2, readme.txt
	return m, opened
}

func TestClickSelectsFileRow(t *testing.T) {
	m, _ := filePaneModel(t, t.TempDir())

	// body row 0 is the pane title, 1 the directory header, 2 the first file
	if cmd := m.mouse(leftClick(m.listW+4, 3)); cmd != nil {
		t.Fatalf("single click on a file returned a command: %v", cmd)
	}
	if m.pane != paneFiles || m.fileCursor != 1 {
		t.Fatalf("pane=%v fileCursor=%d, want the file pane and file 1", m.pane, m.fileCursor)
	}

	// a directory header selects the first file beneath it
	m.mouse(leftClick(m.listW+4, 1))
	if m.fileCursor != 0 {
		t.Fatalf("fileCursor = %d, want the first file under the header", m.fileCursor)
	}
}

func TestDoubleClickPlaysFile(t *testing.T) {
	dir := t.TempDir()
	m, opened := filePaneModel(t, dir)
	path := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	now, advance := fakeClock()
	m.clicks.now = now

	m.mouse(leftClick(m.listW+4, 4))
	advance(doubleClickInterval / 2)
	cmd := m.mouse(leftClick(m.listW+4, 4))
	if cmd == nil {
		t.Fatal("double click on a file returned no command")
	}
	m.update(cmd())

	if len(*opened) != 1 || (*opened)[0] != path {
		t.Fatalf("opened files = %v, want %q", *opened, path)
	}
	if !strings.Contains(m.notice, "playing readme.txt") {
		t.Fatalf("notice = %q, want playing confirmation", m.notice)
	}
}

// Double clicking a directory header only selects; it must not start playing
// the file it selected.
func TestDoubleClickOnDirectoryHeaderPlaysNothing(t *testing.T) {
	m, _ := filePaneModel(t, t.TempDir())
	now, advance := fakeClock()
	m.clicks.now = now

	m.mouse(leftClick(m.listW+4, 1))
	advance(doubleClickInterval / 2)
	if cmd := m.mouse(leftClick(m.listW+4, 1)); cmd != nil {
		t.Fatalf("double click on a directory header returned a command: %v", cmd)
	}
}

func TestClickInPickerMovesCursorAndDoubleClickToggles(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	app.eng = engine.New(nil, nil)
	app.width, app.height = 80, 24

	m := newAddlinkModel(app)
	m.linkType, m.state = "folder", statePicker
	m.nodes = testNodes()
	m.picker = newPicker(m.nodes)
	app.addlink = m

	// aim at the row the dialog actually drew e01.mkv on (picker row 2 of
	// Root, S01, e01.mkv, e02.mkv, extra.txt)
	row := screenRow(t, app.View(), "e01.mkv")

	now, advance := fakeClock()
	m.clicks.now = now

	app.Update(leftClick(m.modal.x+4, row))
	if m.picker.cursor != 2 {
		t.Fatalf("picker cursor = %d, want the clicked row 2", m.picker.cursor)
	}
	if !m.picker.selected["e1"] {
		t.Fatal("a single click cleared a checkbox, want it left alone")
	}

	advance(doubleClickInterval / 2)
	app.Update(leftClick(m.modal.x+4, row))
	if m.picker.selected["e1"] {
		t.Fatal("double click did not clear the checkbox")
	}

	// clicks outside the dialog leave the picker alone
	app.Update(leftClick(0, app.bodyTop))
	if m.picker.cursor != 2 {
		t.Fatalf("picker cursor = %d, want it unchanged by a click outside", m.picker.cursor)
	}

	app.Update(wheel(tea.MouseButtonWheelDown, m.modal.x+4, row))
	if m.picker.cursor != 3 {
		t.Fatalf("picker cursor = %d, want 3 after a wheel notch", m.picker.cursor)
	}
}
