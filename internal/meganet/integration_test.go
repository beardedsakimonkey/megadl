package meganet

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"megadl/internal/db"
	"megadl/internal/engine"
	"megadl/internal/mega"
)

// TestEngineIntegration proves the native driver is a drop-in for the
// engine: a queued folder download runs through engine.Run against the
// fake MEGA server and lands with correct statuses, byte accounting and
// file contents.
func TestEngineIntegration(t *testing.T) {
	m, episode, readme := standardTree(t)
	root := t.TempDir()
	dest := filepath.Join(root, "My Show")

	database, err := db.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	total := int64(len(episode) + len(readme))
	id, err := database.InsertDownload(&db.Download{
		URL: m.folderURL(), Handle: m.linkHandle, LinkType: "folder",
		Name: "My Show", DestPath: dest,
		Selection:  "ROOTHND1", // whole link, folder-pruned like the picker
		TotalBytes: total,
	}, []db.File{
		{NodeHandle: "FILEHND1", RemotePath: "/My Show/Season 1/e01.mkv",
			LocalPath: filepath.Join(dest, "Season 1", "e01.mkv"), Size: int64(len(episode)), Queued: true},
		{NodeHandle: "FILEHND2", RemotePath: "/My Show/readme.txt",
			LocalPath: filepath.Join(dest, "readme.txt"), Size: int64(len(readme)), Queued: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	var drv mega.Driver = m.driver() // compile-time proof of the interface
	eng := engine.New(drv, database)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	deadline := time.Now().Add(30 * time.Second)
	for {
		dl, err := database.Download(id)
		if err != nil {
			t.Fatal(err)
		}
		if dl.Status == db.StatusDone {
			break
		}
		if dl.Status == db.StatusError || time.Now().After(deadline) {
			t.Fatalf("download did not finish: %+v", dl)
		}
		time.Sleep(20 * time.Millisecond)
	}

	dl, _ := database.Download(id)
	if dl.DoneBytes != total {
		t.Errorf("done_bytes = %d, want %d", dl.DoneBytes, total)
	}
	if n, _ := database.BytesSince(time.Now().Add(-time.Minute)); n != total {
		t.Errorf("transfer_log = %d, want %d", n, total)
	}
	files, _ := database.Files(id)
	for _, f := range files {
		if f.Status != db.FileDone {
			t.Errorf("file %s status = %s, want done", f.RemotePath, f.Status)
		}
	}
	if got := mustRead(t, filepath.Join(dest, "Season 1", "e01.mkv")); !bytes.Equal(got, episode) {
		t.Error("episode content mismatch")
	}
	if got := mustRead(t, filepath.Join(dest, "readme.txt")); !bytes.Equal(got, readme) {
		t.Error("readme content mismatch")
	}
}
