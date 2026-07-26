package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"megadl/internal/db"
	"megadl/internal/engine"
)

// renameTestApp builds an app around one folder download that exists on disk,
// with the cursor on its row.
func renameTestApp(t *testing.T) (*App, *db.DB, int64) {
	t.Helper()
	app, database := openAddlinkTestApp(t)
	app.eng = engine.New(nil, database)

	dest := filepath.Join(app.cfg.DownloadDir, "Show")
	if err := os.MkdirAll(filepath.Join(dest, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "s1", "a.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := database.InsertDownload(&db.Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "Show", DestPath: dest,
	}, []db.File{{
		NodeHandle: "a", RemotePath: "/Show/s1/a.mkv",
		LocalPath: filepath.Join(dest, "s1", "a.mkv"), Queued: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()
	return app, database, id
}

func typeKeys(app *App, keys string) {
	for _, r := range keys {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// openRename presses r and returns the prompt it opened.
func openRename(t *testing.T, app *App) *renameModel {
	t.Helper()
	typeKeys(app, "r")
	if app.rename == nil {
		t.Fatal("r did not open the rename prompt")
	}
	return app.rename
}

func TestRenameMovesFolderOnDiskAndRepointsRecords(t *testing.T) {
	app, database, id := renameTestApp(t)
	prompt := openRename(t, app)
	if got := prompt.input.Value(); got != "Show" {
		t.Fatalf("prompt starts with %q, want the current name %q", got, "Show")
	}

	prompt.input.SetValue("Series")
	prompt.input.CursorEnd()
	typeKeys(app, "2") // keys reach the prompt, not the downloads view
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if app.rename != nil {
		t.Fatalf("prompt still open: %v", app.rename.errMsg)
	}
	dest := filepath.Join(app.cfg.DownloadDir, "Series2")
	if _, err := os.Stat(filepath.Join(dest, "s1", "a.mkv")); err != nil {
		t.Fatalf("file not under renamed folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DownloadDir, "Show")); !os.IsNotExist(err) {
		t.Fatalf("old folder still on disk (%v)", err)
	}

	dl, err := database.Download(id)
	if err != nil {
		t.Fatal(err)
	}
	if dl.Name != "Series2" || dl.DestPath != dest {
		t.Fatalf("download = %q at %q, want %q at %q", dl.Name, dl.DestPath, "Series2", dest)
	}
	files, err := database.Files(id)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dest, "s1", "a.mkv"); files[0].LocalPath != want {
		t.Fatalf("file path = %q, want %q", files[0].LocalPath, want)
	}
	if !strings.Contains(app.downloads.notice, "Series2") {
		t.Fatalf("notice = %q, want it to name the new folder", app.downloads.notice)
	}
	if app.downloads.rows[0].Name != "Series2" {
		t.Fatalf("list shows %q, want the renamed row", app.downloads.rows[0].Name)
	}
}

func TestRenameRejectsNameAlreadyOnDisk(t *testing.T) {
	app, database, id := renameTestApp(t)
	if err := os.MkdirAll(filepath.Join(app.cfg.DownloadDir, "Other"), 0o755); err != nil {
		t.Fatal(err)
	}

	prompt := openRename(t, app)
	prompt.input.SetValue("Other")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if app.rename == nil {
		t.Fatal("prompt closed despite the collision")
	}
	if !strings.Contains(prompt.errMsg, "already exists") {
		t.Fatalf("errMsg = %q, want a collision error", prompt.errMsg)
	}
	if dl, _ := database.Download(id); dl.Name != "Show" {
		t.Fatalf("name = %q, want it left alone", dl.Name)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DownloadDir, "Show")); err != nil {
		t.Fatalf("original folder moved: %v", err)
	}
}

func TestRenameRejectsEmptyName(t *testing.T) {
	app, database, id := renameTestApp(t)
	prompt := openRename(t, app)
	prompt.input.SetValue("...") // sanitizes away to nothing
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if app.rename == nil {
		t.Fatal("prompt closed on an empty name")
	}
	if !strings.Contains(prompt.errMsg, "empty") {
		t.Fatalf("errMsg = %q, want an empty-name error", prompt.errMsg)
	}
	if dl, _ := database.Download(id); dl.Name != "Show" {
		t.Fatalf("name = %q, want it left alone", dl.Name)
	}
}

// A download whose folder was never created still renames: only its records
// move.
func TestRenameWithoutFolderOnDiskUpdatesRecords(t *testing.T) {
	app, database, id := renameTestApp(t)
	if err := os.RemoveAll(app.downloads.rows[0].DestPath); err != nil {
		t.Fatal(err)
	}

	prompt := openRename(t, app)
	prompt.input.SetValue("Series")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if app.rename != nil {
		t.Fatalf("prompt still open: %v", app.rename.errMsg)
	}
	dest := filepath.Join(app.cfg.DownloadDir, "Series")
	dl, err := database.Download(id)
	if err != nil {
		t.Fatal(err)
	}
	if dl.Name != "Series" || dl.DestPath != dest {
		t.Fatalf("download = %q at %q, want %q at %q", dl.Name, dl.DestPath, "Series", dest)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("rename created the folder (%v)", err)
	}
}

func TestRenameToSameNameClosesWithoutMoving(t *testing.T) {
	app, database, id := renameTestApp(t)
	prompt := openRename(t, app)
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if app.rename != nil {
		t.Fatalf("prompt still open: %v", prompt.errMsg)
	}
	dl, _ := database.Download(id)
	if dl.Name != "Show" || dl.DestPath != filepath.Join(app.cfg.DownloadDir, "Show") {
		t.Fatalf("download = %q at %q, want it unchanged", dl.Name, dl.DestPath)
	}
	if _, err := os.Stat(filepath.Join(dl.DestPath, "s1", "a.mkv")); err != nil {
		t.Fatalf("folder disturbed: %v", err)
	}
}

// On a case-insensitive filesystem the new path stats as an existing entry —
// the very folder being renamed — which must not read as a collision.
func TestRenameChangingOnlyCaseIsAllowed(t *testing.T) {
	app, database, id := renameTestApp(t)
	prompt := openRename(t, app)
	prompt.input.SetValue("show")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if app.rename != nil {
		t.Fatalf("prompt still open: %v", app.rename.errMsg)
	}
	dl, err := database.Download(id)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(app.cfg.DownloadDir, "show")
	if dl.Name != "show" || dl.DestPath != dest {
		t.Fatalf("download = %q at %q, want %q at %q", dl.Name, dl.DestPath, "show", dest)
	}
	if _, err := os.Stat(filepath.Join(dest, "s1", "a.mkv")); err != nil {
		t.Fatalf("file not under renamed folder: %v", err)
	}
}

func TestRenameEscapeLeavesEverythingAlone(t *testing.T) {
	app, database, id := renameTestApp(t)
	prompt := openRename(t, app)
	prompt.input.SetValue("Series")
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if app.rename != nil {
		t.Fatal("esc did not close the prompt")
	}
	if dl, _ := database.Download(id); dl.Name != "Show" {
		t.Fatalf("name = %q, want it left alone", dl.Name)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DownloadDir, "Show")); err != nil {
		t.Fatalf("original folder moved: %v", err)
	}
}
