package ui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"megadl/internal/db"
)

func TestShuffleIncludesDescendants(t *testing.T) {
	dir := t.TempDir()
	season := filepath.Join(dir, "Season 01")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []db.File{
		{LocalPath: filepath.Join(season, "cover.jpg"), Status: db.FileDone},
		{LocalPath: filepath.Join(season, "e1.mkv"), Status: db.FileDone},
		{LocalPath: filepath.Join(season, "e2.mkv"), Status: db.FileSkipped},
		{LocalPath: filepath.Join(season, "e3.mkv"), Status: db.FilePending},
		{LocalPath: filepath.Join(season, "Extras", "bonus.mkv"), Status: db.FileDone},
		{LocalPath: filepath.Join(dir, "Season 02", "e1.mkv"), Status: db.FileDone},
		{LocalPath: filepath.Join(dir, "Season 02", "missing.mkv"), Status: db.FileDone},
	}
	for _, f := range files {
		if f.Status == db.FilePending || filepath.Base(f.LocalPath) == "missing.mkv" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.LocalPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.LocalPath, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, folder := range []bool{false, true} {
		for _, shuffle := range []bool{false, true} {
			name := "download"
			if folder {
				name = "folder"
			}
			key := tea.KeyMsg{Type: tea.KeyEnter}
			if shuffle {
				key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}
				name += "/shuffle"
			}
			t.Run(name, func(t *testing.T) {
				called := false
				m := &downloadsModel{
					pane: paneList,
					openFile: func(paths []string, gotShuffle bool) (func() error, error) {
						called = true
						if gotShuffle != shuffle {
							t.Fatalf("shuffle = %v, want %v", gotShuffle, shuffle)
						}
						want := []string{files[1].LocalPath, files[2].LocalPath}
						if shuffle {
							want = append(want, files[4].LocalPath)
							if !folder {
								want = append(want, files[5].LocalPath)
							}
						}
						if folder && !shuffle {
							want = []string{files[4].LocalPath}
						}
						if shuffle {
							slices.Sort(paths)
							slices.Sort(want)
						}
						if !slices.Equal(paths, want) {
							t.Fatalf("paths = %v, want %v", paths, want)
						}
						return nil, nil
					},
				}
				m.setFiles(&db.Download{DestPath: dir}, files)
				if folder {
					m.pane = paneFiles
					m.treeCursor = 0
				}
				cmd := m.update(key)
				if cmd == nil {
					t.Fatal("no playback command")
				}
				if called {
					t.Fatal("player started before command ran")
				}
				cmd()
				if !called {
					t.Fatal("player was not started")
				}
			})
		}
	}
}
