package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"megadl/internal/db"
	"megadl/internal/engine"
)

// deleteTestApp builds an app around one folder download whose destination
// really exists on disk, holding a finished file and a partial, with the
// cursor on its row.
func deleteTestApp(t *testing.T) (*App, *db.DB, string) {
	t.Helper()
	app, database := openAddlinkTestApp(t)
	app.eng = engine.New(nil, database)

	dest := filepath.Join(app.cfg.DownloadDir, "Folder")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.mkv", ".megatmp.b"} {
		if err := os.WriteFile(filepath.Join(dest, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.InsertDownload(&db.Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "Folder", DestPath: dest,
	}, []db.File{
		{NodeHandle: "a", RemotePath: "/Folder/a.mkv",
			LocalPath: filepath.Join(dest, "a.mkv"), Status: db.FileDone, Queued: true},
		{NodeHandle: "b", RemotePath: "/Folder/b.mkv",
			LocalPath: filepath.Join(dest, "b.mkv"), Queued: true},
	}); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()
	return app, database, dest
}

// openDelete presses d and returns the dialog it opened.
func openDelete(t *testing.T, app *App) *deleteModel {
	t.Helper()
	typeKeys(app, "d")
	if app.del == nil {
		t.Fatal("d did not open the delete dialog")
	}
	return app.del
}

func TestDeleteConfirmedRemovesTheFolderFromDiskAndTheLibrary(t *testing.T) {
	app, database, dest := deleteTestApp(t)
	openDelete(t, app)

	typeKeys(app, "y")

	if app.del != nil {
		t.Fatal("delete dialog is still open")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) err = %v, want not-exist", dest, err)
	}
	rows, err := database.Downloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("library still has %d downloads, want 0", len(rows))
	}
}

func TestDeleteCancelledKeepsTheDownloadAndItsFiles(t *testing.T) {
	app, database, dest := deleteTestApp(t)
	openDelete(t, app)

	typeKeys(app, "n")

	if app.del != nil {
		t.Fatal("delete dialog is still open")
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("cancelling removed the folder from disk: %v", err)
	}
	rows, err := database.Downloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("library has %d downloads, want 1", len(rows))
	}
}

// A file link's destination is the file itself, so its .megatmp partial sits
// beside it in the library root rather than inside the destination.
func TestDeleteRemovesAFileLinkPartial(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	app.eng = engine.New(nil, database)
	dest := filepath.Join(app.cfg.DownloadDir, "movie.mkv")
	tmp := filepath.Join(app.cfg.DownloadDir, ".megatmp.h")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertDownload(&db.Download{
		URL: "u", Handle: "h", LinkType: "file", Name: "movie.mkv", DestPath: dest,
	}, []db.File{
		{NodeHandle: "h", RemotePath: "/movie.mkv", LocalPath: dest, Queued: true},
	}); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()
	openDelete(t, app)

	typeKeys(app, "y")

	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) err = %v, want not-exist", tmp, err)
	}
}

func TestDeleteRefusesTheLibraryRoot(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	m := newDeleteModel(app, &db.Download{Name: "Root", DestPath: app.cfg.DownloadDir})

	if err := m.deleteFromDisk(); err == nil {
		t.Fatal("deleteFromDisk on the library root returned no error")
	}
	if _, err := os.Stat(app.cfg.DownloadDir); err != nil {
		t.Fatalf("library root was removed: %v", err)
	}
}

// The dialog names the destination it is about to remove, so confirming is
// never a guess about what leaves the disk.
func TestDeleteDialogNamesTheDestination(t *testing.T) {
	app, _, dest := deleteTestApp(t)
	view := ansi.Strip(openDelete(t, app).view())

	for _, want := range []string{"Folder", truncateMiddle(dest, 60)} {
		if !strings.Contains(view, want) {
			t.Fatalf("delete dialog = %q, want it to mention %q", view, want)
		}
	}
}
