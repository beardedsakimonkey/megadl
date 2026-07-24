package library

import (
	"os"
	"path/filepath"
	"testing"

	"megadl/internal/db"
)

func TestSyncRebasesPathsBeforePruning(t *testing.T) {
	root := t.TempDir()
	database, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	oldRoot := filepath.Join(root, "old")
	newRoot := filepath.Join(root, "new")
	if err := os.Mkdir(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	missingDoneID := insertDownload(t, database, filepath.Join(oldRoot, "missing-done"))
	if err := database.MarkCompleted(missingDoneID, db.StatusDone); err != nil {
		t.Fatal(err)
	}

	presentDonePath := filepath.Join(newRoot, "present-done")
	if err := os.Mkdir(presentDonePath, 0o755); err != nil {
		t.Fatal(err)
	}
	presentDoneID := insertDownload(t, database, filepath.Join(oldRoot, "present-done"))
	if err := database.MarkCompleted(presentDoneID, db.StatusDone); err != nil {
		t.Fatal(err)
	}

	missingQueuedID := insertDownload(t, database, filepath.Join(oldRoot, "missing-queued"))

	pruned, err := Sync(newRoot, database)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}

	downloads, err := database.Downloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 2 {
		t.Fatalf("downloads = %+v, want 2 entries", downloads)
	}
	remaining := map[int64]bool{}
	for _, download := range downloads {
		remaining[download.ID] = true
	}
	if remaining[missingDoneID] {
		t.Error("missing completed download was retained")
	}
	if !remaining[presentDoneID] {
		t.Error("present completed download was pruned")
	}
	if !remaining[missingQueuedID] {
		t.Error("missing queued download was pruned")
	}

	presentDone, err := database.Download(presentDoneID)
	if err != nil {
		t.Fatal(err)
	}
	if presentDone.DestPath != presentDonePath {
		t.Errorf("destination = %q, want %q", presentDone.DestPath, presentDonePath)
	}
	files, err := database.Files(presentDoneID)
	if err != nil {
		t.Fatal(err)
	}
	wantFilePath := filepath.Join(presentDonePath, "episode.mkv")
	if len(files) != 1 || files[0].LocalPath != wantFilePath {
		t.Errorf("files = %+v, want local path %q", files, wantFilePath)
	}
}

func insertDownload(t *testing.T, database *db.DB, destPath string) int64 {
	t.Helper()
	id, err := database.InsertDownload(&db.Download{
		URL:      "https://mega.nz/folder/AAAAAAAA#kkkkkkkkkkkkkkkkkkkkkk",
		Handle:   filepath.Base(destPath),
		LinkType: "folder",
		Name:     filepath.Base(destPath),
		DestPath: destPath,
	}, []db.File{{
		NodeHandle: "file",
		RemotePath: "/episode.mkv",
		LocalPath:  filepath.Join(destPath, "episode.mkv"),
		Size:       1,
		Wanted:     true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
