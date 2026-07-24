package ui

import (
	"reflect"
	"testing"

	"megadl/internal/mega"
)

// tree ordered like dl --list output (path-sorted, root first)
func testNodes() []mega.Node {
	return []mega.Node{
		{Path: "/Root", Name: "Root", Type: "folder", Handle: "root", Parent: ""},
		{Path: "/Root/S01", Name: "S01", Type: "folder", Handle: "s01", Parent: "root"},
		{Path: "/Root/S01/e01.mkv", Name: "e01.mkv", Type: "file", Size: 100, Handle: "e1", Parent: "s01"},
		{Path: "/Root/S01/e02.mkv", Name: "e02.mkv", Type: "file", Size: 200, Handle: "e2", Parent: "s01"},
		{Path: "/Root/extra.txt", Name: "extra.txt", Type: "file", Size: 7, Handle: "x", Parent: "root"},
	}
}

func TestPickerDefaultsToAllAndCollapsesToRoot(t *testing.T) {
	p := newPicker(testNodes())
	count, bytes := p.totals()
	if count != 3 || bytes != 307 {
		t.Fatalf("defaults: count=%d bytes=%d", count, bytes)
	}
	// everything selected -> minimal selection is just the root folder
	if got := p.minimalHandles(); !reflect.DeepEqual(got, []string{"root"}) {
		t.Fatalf("minimal = %v", got)
	}
}

func TestPickerPartialSelection(t *testing.T) {
	p := newPicker(testNodes())
	// deselect extra.txt (row index 4)
	p.toggle(4)

	count, bytes := p.totals()
	if count != 2 || bytes != 300 {
		t.Fatalf("count=%d bytes=%d", count, bytes)
	}
	// S01 fully selected -> its handle, not its children
	if got := p.minimalHandles(); !reflect.DeepEqual(got, []string{"s01"}) {
		t.Fatalf("minimal = %v", got)
	}

	// deselect one episode too -> only the remaining file handle
	p.toggle(3)
	if got := p.minimalHandles(); !reflect.DeepEqual(got, []string{"e1"}) {
		t.Fatalf("minimal = %v", got)
	}
	files := p.selectedFiles()
	if len(files) != 1 || files[0].Handle != "e1" {
		t.Fatalf("selectedFiles = %+v", files)
	}
}

func TestPickerFolderToggle(t *testing.T) {
	p := newPicker(testNodes())
	p.setAll(false)
	if c, _ := p.totals(); c != 0 {
		t.Fatal("setAll(false) failed")
	}

	// toggling S01 (row 1) selects only its subtree
	p.toggle(1)
	if got := p.minimalHandles(); !reflect.DeepEqual(got, []string{"s01"}) {
		t.Fatalf("minimal = %v", got)
	}
	if state := p.folderState(0); state != 1 {
		t.Fatalf("root state = %d, want partial", state)
	}

	// toggling a fully-selected folder deselects it
	p.toggle(1)
	if c, _ := p.totals(); c != 0 {
		t.Fatalf("re-toggle should clear, count=%d", c)
	}
}
