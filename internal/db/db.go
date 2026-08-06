// Package db persists download history, per-file state and transfer
// accounting in a SQLite database that lives inside the download root.
package db

import (
	"database/sql"
	"errors"
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
  selected_dir     TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL DEFAULT 'queued'
                   CHECK (status IN ('queued','running','stopped','done','error','quota')),
  error            TEXT NOT NULL DEFAULT '',
  total_bytes      INTEGER NOT NULL DEFAULT 0,
  done_bytes       INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  queued_at        INTEGER NOT NULL DEFAULT 0,
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
  queued       INTEGER NOT NULL DEFAULT 1,
  -- when this file finished downloading here; 0 unless status is 'done'
  completed_at INTEGER NOT NULL DEFAULT 0
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
  id                         INTEGER PRIMARY KEY CHECK (id = 1),
  selected_download_id       INTEGER REFERENCES downloads(id) ON DELETE SET NULL,
  files_pane_selected        INTEGER NOT NULL DEFAULT 0
                             CHECK (files_pane_selected IN (0,1))
);

-- Single row (id = 1) holding queue state. Pausing belongs to the queue as a
-- whole rather than to any one download: it stops the engine from starting the
-- head, and survives restarts.
CREATE TABLE IF NOT EXISTS queue_state (
  id     INTEGER PRIMARY KEY CHECK (id = 1),
  paused INTEGER NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT ''
);

-- Links the user has added, for the add-link prompt to page back through.
-- Deliberately unrelated to downloads: deleting a download takes its folder off
-- disk, but the link that produced it is still the thing you would type next,
-- so it has no foreign key and outlives the row it came from.
-- name is what the link was added to the library as, kept for the record; the
-- download it named may be long gone.
CREATE TABLE IF NOT EXISTS link_history (
  id           INTEGER PRIMARY KEY,
  url          TEXT NOT NULL UNIQUE,
  name         TEXT NOT NULL DEFAULT '',
  submitted_at INTEGER NOT NULL
);
`

// Download statuses. The column records only terminal outcomes; whether a
// download is queued, running or paused is derived from download_files.queued
// and from what the engine is doing, so the two can never disagree.
// StatusPending is spelled 'queued' on disk because the schema's CHECK
// constraint predates this split and SQLite cannot alter a CHECK in place.
const (
	StatusPending = "queued" // nothing terminal has happened yet
	StatusDone    = "done"
	StatusError   = "error"
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
	// SelectedDir is the folder the TUI last highlighted here, relative to
	// DestPath; empty when the cursor was on a file. Folders have no row of
	// their own, so the selection is recorded against the download.
	SelectedDir string

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
	Queued     bool // in the download queue; false = user removed it
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
	// Columns dropped after the fact; a missing one means this database never
	// had it, or the schema above just created the table without it.
	//   - wanted became queue membership, renamed to queued.
	//   - the statusbar strip follows the queue now, so the file it used to
	//     remember across sessions is no longer recorded.
	for _, stmt := range []string{
		`ALTER TABLE download_files RENAME COLUMN wanted TO queued`,
		`ALTER TABLE ui_state DROP COLUMN statusbar_file_id`,
	} {
		if _, err := h.Exec(stmt); err != nil && !strings.Contains(err.Error(), "no such column") {
			h.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	// Columns added after the fact; fresh databases already have them from the
	// schema above, so a duplicate-column error means "already migrated". The
	// queued column is listed for databases old enough to predate wanted, so
	// the rename above found nothing to rename.
	for _, stmt := range []string{
		`ALTER TABLE downloads ADD COLUMN selected_file_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE downloads ADD COLUMN selected_dir TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE downloads ADD COLUMN queued_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE download_files ADD COLUMN queued INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE download_files ADD COLUMN completed_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE ui_state ADD COLUMN files_pane_selected INTEGER NOT NULL DEFAULT 0
			CHECK (files_pane_selected IN (0,1))`,
		`ALTER TABLE link_history ADD COLUMN name TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := h.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			h.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	// Downloads a previous version had stopped must not silently restart: drop
	// them out of the queue, then collapse the statuses that no longer exist
	// into StatusPending. Order matters — the first statement reads 'stopped'.
	for _, stmt := range []string{
		`UPDATE download_files SET queued = 0 WHERE download_id IN
			(SELECT id FROM downloads WHERE status = 'stopped')`,
		`UPDATE downloads SET status = 'queued' WHERE status IN ('running','stopped','quota')`,
		`UPDATE downloads SET queued_at = created_at WHERE queued_at = 0`,
		// Files that landed before this column existed have no stamp of their
		// own; their download's completion time is the closest thing on record,
		// and it keeps older files ordered behind newer ones.
		`UPDATE download_files SET completed_at = COALESCE(
			(SELECT completed_at FROM downloads WHERE id = download_id), 0)
			WHERE status = 'done' AND completed_at = 0`,
		// Link history used to be read off the library, so anything still in it
		// seeds the table. OR IGNORE keeps this idempotent, and keeps a URL the
		// table already carries at the stamp it was last submitted under.
		// The bare name is the one from the row MAX picked, i.e. what the link
		// was most recently added as. Oldest first so the ids the rows land on
		// carry the same order the stamps do.
		`INSERT OR IGNORE INTO link_history (url, name, submitted_at)
			SELECT url, name, MAX(created_at) AS ts FROM downloads
			WHERE url <> '' GROUP BY url ORDER BY ts ASC`,
	} {
		if _, err := h.Exec(stmt); err != nil {
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

	now := time.Now().Unix()
	res, err := tx.Exec(`INSERT INTO downloads
		(url, handle, link_type, name, dest_path, selection, status, total_bytes, created_at, queued_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		dl.URL, dl.Handle, dl.LinkType, dl.Name, dl.DestPath, dl.Selection,
		StatusPending, dl.TotalBytes, now, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, f := range files {
		if _, err := tx.Exec(`INSERT INTO download_files
			(download_id, node_handle, remote_path, local_path, size, queued)
			VALUES (?,?,?,?,?,?)`,
			id, f.NodeHandle, f.RemotePath, f.LocalPath, f.Size, f.Queued); err != nil {
			return 0, err
		}
	}
	// A link is remembered by the same act that adds it, so the prompt's
	// history covers exactly what was submitted, no more and no less.
	// A link submitted again is the same entry moved back to the front, not a
	// second one: the old row goes and a new id puts it at the head. Ordering
	// by id rather than the stamp keeps that exact, since two submissions can
	// land in the same second.
	if dl.URL != "" {
		if _, err := tx.Exec(`DELETE FROM link_history WHERE url = ?`, dl.URL); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(
			`INSERT INTO link_history (url, name, submitted_at) VALUES (?,?,?)`,
			dl.URL, dl.Name, now); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// LinkHistory returns the links that have been added, newest first. It is not
// scoped to the library: a deleted download leaves its link behind, since
// re-adding it is exactly what the prompt is paged back through for.
func (d *DB) LinkHistory() ([]string, error) {
	rows, err := d.sql.Query(`SELECT url FROM link_history ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			return nil, err
		}
		out = append(out, url)
	}
	return out, rows.Err()
}

// MergeFiles inserts listing rows not tracked yet (matched by node handle)
// outside the queue, so remote files added after enqueueing become visible
// without downloading themselves. Returns how many rows were added.
func (d *DB) MergeFiles(downloadID int64, files []File) (int, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	added := 0
	for _, f := range files {
		res, err := tx.Exec(`INSERT INTO download_files
			(download_id, node_handle, remote_path, local_path, size, queued)
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
		&dl.Selection, &dl.SelectedFileID, &dl.SelectedDir, &dl.Status, &dl.Error,
		&dl.TotalBytes, &dl.DoneBytes, &created, &started, &completed)
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
	selected_file_id, selected_dir, status, error, total_bytes, done_bytes,
	created_at, started_at, completed_at`

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

// inQueue matches downloads with fetching left to do: at least one file the
// user has queued that has not landed yet. Files left in 'error' don't match,
// which is what keeps a failing download from being retried forever — queueing
// it again clears the error and puts it back in.
const inQueue = `EXISTS (SELECT 1 FROM download_files
	WHERE download_id = downloads.id AND queued = 1 AND status = 'pending')`

// queueOrder is the order the engine works through the queue: whenever a
// download joined it, oldest first. Re-adding one sets queued_at again, which
// sends it to the back. MoveToFront restamps in the other direction.
const queueOrder = `ORDER BY queued_at ASC, id ASC`

// MoveToFront sends a download to the head of the queue by restamping
// queued_at ahead of everything else waiting. keepID stays in front of it —
// pass the download being fetched, since the engine does not preempt and the
// status bar and list markers read the head as the one running; pass 0 when
// nothing is running. Both stamps are rewritten so the order holds even when
// queued_at, which has one-second resolution, collides.
func (d *DB) MoveToFront(downloadID, keepID int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var waiting sql.NullInt64
	if err := tx.QueryRow(`SELECT MIN(queued_at) FROM downloads
		WHERE id NOT IN (?, ?) AND `+inQueue, downloadID, keepID).Scan(&waiting); err != nil {
		return err
	}
	front := time.Now().Unix()
	if waiting.Valid && waiting.Int64 <= front {
		front = waiting.Int64 - 1
	}
	if _, err := tx.Exec(`UPDATE downloads SET queued_at = ? WHERE id = ?`,
		front, downloadID); err != nil {
		return err
	}
	if keepID != 0 {
		if _, err := tx.Exec(`UPDATE downloads SET queued_at = ? WHERE id = ?`,
			front-1, keepID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NextQueued returns the download at the head of the queue, or nil.
func (d *DB) NextQueued() (*Download, error) {
	dls, err := d.collectDownloads(`SELECT ` + downloadCols + ` FROM downloads
		WHERE ` + inQueue + ` ` + queueOrder + ` LIMIT 1`)
	if err != nil || len(dls) == 0 {
		return nil, err
	}
	return dls[0], nil
}

// Queue returns the ids of the downloads waiting to be fetched, head first.
func (d *DB) Queue() ([]int64, error) {
	rows, err := d.sql.Query(`SELECT id FROM downloads WHERE ` + inQueue + ` ` + queueOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Paused reports whether the queue is held, and why. A paused queue keeps its
// head: nothing starts until the user resumes. The reason is empty when the
// user paused it themselves, and outlives the download process that hit it —
// which is what lets the UI still explain a quota pause afterwards.
func (d *DB) Paused() (bool, string, error) {
	var paused bool
	var reason string
	err := d.sql.QueryRow(`SELECT paused, reason FROM queue_state WHERE id = 1`).Scan(&paused, &reason)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	return paused, reason, err
}

func (d *DB) SetPaused(paused bool, reason string) error {
	_, err := d.sql.Exec(`INSERT INTO queue_state (id, paused, reason) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET paused = excluded.paused, reason = excluded.reason`,
		paused, reason)
	return err
}

func (d *DB) SetStatus(id int64, status, errMsg string) error {
	_, err := d.sql.Exec(`UPDATE downloads SET status = ?, error = ? WHERE id = ?`, status, errMsg, id)
	return err
}

// MarkStarted clears the previous run's outcome as a download starts, so a
// re-run of a done or failed download stops reading as done or failed.
func (d *DB) MarkStarted(id int64) error {
	_, err := d.sql.Exec(`UPDATE downloads SET status = ?, started_at = ?, error = '' WHERE id = ?`,
		StatusPending, time.Now().Unix(), id)
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
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
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

// SetFilesPaneSelected records whether the file pane has focus, so restoring
// the cursors also restores which of them the user was navigating.
func (d *DB) SetFilesPaneSelected(selected bool) error {
	_, err := d.sql.Exec(`INSERT INTO ui_state (id, files_pane_selected) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET files_pane_selected = excluded.files_pane_selected`, selected)
	return err
}

// FilesPaneSelected reports whether the file pane had focus in the last
// session. It is false before any view selection has been recorded.
func (d *DB) FilesPaneSelected() (bool, error) {
	var selected bool
	err := d.sql.QueryRow(`SELECT files_pane_selected FROM ui_state WHERE id = 1`).Scan(&selected)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return selected, err
}

// SetSelectedRow records what the TUI file pane is highlighting inside a
// download: a file by id (0 clears it), or a folder by its path under
// DestPath. A folder is recorded with the file it was last on left in place,
// so losing the folder still leaves somewhere sensible to land.
func (d *DB) SetSelectedRow(downloadID, fileID int64, dir string) error {
	_, err := d.sql.Exec(`UPDATE downloads SET selected_file_id = ?, selected_dir = ?
		WHERE id = ?`, fileID, dir, downloadID)
	return err
}

const fileCols = `id, download_id, node_handle, remote_path, local_path, size, status, queued`

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
			&f.LocalPath, &f.Size, &f.Status, &f.Queued); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FileCount summarises one download's file rows.
type FileCount struct {
	Total  int
	Landed int // bytes all on disk: downloaded here, or already there
}

// Complete reports whether every file the listing knows about is on disk,
// including the ones the user never queued. That is what earns a download the
// completed marker: anything less is only part of the folder.
func (c FileCount) Complete() bool { return c.Total > 0 && c.Landed == c.Total }

// FileCounts summarises every download's files in one pass, for the list
// markers.
func (d *DB) FileCounts() (map[int64]FileCount, error) {
	rows, err := d.sql.Query(`SELECT download_id, COUNT(*),
		COUNT(CASE WHEN status IN ('done','skipped') THEN 1 END)
		FROM download_files GROUP BY download_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]FileCount)
	for rows.Next() {
		var id int64
		var c FileCount
		if err := rows.Scan(&id, &c.Total, &c.Landed); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

func (d *DB) File(id int64) (*File, error) {
	var f File
	err := d.sql.QueryRow(`SELECT `+fileCols+` FROM download_files WHERE id = ?`, id).
		Scan(&f.ID, &f.DownloadID, &f.NodeHandle, &f.RemotePath,
			&f.LocalPath, &f.Size, &f.Status, &f.Queued)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// SetFileQueued adds one file to the queue or takes it out. Queueing clears a
// previous failure in the same statement: a file left in 'error' is inert, so
// without this the file would go back in the queue and never be fetched.
func (d *DB) SetFileQueued(fileID int64, queued bool) error {
	if !queued {
		_, err := d.sql.Exec(`UPDATE download_files SET queued = 0 WHERE id = ?`, fileID)
		return err
	}
	_, err := d.sql.Exec(`UPDATE download_files SET queued = 1,
		status = CASE WHEN status = 'error' THEN 'pending' ELSE status END
		WHERE id = ?`, fileID)
	return err
}

// SetDownloadQueued adds a whole download to the queue or takes it out.
// Queueing takes everything that is not already on disk — the selection made
// when the link was added is not preserved, since the user can pick files out
// again from the file pane — and stamps queued_at so the download joins the
// back of the queue.
func (d *DB) SetDownloadQueued(downloadID int64, queued bool) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if queued {
		if _, err := tx.Exec(`UPDATE download_files SET queued = 1,
			status = CASE WHEN status = 'error' THEN 'pending' ELSE status END
			WHERE download_id = ? AND status NOT IN ('done','skipped')`, downloadID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE downloads SET queued_at = ? WHERE id = ?`,
			time.Now().Unix(), downloadID); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE download_files SET queued = 0 WHERE download_id = ?`,
		downloadID); err != nil {
		return err
	}
	if err := recalcTotalBytes(tx, downloadID); err != nil {
		return err
	}
	return tx.Commit()
}

// DequeuePendingFiles drops whatever a finished run left unfetched out of the
// queue. It is the brake on retry loops: a download whose run failed partway
// leaves the queue instead of being started again immediately, and the user
// decides whether to put it back. total_bytes is deliberately left alone —
// it describes the download, not the outcome of one run.
func (d *DB) DequeuePendingFiles(downloadID int64) error {
	_, err := d.sql.Exec(`UPDATE download_files SET queued = 0
		WHERE download_id = ? AND status = 'pending'`, downloadID)
	return err
}

// RecalcTotalBytes rebases a download's total on its queued files, so progress
// and sizes track what is actually being fetched.
func (d *DB) RecalcTotalBytes(downloadID int64) error {
	return recalcTotalBytes(d.sql, downloadID)
}

func recalcTotalBytes(x interface {
	Exec(string, ...any) (sql.Result, error)
}, downloadID int64) error {
	_, err := x.Exec(`UPDATE downloads SET total_bytes = COALESCE(
		(SELECT SUM(size) FROM download_files WHERE download_id = ? AND queued = 1), 0)
		WHERE id = ?`, downloadID, downloadID)
	return err
}

func (d *DB) SetFileStatusByLocalPath(downloadID int64, localPath, status string) error {
	_, err := d.sql.Exec(`UPDATE download_files SET status = ?, completed_at = ?
		WHERE download_id = ? AND local_path = ?`,
		status, completionStamp(status), downloadID, localPath)
	return err
}

func (d *DB) SetFileStatusByHandle(downloadID int64, handle, status string) error {
	_, err := d.sql.Exec(`UPDATE download_files SET status = ?, completed_at = ?
		WHERE download_id = ? AND node_handle = ?`,
		status, completionStamp(status), downloadID, handle)
	return err
}

// completionStamp is when a file landed, and only a file that just finished
// downloading has one: any other status clears it, so completed_at never
// outlives the 'done' it describes.
func completionStamp(status string) int64 {
	if status != FileDone {
		return 0
	}
	return time.Now().Unix()
}

// LastCompletedFile is the file this app downloaded most recently, or nil when
// nothing has ever finished. Files that were already on disk are not it: they
// were never fetched, so they carry no stamp.
func (d *DB) LastCompletedFile() (*File, error) {
	var f File
	err := d.sql.QueryRow(`SELECT `+fileCols+` FROM download_files
		WHERE status = ? AND completed_at > 0
		ORDER BY completed_at DESC, id DESC LIMIT 1`, FileDone).
		Scan(&f.ID, &f.DownloadID, &f.NodeHandle, &f.RemotePath,
			&f.LocalPath, &f.Size, &f.Status, &f.Queued)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
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

// TransferBuckets splits the last window into equal slices of time and sums
// the bytes logged in each, oldest first. The series always ends at now, so
// the final bucket is the slice still filling up.
func (d *DB) TransferBuckets(window time.Duration, buckets int) ([]int64, error) {
	if window <= 0 || buckets <= 0 {
		return nil, nil
	}
	size := max(int64(window.Seconds())/int64(buckets), 1)
	start := time.Now().Unix() - size*int64(buckets)
	rows, err := d.sql.Query(`SELECT (ts - ?) / ?, SUM(bytes) FROM transfer_log
		WHERE ts > ? GROUP BY 1`, start, size, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, buckets)
	for rows.Next() {
		var idx, n int64
		if err := rows.Scan(&idx, &n); err != nil {
			return nil, err
		}
		// A row stamped exactly now divides out one past the end, as does one
		// written before a clock jumped; both belong in the slice in progress.
		out[min(max(idx, 0), int64(buckets-1))] += n
	}
	return out, rows.Err()
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
