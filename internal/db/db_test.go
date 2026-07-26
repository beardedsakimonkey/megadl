package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestDownloadLifecycle(t *testing.T) {
	d := openTest(t)

	id, err := d.InsertDownload(&Download{
		URL:    "https://mega.nz/folder/AAAAAAAA#kkkkkkkkkkkkkkkkkkkkkk",
		Handle: "AAAAAAAA", LinkType: "folder", Name: "My Show",
		DestPath: "/tmp/lib/My Show", Selection: "h1,h2", TotalBytes: 300,
	}, []File{
		{NodeHandle: "h1", RemotePath: "/Root/a.mkv", LocalPath: "/tmp/lib/My Show/a.mkv", Size: 100, Wanted: true},
		{NodeHandle: "h2", RemotePath: "/Root/b.mkv", LocalPath: "/tmp/lib/My Show/b.mkv", Size: 200, Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	next, err := d.NextQueued()
	if err != nil || next == nil || next.ID != id {
		t.Fatalf("NextQueued = %+v, %v", next, err)
	}

	d.MarkStarted(id)
	if next, _ = d.NextQueued(); next != nil {
		t.Fatalf("running download still queued")
	}

	d.SetFileStatusByLocalPath(id, "/tmp/lib/My Show/a.mkv", FileDone)
	d.SetFileStatusByHandle(id, "h2", FileError)
	files, _ := d.Files(id)
	if files[0].Status != FileDone || files[1].Status != FileError {
		t.Fatalf("file statuses = %+v", files)
	}

	d.ResetPendingFiles(id)
	files, _ = d.Files(id)
	if files[0].Status != FileDone || files[1].Status != FilePending {
		t.Fatalf("after reset = %+v", files)
	}

	d.AddDoneBytes(id, 128)
	d.MarkCompleted(id, StatusDone)
	dl, err := d.Download(id)
	if err != nil || dl.Status != StatusDone || dl.DoneBytes != 128 || dl.CompletedAt.IsZero() {
		t.Fatalf("completed = %+v, %v", dl, err)
	}

	// history hard-delete cascades to files
	if err := d.DeleteDownload(id); err != nil {
		t.Fatal(err)
	}
	if files, _ := d.Files(id); len(files) != 0 {
		t.Fatalf("cascade failed: %+v", files)
	}
}

func TestFileWanted(t *testing.T) {
	d := openTest(t)
	id, err := d.InsertDownload(&Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "n",
		DestPath: "/x/n", TotalBytes: 300,
	}, []File{
		{NodeHandle: "h1", RemotePath: "/r/a", LocalPath: "/x/n/a", Size: 100, Wanted: true},
		{NodeHandle: "h2", RemotePath: "/r/b", LocalPath: "/x/n/b", Size: 200, Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	files, _ := d.Files(id)
	if !files[0].Wanted || !files[1].Wanted {
		t.Fatalf("wanted flag not persisted: %+v", files)
	}

	d.SetFileWanted(files[1].ID, false)
	f, err := d.File(files[1].ID)
	if err != nil || f.Wanted {
		t.Fatalf("File = %+v, %v", f, err)
	}

	d.RecalcTotalBytes(id)
	if dl, _ := d.Download(id); dl.TotalBytes != 100 {
		t.Fatalf("total_bytes = %d, want 100", dl.TotalBytes)
	}

	d.SetFileWanted(files[1].ID, true)
	d.RecalcTotalBytes(id)
	if dl, _ := d.Download(id); dl.TotalBytes != 300 {
		t.Fatalf("total_bytes = %d, want 300", dl.TotalBytes)
	}
}

func TestMergeFiles(t *testing.T) {
	d := openTest(t)
	id, err := d.InsertDownload(&Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "n",
		DestPath: "/x/n", TotalBytes: 100,
	}, []File{
		{NodeHandle: "h1", RemotePath: "/r/a", LocalPath: "/x/n/a", Size: 100, Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	added, err := d.MergeFiles(id, []File{
		{NodeHandle: "h1", RemotePath: "/r/a", LocalPath: "/x/n/a", Size: 100},
		{NodeHandle: "h2", RemotePath: "/r/b", LocalPath: "/x/n/b", Size: 200},
	})
	if err != nil || added != 1 {
		t.Fatalf("MergeFiles = %d, %v", added, err)
	}

	files, _ := d.Files(id)
	if len(files) != 2 || !files[0].Wanted || files[1].Wanted {
		t.Fatalf("merged files = %+v", files)
	}
	if files[1].Status != FilePending {
		t.Fatalf("new file status = %s, want pending", files[1].Status)
	}
	// merged rows are unwanted, so the total must not move
	if dl, _ := d.Download(id); dl.TotalBytes != 100 {
		t.Fatalf("total_bytes = %d, want 100", dl.TotalBytes)
	}

	if added, err = d.MergeFiles(id, files); err != nil || added != 0 {
		t.Fatalf("re-merge = %d, %v", added, err)
	}
}

func TestMigrationAddsWantedColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE download_files (
		id INTEGER PRIMARY KEY,
		download_id INTEGER NOT NULL,
		node_handle TEXT NOT NULL,
		remote_path TEXT NOT NULL,
		local_path  TEXT NOT NULL,
		size INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending');
		INSERT INTO download_files (download_id, node_handle, remote_path, local_path, size)
		VALUES (1, 'h1', '/r/a', '/l/a', 5)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	files, err := d.Files(1)
	if err != nil || len(files) != 1 || !files[0].Wanted {
		t.Fatalf("migrated files = %+v, %v", files, err)
	}
}

func TestBootRecovery(t *testing.T) {
	d := openTest(t)
	id, _ := d.InsertDownload(&Download{
		URL: "u", Handle: "h", LinkType: "file", Name: "n", DestPath: "/x/n",
	}, nil)
	d.MarkStarted(id)
	d.ResetRunning()
	dl, _ := d.Download(id)
	if dl.Status != StatusStopped {
		t.Fatalf("status = %s, want stopped", dl.Status)
	}
}

func TestQuotaAccounting(t *testing.T) {
	d := openTest(t)

	d.LogTransfer(1000)
	d.LogTransfer(500)
	d.LogTransfer(0)  // ignored
	d.LogTransfer(-5) // ignored

	got, err := d.BytesSince(time.Now().Add(-time.Hour))
	if err != nil || got != 1500 {
		t.Fatalf("BytesSince = %d, %v", got, err)
	}

	// Entries before the rolling quota window shouldn't count.
	d.sql.Exec(`INSERT INTO transfer_log (ts, bytes) VALUES (?, ?)`,
		time.Now().Add(-25*time.Hour).Unix(), 9999)
	got, _ = d.BytesSince(time.Now().Add(-6 * time.Hour))
	if got != 1500 {
		t.Fatalf("6h window = %d, want 1500", got)
	}

	days, err := d.DailyTotals(7)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().Format("2006-01-02")
	if days[today] != 1500 {
		t.Fatalf("today total = %d (%v)", days[today], days)
	}
}

func TestFindByDestPath(t *testing.T) {
	d := openTest(t)
	d.InsertDownload(&Download{URL: "u1", Handle: "h", LinkType: "folder",
		Name: "Old", DestPath: "/lib/Old"}, nil)
	id2, _ := d.InsertDownload(&Download{URL: "u2", Handle: "h", LinkType: "folder",
		Name: "Show", DestPath: "/lib/Show"}, nil)

	dl, err := d.FindByDestPath("/lib/Show")
	if err != nil || dl == nil || dl.ID != id2 {
		t.Fatalf("FindByDestPath = %+v, %v", dl, err)
	}
	if dl, _ := d.FindByDestPath("/lib/Missing"); dl != nil {
		t.Fatalf("unexpected match: %+v", dl)
	}

	d.RenameDownload(id2, "Show2", "/lib/Show", "/lib/Show2")
	if dl, _ := d.FindByDestPath("/lib/Show2"); dl == nil || dl.Name != "Show2" {
		t.Fatalf("rename not visible: %+v", dl)
	}
}

func TestRenameDownloadCarriesFilePaths(t *testing.T) {
	d := openTest(t)
	id, err := d.InsertDownload(&Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "Show", DestPath: "/lib/Show",
	}, []File{
		{NodeHandle: "a", RemotePath: "/Show/a.mkv", LocalPath: "/lib/Show/a.mkv", Wanted: true},
		{NodeHandle: "b", RemotePath: "/Show/s1/b.mkv", LocalPath: "/lib/Show/s1/b.mkv", Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := d.RenameDownload(id, "Series", "/lib/Show", "/lib/Series"); err != nil {
		t.Fatal(err)
	}

	dl, err := d.Download(id)
	if err != nil {
		t.Fatal(err)
	}
	if dl.Name != "Series" || dl.DestPath != "/lib/Series" {
		t.Fatalf("download = %q at %q, want %q at %q", dl.Name, dl.DestPath, "Series", "/lib/Series")
	}

	files, err := d.Files(id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/lib/Series/a.mkv", "/lib/Series/s1/b.mkv"}
	for i, f := range files {
		if f.LocalPath != want[i] {
			t.Fatalf("file %d local path = %q, want %q", i, f.LocalPath, want[i])
		}
	}
}

func TestFindByResource(t *testing.T) {
	d := openTest(t)
	first, _ := d.InsertDownload(&Download{
		URL: "u1", Handle: "same", LinkType: "folder",
		Name: "Original", DestPath: "/lib/Original",
	}, nil)
	d.InsertDownload(&Download{
		URL: "u2", Handle: "same", LinkType: "folder",
		Name: "Duplicate", DestPath: "/lib/Duplicate",
	}, nil)
	d.InsertDownload(&Download{
		URL: "u3", Handle: "same", LinkType: "file",
		Name: "File", DestPath: "/lib/File",
	}, nil)

	dl, err := d.FindByResource("folder", "same")
	if err != nil || dl == nil || dl.ID != first {
		t.Fatalf("FindByResource = %+v, %v", dl, err)
	}
	if dl, err = d.FindByResource("folder", "missing"); err != nil || dl != nil {
		t.Fatalf("missing resource = %+v, %v", dl, err)
	}
	if dl, err = d.FindByResource("file", "same"); err != nil || dl == nil || dl.LinkType != "file" {
		t.Fatalf("file resource = %+v, %v", dl, err)
	}
}

func TestSelectionPersistsAndClearsWithItsDownload(t *testing.T) {
	d := openTest(t)

	id, err := d.InsertDownload(&Download{
		URL: "u", Handle: "h", LinkType: "folder", Name: "Show", DestPath: "/lib/Show",
	}, []File{
		{NodeHandle: "h1", RemotePath: "/Show/a.mkv", LocalPath: "/lib/Show/a.mkv", Wanted: true},
		{NodeHandle: "h2", RemotePath: "/Show/b.mkv", LocalPath: "/lib/Show/b.mkv", Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	if sel, err := d.SelectedDownload(); err != nil || sel != 0 {
		t.Fatalf("selection before any is recorded = %d, %v", sel, err)
	}

	files, _ := d.Files(id)
	if err := d.SetSelectedDownload(id); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSelectedFile(id, files[1].ID); err != nil {
		t.Fatal(err)
	}
	// the second write must update the single row, not add another
	if err := d.SetSelectedDownload(id); err != nil {
		t.Fatal(err)
	}

	if sel, err := d.SelectedDownload(); err != nil || sel != id {
		t.Fatalf("selected download = %d, %v, want %d", sel, err, id)
	}
	dl, err := d.Download(id)
	if err != nil || dl.SelectedFileID != files[1].ID {
		t.Fatalf("selected file = %+v, %v, want %d", dl, err, files[1].ID)
	}

	if err := d.DeleteDownload(id); err != nil {
		t.Fatal(err)
	}
	if sel, err := d.SelectedDownload(); err != nil || sel != 0 {
		t.Fatalf("selection after delete = %d, %v, want cleared", sel, err)
	}
}

func TestMigrationAddsSelectedFileColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE downloads (
		id INTEGER PRIMARY KEY,
		url TEXT NOT NULL,
		handle TEXT NOT NULL,
		link_type TEXT NOT NULL,
		name TEXT NOT NULL,
		dest_path TEXT NOT NULL,
		selection TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'queued',
		error TEXT NOT NULL DEFAULT '',
		total_bytes INTEGER NOT NULL DEFAULT 0,
		done_bytes INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		started_at INTEGER,
		completed_at INTEGER);
		INSERT INTO downloads (url, handle, link_type, name, dest_path, created_at)
		VALUES ('u', 'h', 'folder', 'Show', '/l/Show', 1)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	dl, err := d.Download(1)
	if err != nil || dl.SelectedFileID != 0 {
		t.Fatalf("migrated download = %+v, %v", dl, err)
	}
	if err := d.SetSelectedDownload(1); err != nil {
		t.Fatal(err)
	}
	if sel, err := d.SelectedDownload(); err != nil || sel != 1 {
		t.Fatalf("selection in migrated database = %d, %v", sel, err)
	}
}
