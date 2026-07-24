package ui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"megadl/internal/config"
	"megadl/internal/db"
	"megadl/internal/mega"
)

func openAddlinkTestApp(t *testing.T) (*App, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	app := &App{
		cfg: &config.Config{DownloadDir: dir},
		db:  database,
	}
	app.downloads = newDownloadsModel(app)
	return app, database
}

func TestAddlinkSuggestsExistingResourceInsteadOfDuplicate(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	id, err := database.InsertDownload(&db.Download{
		URL:      "https://mega.nz/folder/AAAAAAAA#old",
		Handle:   "root",
		LinkType: "folder",
		Name:     "Skins",
		DestPath: filepath.Join(app.cfg.DownloadDir, "Skins"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkCompleted(id, db.StatusDone); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()

	url := "https://mega.nz/folder/AAAAAAAA#new"
	m := newAddlinkModel(app)
	m.url, m.linkType, m.state = url, "folder", stateListing
	model, cmd := m.update(listResultMsg{
		url: url,
		nodes: []mega.Node{{
			Path: "/Skins", Name: "Skins", Type: "folder", Handle: "root",
		}},
	})
	if cmd != nil || model.state != stateExisting || model.existing == nil || model.existing.ID != id {
		t.Fatalf("existing state = %+v, cmd=%v", model, cmd)
	}

	model, cmd = model.update(tea.KeyMsg{Type: tea.KeyEnter})
	if model != nil || cmd != nil {
		t.Fatalf("reuse should close modal: model=%+v, cmd=%v", model, cmd)
	}
	rows, err := database.Downloads()
	if err != nil || len(rows) != 1 {
		t.Fatalf("downloads after reuse = %+v, %v", rows, err)
	}
	if app.downloads.rows[app.downloads.cursor].ID != id {
		t.Fatalf("selected download = %d, want %d",
			app.downloads.rows[app.downloads.cursor].ID, id)
	}
}

func TestAddlinkStillDisambiguatesDifferentResourceWithSameName(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	if _, err := database.InsertDownload(&db.Download{
		URL:      "old",
		Handle:   "old-root",
		LinkType: "file",
		Name:     "skins.zip",
		DestPath: filepath.Join(app.cfg.DownloadDir, "skins.zip"),
	}, nil); err != nil {
		t.Fatal(err)
	}

	url := "https://mega.nz/file/AAAAAAAA#key"
	m := newAddlinkModel(app)
	m.url, m.linkType, m.state = url, "file", stateListing
	model, _ := m.update(listResultMsg{
		url: url,
		nodes: []mega.Node{{
			Path: "/skins.zip", Name: "skins.zip", Type: "file",
			Handle: "new-root", Size: 10,
		}},
	})
	if model.state != stateName {
		t.Fatalf("state = %v, want name", model.state)
	}
	if got := model.nameInput.Value(); got != "skins (2).zip" {
		t.Fatalf("name = %q, want %q", got, "skins (2).zip")
	}
}

func TestEnqueueRejectsResourceAddedAfterListing(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	if _, err := database.InsertDownload(&db.Download{
		URL:      "old",
		Handle:   "root",
		LinkType: "file",
		Name:     "skins.zip",
		DestPath: filepath.Join(app.cfg.DownloadDir, "skins.zip"),
	}, nil); err != nil {
		t.Fatal(err)
	}

	m := newAddlinkModel(app)
	m.url, m.linkType = "new", "file"
	m.nodes = []mega.Node{{
		Path: "/skins.zip", Name: "skins.zip", Type: "file",
		Handle: "root", Size: 10,
	}}
	m.nameInput.SetValue("skins (2).zip")

	err := m.enqueue()
	if err == nil || !strings.Contains(err.Error(), "reuse the existing download") {
		t.Fatalf("enqueue error = %v", err)
	}
	rows, err := database.Downloads()
	if err != nil || len(rows) != 1 {
		t.Fatalf("downloads after enqueue = %+v, %v", rows, err)
	}
}

func TestAddlinkNavigatesSubmittedLinkHistory(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	oldURL := "https://mega.nz/file/AAAAAAAA#old"
	newURL := "https://mega.nz/folder/BBBBBBBB#new"
	for i, url := range []string{oldURL, newURL, newURL} {
		if _, err := database.InsertDownload(&db.Download{
			URL:      url,
			Handle:   string(rune('a' + i)),
			LinkType: "file",
			Name:     string(rune('a' + i)),
			DestPath: filepath.Join(app.cfg.DownloadDir, string(rune('a'+i))),
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	app.downloads.reload()

	m := newAddlinkModel(app)
	m.urlInput.SetValue("draft")

	m.updateKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.urlInput.Value(); got != newURL {
		t.Fatalf("up = %q, want newest %q", got, newURL)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyCtrlP})
	if got := m.urlInput.Value(); got != oldURL {
		t.Fatalf("ctrl+p = %q, want older %q", got, oldURL)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.urlInput.Value(); got != oldURL {
		t.Fatalf("up past oldest = %q, want %q", got, oldURL)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyCtrlN})
	if got := m.urlInput.Value(); got != newURL {
		t.Fatalf("ctrl+n = %q, want newer %q", got, newURL)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.urlInput.Value(); got != "draft" {
		t.Fatalf("down past newest = %q, want draft", got)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.urlInput.Value(); got != "draft" {
		t.Fatalf("down while at draft = %q, want draft", got)
	}
}
