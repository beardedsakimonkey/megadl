package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"megadl/internal/db"
)

func TestTabFoldsFolderAndKeepsItsCursor(t *testing.T) {
	app, _, _ := folderTreeApp(t)
	m := &app.downloads
	full := append([]fileTreeRow(nil), m.tree...)
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if len(m.tree) != 4 || m.treeCursor != 0 || m.tree[1].path != "Season 02" {
		t.Fatalf("collapsed tree = %+v, cursor = %d", m.tree, m.treeCursor)
	}
	if view := ansi.Strip(m.dirRowView(m.tree[0], true, 40)); !strings.Contains(view, "▸ Season 01/") {
		t.Fatalf("collapsed header = %q", view)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.tree[m.treeCursor].path != "Season 02" {
		t.Fatal("cursor entered a hidden row")
	}
	app.Update(tea.KeyMsg{Type: tea.KeyUp})
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !reflect.DeepEqual(m.tree, full) || m.treeCursor != 0 {
		t.Fatalf("expanded tree = %+v, cursor = %d", m.tree, m.treeCursor)
	}
	if view := ansi.Strip(m.dirRowView(m.tree[0], true, 40)); !strings.Contains(view, "▾ Season 01/") {
		t.Fatalf("expanded header = %q", view)
	}
}

func TestNestedFoldSurvivesParentToggleAndReload(t *testing.T) {
	app, database, id := folderTreeApp(t)
	m := &app.downloads
	m.treeCursor = 1 // Extras
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.treeCursor = 0
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if _, err := database.MergeFiles(id, []db.File{{
		NodeHandle: "new", RemotePath: "/Show/Season 01/new.mkv",
		LocalPath: "/dl/Show/Season 01/new.mkv",
	}}); err != nil {
		t.Fatal(err)
	}
	m.reload()
	if len(m.tree) != 4 || m.treeCursor != 0 {
		t.Fatalf("reload unfolded folder: %+v", m.tree)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if len(m.tree) != 7 || m.tree[1].path != "Season 01/Extras" || m.tree[2].dir != "" {
		t.Fatalf("parent expansion lost nested fold: %+v", m.tree)
	}
	if m.files[m.tree[2].file].NodeHandle != "a" {
		t.Fatal("nested file should still be hidden")
	}
}

func TestCollapsedFolderActionsIncludeHiddenFiles(t *testing.T) {
	app, database, id := folderTreeApp(t)
	m := &app.downloads
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.rowFiles(0); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("folder action targets = %v", got)
	}
	app.Update(tea.KeyMsg{Type: tea.KeySpace})
	if got := queuedFiles(t, database, id); !reflect.DeepEqual(got, map[string]bool{"x": true, "a": true, "b": false, "r": false}) {
		t.Fatalf("queued files = %v", got)
	}
	if len(m.tree) != 4 {
		t.Fatal("queue toggle expanded the folder")
	}
	app.Update(tea.KeyMsg{Type: tea.KeySpace})
	if got := queuedFiles(t, database, id); !reflect.DeepEqual(got, map[string]bool{"x": false, "a": false, "b": false, "r": false}) {
		t.Fatalf("queued files = %v", got)
	}
}

func TestFocusFileRevealsCollapsedAncestors(t *testing.T) {
	app, _, id := folderTreeApp(t)
	m := &app.downloads
	file := m.files[0]
	m.treeCursor = 1
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.treeCursor = 0
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !m.focusFile(id, &file) || m.cursorFile() != 0 || len(m.tree) != 7 {
		t.Fatalf("focus failed to reveal file: cursor %d, tree %+v", m.treeCursor, m.tree)
	}
}

func TestTabOnFileDoesNothing(t *testing.T) {
	app, _, _ := folderTreeApp(t)
	m := &app.downloads
	m.treeCursor = 3
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if len(m.tree) != 7 || m.treeCursor != 3 || len(m.collapsed) != 0 {
		t.Fatal("tab on file changed the tree")
	}
}

func TestFoldStateBelongsToEachDownload(t *testing.T) {
	m := downloadsModel{}
	first := &db.Download{ID: 1, DestPath: "/dl/one"}
	second := &db.Download{ID: 2, DestPath: "/dl/two"}
	m.setFiles(first, []db.File{{LocalPath: "/dl/one/Season/a"}})
	m.toggleCollapsed()
	m.setFiles(second, []db.File{{LocalPath: "/dl/two/Season/a"}})
	if len(m.tree) != 2 {
		t.Fatal("fold state leaked into another download")
	}
	m.setFiles(first, []db.File{{LocalPath: "/dl/one/Season/a"}})
	if len(m.tree) != 1 {
		t.Fatal("switching downloads lost fold state")
	}
}

func TestTabTogglesEmptyFolder(t *testing.T) {
	m := downloadsModel{}
	m.setListing(&db.Download{ID: 1, DestPath: "/dl"}, nil,
		[]db.Directory{{LocalPath: "/dl/empty"}})
	m.toggleCollapsed()
	if len(m.tree) != 1 || !m.collapsed[1]["empty"] {
		t.Fatal("empty folder did not collapse")
	}
	m.toggleCollapsed()
	if len(m.tree) != 1 || m.collapsed[1]["empty"] {
		t.Fatal("empty folder did not expand")
	}
}

func TestFoldSavesSerializeAndQuitWaits(t *testing.T) {
	app, database, id := folderTreeApp(t)
	m := &app.downloads
	m.treeCursor = 1 // Extras
	_, firstSave := app.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.treeCursor = 0
	_, next := app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if next != nil {
		t.Fatal("second save started before first completed")
	}
	_, quit := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if quit != nil {
		t.Fatal("quit before pending save completed")
	}
	_, secondSave := app.Update(firstSave())
	if secondSave == nil {
		t.Fatal("latest folds were not saved")
	}
	_, quit = app.Update(secondSave())
	if quit == nil {
		t.Fatal("quit did not resume after save")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("expected quit")
	}

	restored := newDownloadsModel(app)
	restored.restore()
	if len(restored.tree) != 4 || !restored.collapsed[id]["Season 01/Extras"] {
		t.Fatalf("restored folds = %v, tree = %+v", restored.collapsed, restored.tree)
	}
	app.downloads = restored
	m = &app.downloads
	m.pane = paneFiles
	m.treeCursor = 0
	_, save := app.Update(tea.KeyMsg{Type: tea.KeyTab})
	app.Update(save())
	restored = newDownloadsModel(app)
	restored.restore()
	if len(restored.tree) != 6 || restored.collapsed[id]["Season 01"] || !restored.collapsed[id]["Season 01/Extras"] {
		t.Fatalf("expanded parent lost nested fold: %+v", restored.tree)
	}
	// Jumping to a file also persists its opened ancestors.
	app.downloads = restored
	m = &app.downloads
	file := m.files[0]
	m.focusFile(id, &file)
	app.Update(m.saveFolds()())
	folders, err := database.CollapsedDirs()
	if err != nil || len(folders[id]) != 0 {
		t.Fatalf("revealed folders = %v, %v", folders, err)
	}
}

func TestFoldSaveFailureKeepsChangesForRetry(t *testing.T) {
	app, database, id := folderTreeApp(t)
	m := &app.downloads
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	// Report a failed write without executing it, then retry on quit.
	_, cmd := app.Update(foldsSavedMsg{folders: map[int64][]string{id: {"Season 01"}}, err: fmt.Errorf("write failed")})
	if cmd != nil || m.foldSaving || m.foldQuitting || !m.noticeErr || !m.foldDirty[id] {
		t.Fatalf("failed save was not retained: %+v", m)
	}
	_, save := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_, quit := app.Update(save())
	if quit == nil {
		t.Fatal("retry did not finish quitting")
	}
	folders, err := database.CollapsedDirs()
	if err != nil || !folders[id]["Season 01"] {
		t.Fatalf("retry = %v, %v", folders, err)
	}
}
