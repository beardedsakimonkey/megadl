package ui

import (
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
