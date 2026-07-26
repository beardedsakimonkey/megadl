// Package db persists download history, per-file state and transfer
// accounting in a SQLite database that lives inside the download root.
package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS downloads (
  id               INTEGER PRIMARY KEY,
  url              TEXT NOT NULL,
  handle           TEXT NOT NULL,
  link_type        TEXT NOT NULL CHECK (link_type IN ('file','folder')),
  name             TEXT NOT NULL,
  dest_path        TEXT NOT NULL,
  selection        TEXT NOT NULL DEFAULT '',
  selected_file_id INTEGER NOT NULL DEFAULT 0,
  status           TEXT NOT NULL DEFAULT 'queued'
                   CHECK (status IN ('queued','running','stopped','done','error','quota')),
  error            TEXT NOT NULL DEFAULT '',
  total_bytes      INTEGER NOT NULL DEFAULT 0,
  done_bytes       INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  started_at       INTEGER,
  completed_at     INTEGER
);

CREATE TABLE IF NOT EXISTS download_files (
  id           INTEGER PRIMARY KEY,
  download_id  INTEGER NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
  node_handle  TEXT NOT NULL,
  remote_path  TEXT NOT NULL,
  local_path   TEXT NOT NULL,
  size         INTEGER NOT NULL,
  status       TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending','done','skipped','error')),
  wanted       INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_files_download ON download_files(download_id);

CREATE TABLE IF NOT EXISTS transfer_log (
  id    INTEGER PRIMARY KEY,
  ts    INTEGER NOT NULL,
  bytes INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_transfer_ts ON transfer_log(ts);

-- Single row (id = 1) holding view state that outlives the process. The
-- foreign key clears the selection when the download it points at is removed.
CREATE TABLE IF NOT EXISTS ui_state (
  id                   INTEGER PRIMARY KEY CHECK (id = 1),
  selected_download_id INTEGER REFERENCES downloads(id) ON DELETE SET NULL
);
`

// Download statuses.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusStopped = "stopped"
	StatusDone    = "done"
	StatusError   = "error"
	StatusQuota   = "quota"
)

// File statuses.
const (
	FilePending = "pending"
	FileDone    = "done"
	FileSkipped = "skipped"
	FileError   = "error"
)

type Download struct {
	ID             int64
	URL            string
	Handle         string
	LinkType       string // "file" | "folder"
	Name           string
	DestPath       string
	Selection      string // comma-joined selected node handles
	SelectedFileID int64  // file the TUI last highlighted here; 0 if none

	Status      string
	Error       string
	TotalBytes  int64
	DoneBytes   int64
	CreatedAt   time.Time
	StartedAt   time.Time // zero if never started
	CompletedAt time.Time // zero if not completed
}

type File struct {
	ID         int64
	DownloadID int64
	NodeHandle string
	RemotePath string
	LocalPath  string
	Size       int64
	Status     string
	Wanted     bool // false = user stopped this file; excluded from fetch
}

type DB struct {
	sql *sql.DB
}

func Open(path string) (*DB, error) {
	h, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// modernc.org/sqlite serializes writes; a single conn avoids lock races.
	h.SetMaxOpenConns(1)
	if _, err := h.Exec(schema); err != nil {
		h.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Columns added after the fact; fresh databases already have them from
	// the schema above, so a duplicate-column error means "already migrated".
	for _, stmt := range []string{
		`ALTER TABLE download_files ADD COLUMN wanted INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE downloads ADD COLUMN selected_file_id INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := h.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			h.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return &DB{sql: h}, nil
}

func (d *DB) Close() error { return d.sql.Close() }

// InsertDownload creates the download plus its file rows.
func (d *DB) InsertDownload(dl *Download, files []File) (int64, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO downloads
		(url, handle, link_type, name, dest_path, selection, status, total_bytes, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		dl.URL, dl.Handle, dl.LinkType, dl.Name, dl.DestPath, dl.Selection,
		StatusQueued, dl.TotalBytes, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, f := range files {
		if _, err := tx.Exec(`INSERT INTO download_files
			(download_id, node_handle, remote_path, local_path, size, wanted)
			VALUES (?,?,?,?,?,?)`,
			id, f.NodeHandle, f.RemotePath, f.LocalPath, f.Size, f.Wanted); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// MergeFiles inserts listing rows not tracked yet (matched by node
// handle) as unwanted, so remote files added after enqueueing become
// visible. Returns how many rows were added.
func (d *DB) MergeFiles(downloadID int64, files []File) (int, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	added := 0
	for _, f := range files {
		res, err := tx.Exec(`INSERT INTO download_files
			(download_id, node_handle, remote_path, local_path, size, wanted)
			SELECT ?,?,?,?,?,0
			WHERE NOT EXISTS (SELECT 1 FROM download_files
				WHERE download_id = ? AND node_handle = ?)`,
			downloadID, f.NodeHandle, f.RemotePath, f.LocalPath, f.Size,
			downloadID, f.NodeHandle)
		if err != nil {
			return 0, err
		}
		if n, err := res.RowsAffected(); err == nil {
			added += int(n)
		}
	}
	return added, tx.Commit()
}

func scanDownload(row interface{ Scan(...any) error }) (*Download, error) {
	var dl Download
	var created int64
	var started, completed sql.NullInt64
	err := row.Scan(&dl.ID, &dl.URL, &dl.Handle, &dl.LinkType, &dl.Name, &dl.DestPath,
		&dl.Selection, &dl.SelectedFileID, &dl.Status, &dl.Error, &dl.TotalBytes, &dl.DoneBytes,
		&created, &started, &completed)
	if err != nil {
		return nil, err
	}
	dl.CreatedAt = time.Unix(created, 0)
	if started.Valid {
		dl.StartedAt = time.Unix(started.Int64, 0)
	}
	if completed.Valid {
		dl.CompletedAt = time.Unix(completed.Int64, 0)
	}
	return &dl, nil
}

const downloadCols = `id, url, handle, link_type, name, dest_path, selection,
	selected_file_id, status, error, total_bytes, done_bytes, created_at,
	started_at, completed_at`

func (d *DB) collectDownloads(query string, args ...any) ([]*Download, error) {
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Download
	for rows.Next() {
		dl, err := scanDownload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// Downloads returns every download, newest first.
func (d *DB) Downloads() ([]*Download, error) {
	return d.collectDownloads(`SELECT ` + downloadCols + ` FROM downloads ORDER BY created_at DESC, id DESC`)
}

func (d *DB) Download(id int64) (*Download, error) {
	return scanDownload(d.sql.QueryRow(`SELECT `+downloadCols+` FROM downloads WHERE id = ?`, id))
}

// FindByResource returns the original library entry for a MEGA resource.
// Handles identify the listed root node; link type keeps the file and folder
// namespaces distinct.
func (d *DB) FindByResource(linkType, handle string) (*Download, error) {
	dls, err := d.collectDownloads(`SELECT `+downloadCols+` FROM downloads
		WHERE link_type = ? AND handle = ? ORDER BY created_at ASC, id ASC LIMIT 1`,
		linkType, handle)
	if err != nil || len(dls) == 0 {
		return nil, err
	}
	return dls[0], nil
}

// NextQueued returns the oldest queued download, or nil.
func (d *DB) NextQueued() (*Download, error) {
	dls, err := d.collectDownloads(`SELECT ` + downloadCols + ` FROM downloads
		WHERE status = 'queued' ORDER BY created_at ASC, id ASC LIMIT 1`)
	if err != nil || len(dls) == 0 {
		return nil, err
	}
	return dls[0], nil
}

func (d *DB) SetStatus(id int64, status, errMsg string) error {
	_, err := d.sql.Exec(`UPDATE downloads SET status = ?, error = ? WHERE id = ?`, status, errMsg, id)
	return err
}

func (d *DB) MarkStarted(id int64) error {
	_, err := d.sql.Exec(`UPDATE downloads SET status = ?, started_at = ?, error = '' WHERE id = ?`,
		StatusRunning, time.Now().Unix(), id)
	return err
}

func (d *DB) MarkCompleted(id int64, status string) error {
	_, err := d.sql.Exec(`UPDATE downloads SET status = ?, completed_at = ? WHERE id = ?`,
		status, time.Now().Unix(), id)
	return err
}

func (d *DB) AddDoneBytes(id, delta int64) error {
	_, err := d.sql.Exec(`UPDATE downloads SET done_bytes = done_bytes + ? WHERE id = ?`, delta, id)
	return err
}

func (d *DB) DeleteDownload(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM downloads WHERE id = ?`, id)
	return err
}

// RenameDownload gives a download a new name and destination, carrying its
// recorded local file paths along without touching files on disk.
func (d *DB) RenameDownload(id int64, name, oldDestPath, newDestPath string) error {
	return d.rebaseDownload(id, name, oldDestPath, newDestPath)
}

// RebaseDownloadPath moves a download's destination and all of its recorded
// local file paths to a new root without touching files on disk.
func (d *DB) RebaseDownloadPath(id int64, oldDestPath, newDestPath string) error {
	return d.rebaseDownload(id, "", oldDestPath, newDestPath)
}

// rebaseDownload repoints dest_path and every recorded local_path from
// oldDestPath to newDestPath, and renames the download when name is non-empty.
func (d *DB) rebaseDownload(id int64, name, oldDestPath, newDestPath string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id, local_path FROM download_files WHERE download_id = ?`, id)
	if err != nil {
		return err
	}
	type localFile struct {
		id   int64
		path string
	}
	var files []localFile
	for rows.Next() {
		var file localFile
		if err := rows.Scan(&file.id, &file.path); err != nil {
			rows.Close()
			return err
		}
		files = append(files, file)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, file := range files {
		rel, err := filepath.Rel(oldDestPath, file.path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("file path %q is outside download destination %q", file.path, oldDestPath)
		}
		if _, err := tx.Exec(`UPDATE download_files SET local_path = ? WHERE id = ?`,
			filepath.Join(newDestPath, rel), file.id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE downloads SET dest_path = ? WHERE id = ?`, newDestPath, id); err != nil {
		return err
	}
	if name != "" {
		if _, err := tx.Exec(`UPDATE downloads SET name = ? WHERE id = ?`, name, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FindByDestPath matches a library entry back to its download row.
func (d *DB) FindByDestPath(destPath string) (*Download, error) {
	dls, err := d.collectDownloads(`SELECT `+downloadCols+` FROM downloads
		WHERE dest_path = ? ORDER BY created_at DESC LIMIT 1`, destPath)
	if err != nil || len(dls) == 0 {
		return nil, err
	}
	return dls[0], nil
}

// SetSelectedDownload records the download the TUI cursor is on, so the next
// session opens on the same row.
func (d *DB) SetSelectedDownload(id int64) error {
	_, err := d.sql.Exec(`INSERT INTO ui_state (id, selected_download_id) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET selected_download_id = excluded.selected_download_id`, id)
	return err
}

// SelectedDownload returns the download recorded by SetSelectedDownload, or 0
// when there is none — including when that download has since been removed.
func (d *DB) SelectedDownload() (int64, error) {
	var id sql.NullInt64
	err := d.sql.QueryRow(`SELECT selected_download_id FROM ui_state WHERE id = 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id.Int64, err
}

// SetSelectedFile records the file the TUI file pane is highlighting inside a
// download; 0 clears it.
func (d *DB) SetSelectedFile(downloadID, fileID int64) error {
	_, err := d.sql.Exec(`UPDATE downloads SET selected_file_id = ? WHERE id = ?`, fileID, downloadID)
	return err
}

// ResetRunning is boot recovery: anything left 'running' by a previous
// process becomes 'stopped' (resumable).
func (d *DB) ResetRunning() error {
	_, err := d.sql.Exec(`UPDATE downloads SET status = ? WHERE status = ?`, StatusStopped, StatusRunning)
	return err
}

const fileCols = `id, download_id, node_handle, remote_path, local_path, size, status, wanted`

func (d *DB) Files(downloadID int64) ([]File, error) {
	rows, err := d.sql.Query(`SELECT `+fileCols+`
		FROM download_files WHERE download_id = ? ORDER BY remote_path`, downloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.DownloadID, &f.NodeHandle, &f.RemotePath,
			&f.LocalPath, &f.Size, &f.Status, &f.Wanted); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PartialDownloads reports which downloads still have files that never landed
// on disk — deselected, pending or errored. A finished download in that set
// only covers part of its folder, which the list marks differently from one
// that mirrors the folder completely.
func (d *DB) PartialDownloads() (map[int64]bool, error) {
	rows, err := d.sql.Query(`SELECT DISTINCT download_id FROM download_files
		WHERE status NOT IN (?, ?)`, FileDone, FileSkipped)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (d *DB) File(id int64) (*File, error) {
	var f File
	err := d.sql.QueryRow(`SELECT `+fileCols+` FROM download_files WHERE id = ?`, id).
		Scan(&f.ID, &f.DownloadID, &f.NodeHandle, &f.RemotePath,
			&f.LocalPath, &f.Size, &f.Status, &f.Wanted)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (d *DB) SetFileWanted(fileID int64, wanted bool) error {
	_, err := d.sql.Exec(`UPDATE download_files SET wanted = ? WHERE id = ?`, wanted, fileID)
	return err
}

// RecalcTotalBytes rebases a download's total on its wanted files, so
// progress and sizes track the effective selection.
func (d *DB) RecalcTotalBytes(downloadID int64) error {
	_, err := d.sql.Exec(`UPDATE downloads SET total_bytes = COALESCE(
		(SELECT SUM(size) FROM download_files WHERE download_id = ? AND wanted = 1), 0)
		WHERE id = ?`, downloadID, downloadID)
	return err
}

func (d *DB) SetFileStatusByLocalPath(downloadID int64, localPath, status string) error {
	_, err := d.sql.Exec(`UPDATE download_files SET status = ? WHERE download_id = ? AND local_path = ?`,
		status, downloadID, localPath)
	return err
}

func (d *DB) SetFileStatusByHandle(downloadID int64, handle, status string) error {
	_, err := d.sql.Exec(`UPDATE download_files SET status = ? WHERE download_id = ? AND node_handle = ?`,
		status, downloadID, handle)
	return err
}

// ResetPendingFiles rewinds non-terminal file states before a (re)run.
func (d *DB) ResetPendingFiles(downloadID int64) error {
	_, err := d.sql.Exec(`UPDATE download_files SET status = 'pending'
		WHERE download_id = ? AND status = 'error'`, downloadID)
	return err
}

// LogTransfer appends downloaded-bytes deltas for quota accounting.
func (d *DB) LogTransfer(bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	_, err := d.sql.Exec(`INSERT INTO transfer_log (ts, bytes) VALUES (?, ?)`, time.Now().Unix(), bytes)
	return err
}

// BytesSince sums transfer_log entries newer than t.
func (d *DB) BytesSince(t time.Time) (int64, error) {
	var n sql.NullInt64
	err := d.sql.QueryRow(`SELECT SUM(bytes) FROM transfer_log WHERE ts > ?`, t.Unix()).Scan(&n)
	return n.Int64, err
}

// DailyTotals returns per-local-day byte totals for the last `days` days.
func (d *DB) DailyTotals(days int) (map[string]int64, error) {
	rows, err := d.sql.Query(`SELECT date(ts, 'unixepoch', 'localtime') AS day, SUM(bytes)
		FROM transfer_log WHERE ts > ? GROUP BY day`,
		time.Now().AddDate(0, 0, -days).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var day string
		var n int64
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day] = n
	}
	return out, rows.Err()
}

// PruneTransferLog drops accounting rows older than 30 days.
func (d *DB) PruneTransferLog() error {
	_, err := d.sql.Exec(`DELETE FROM transfer_log WHERE ts < ?`,
		time.Now().AddDate(0, 0, -30).Unix())
	return err
}
