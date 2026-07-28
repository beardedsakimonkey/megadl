package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"megadl/internal/db"
	"megadl/internal/engine"
)

type paneID int

const (
	paneList paneID = iota
	paneFiles
)

type downloadsModel struct {
	app    *App
	rows   []*db.Download
	cursor int
	scroll int

	pane     paneID
	files    []db.File
	filesFor int64 // download ID the file pane is showing
	// tree is what the file pane draws: directory headers interleaved with the
	// files under them. The cursor indexes it rather than files, so a folder is
	// focusable in its own right. setFiles rebuilds both together.
	tree       []fileTreeRow
	treeCursor int
	treeScroll int
	// cursorDir is the path of the directory the cursor is on, empty when it is
	// on a file. Directories have no row of their own, so this is what puts the
	// cursor back when a reload rebuilds the tree; it is recorded against the
	// download as SelectedDir, the way a file is as SelectedFileID, so a folder
	// keeps the cursor across downloads and across restarts too.
	cursorDir string
	// partials caches each unfinished file's on-disk .megatmp size (keyed
	// by file ID) so stopped files keep their progress bar. loadFiles
	// refreshes it so View never touches the filesystem.
	partials map[int64]int64
	// fileCounts says how much of each download's folder is on disk, so a row
	// can tell "all of it is here" from "part of it is". Refreshed by reload.
	fileCounts map[int64]db.FileCount
	// queuePos maps a download to its place in the queue. Position 0 is the
	// head: the download being fetched, or the one a pause is holding at.
	// Refreshed by reload.
	queuePos map[int64]int
	// head is the queue's next unit of work, which is what the status bar
	// draws. Refreshed by reload.
	head queueHead
	// savedDownload is the row last written to the database, so repeated
	// events on the same row don't rewrite it.
	savedDownload int64
	// savedPane is the focus last written to the database, for the same reason.
	savedPane paneID

	confirmRemove bool // pending x confirmation for rows[cursor]
	refreshing    bool // remote listing fetch in flight
	notice        string
	// quotaDismissed remembers that esc hid the quota banner. It covers the
	// one stall that was showing then: the engine keeps reporting a stall
	// until bytes move again, and once they do the next stall is news and
	// speaks up. reload clears it.
	quotaDismissed bool

	// Pane geometry recorded by view so mouse events can be mapped back
	// onto rows; coordinates are relative to the top-left of the body.
	listW, filesW, paneHeight int
	clicks                    clickTracker

	// openFile plays a downloaded file plus any queued playlist entries;
	// nil means openInMPV. Test seam so tests never spawn a real player.
	openFile func(paths []string) error
}

// listingMergedMsg reports a finished remote-listing refresh.
type listingMergedMsg struct {
	added int
	err   error
}

// fileOpenedMsg reports the result of spawning a player for a file.
type fileOpenedMsg struct {
	name   string
	queued int // sibling files autoplaying after it
	err    error
}

func newDownloadsModel(app *App) downloadsModel {
	return downloadsModel{app: app}
}

// restore opens the view on the download and pane the last session left
// selected; its file cursor follows from the download's own recorded
// selection. An unknown or removed download falls back to the top of the list.
func (m *downloadsModel) restore() {
	m.reload()
	id, err := m.app.db.SelectedDownload()
	if err != nil || id == 0 {
		return
	}
	for i, dl := range m.rows {
		if dl.ID == id {
			m.cursor, m.savedDownload = i, id
			m.loadFiles()
			if selected, err := m.app.db.FilesPaneSelected(); err == nil && selected && len(m.files) > 0 {
				m.pane = paneFiles
			}
			m.savedPane = m.pane
			return
		}
	}
}

func (m *downloadsModel) reload() {
	rows, err := m.app.db.Downloads()
	if err == nil {
		m.rows = rows
	}
	if counts, err := m.app.db.FileCounts(); err == nil {
		m.fileCounts = counts
	}
	if queue, err := m.app.db.Queue(); err == nil {
		m.queuePos = make(map[int64]int, len(queue))
		for i, id := range queue {
			m.queuePos[id] = i
		}
		m.loadHead(queue)
	}
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
	if m.app.eng != nil && !m.app.eng.Snapshot().QuotaStalled {
		// nothing is being throttled any more, so there is no dismissal left
		// to honour; the banner is already gone on its own
		m.quotaDismissed = false
	}
	m.loadFiles()
}

// queueHead is the queue's next unit of work: the download at the front and the
// file inside it that the engine is fetching, or will pick up next. Its file is
// nil when nothing is queued.
type queueHead struct {
	dl      *db.Download
	file    *db.File
	partial int64 // bytes of file already on disk
}

// loadHead works out what the front of the queue is going to fetch, given the
// queue head first. The engine takes a download's files in listing order, so
// that is its first queued file still waiting — the one being fetched now, or
// the one a paused queue is holding at. The partial is stat'ed here so View
// never touches the filesystem.
func (m *downloadsModel) loadHead(queue []int64) {
	m.head = queueHead{}
	if len(queue) == 0 {
		return
	}
	var dl *db.Download
	for _, row := range m.rows {
		if row.ID == queue[0] {
			dl = row
			break
		}
	}
	if dl == nil {
		return
	}
	files, err := m.app.db.Files(dl.ID)
	if err != nil {
		return
	}
	for _, f := range files {
		if !f.Queued || f.Status != db.FilePending {
			continue
		}
		m.head = queueHead{dl: dl, file: &f, partial: partialSizes([]db.File{f})[f.ID]}
		return
	}
}

// selectDownload focuses an existing library row after another flow reuses it.
func (m *downloadsModel) selectDownload(id int64) {
	m.reload()
	for i, dl := range m.rows {
		if dl.ID != id {
			continue
		}
		m.cursor = i
		m.pane = paneList
		m.loadFiles()
		m.rememberSelection()
		return
	}
}

// loadFiles refreshes the file pane for the download under the cursor.
func (m *downloadsModel) loadFiles() {
	if len(m.rows) == 0 {
		m.files, m.tree, m.filesFor, m.partials = nil, nil, 0, nil
		m.pane = paneList
		return
	}
	dl := m.rows[m.cursor]
	changedDownload := dl.ID != m.filesFor
	m.rememberFileCursor()
	files, err := m.app.db.Files(dl.ID)
	if err != nil {
		return
	}
	if changedDownload {
		// the incoming download's own remembered row, folder included
		m.treeCursor, m.treeScroll, m.cursorDir = 0, 0, dl.SelectedDir
	}
	m.setFiles(dl, files)
	m.focusRow(dl.SelectedFileID)
	if len(m.files) == 0 {
		m.pane = paneList
	}
}

// setFiles gives the file pane its contents: the files, the tree of directory
// headers the pane draws them in, and the partial sizes their progress bars
// read.
func (m *downloadsModel) setFiles(dl *db.Download, files []db.File) {
	m.filesFor, m.files = dl.ID, files
	m.tree = fileTreeRows(files, dl.DestPath)
	m.partials = partialSizes(files)
}

// focusRow puts the cursor back where the pane was once the tree is rebuilt:
// on the folder it was in, or — when it was on a file, or that folder is no
// longer in the listing — on the download's remembered file. Neither being
// there leaves the cursor wherever the new tree can hold it.
func (m *downloadsModel) focusRow(fileID int64) {
	file := -1
	for i, r := range m.tree {
		if r.dir != "" {
			if m.cursorDir != "" && r.path == m.cursorDir {
				m.treeCursor = i
				return
			}
			continue
		}
		if file < 0 && m.files[r.file].ID == fileID {
			file = i
		}
	}
	m.cursorDir = "" // whatever folder was remembered, it is not here
	if file >= 0 {
		m.treeCursor = file
		return
	}
	m.treeCursor = min(max(m.treeCursor, 0), max(0, len(m.tree)-1))
}

// cursorFile is the index into files of the file the pane's cursor is on, or
// -1 when it is on a directory header.
func (m *downloadsModel) cursorFile() int {
	if m.treeCursor < 0 || m.treeCursor >= len(m.tree) || m.tree[m.treeCursor].dir != "" {
		return -1
	}
	return m.tree[m.treeCursor].file
}

// partialSizes stats each unfinished file's .megatmp partial so files that
// are stopped or errored still show how much is already on disk.
func partialSizes(files []db.File) map[int64]int64 {
	sizes := make(map[int64]int64)
	for _, f := range files {
		if f.Status == db.FileDone || f.Status == db.FileSkipped {
			continue
		}
		tmp := filepath.Join(filepath.Dir(f.LocalPath), ".megatmp."+f.NodeHandle)
		if st, err := os.Stat(tmp); err == nil && st.Size() > 0 {
			sizes[f.ID] = st.Size()
		}
	}
	return sizes
}

// rememberSelection persists the cursor — the highlighted download, and the
// file highlighted inside it — so the next session reopens where this one left
// off. It runs after every event; writes that would change nothing are
// skipped, so cursor keys don't hit the database on every press.
func (m *downloadsModel) rememberSelection() {
	m.rememberFileCursor()
	if m.cursor >= len(m.rows) {
		return
	}
	if id := m.rows[m.cursor].ID; id != m.savedDownload {
		m.savedDownload = id
		m.app.db.SetSelectedDownload(id)
	}
	if m.pane != m.savedPane {
		m.savedPane = m.pane
		m.app.db.SetFilesPaneSelected(m.pane == paneFiles)
	}
}

// rememberFileCursor records where the file pane is focused so a reload — or
// the next session — can put the cursor back. A folder is recorded as a path,
// a file as its id, and a folder leaves the remembered file in place so there
// is still somewhere to land if the folder goes away. Both are recorded
// against the download the pane is showing, which is not always the one under
// the list cursor: loadFiles calls this to capture the outgoing selection as
// the panes switch downloads.
func (m *downloadsModel) rememberFileCursor() {
	if m.filesFor == 0 || m.treeCursor < 0 || m.treeCursor >= len(m.tree) {
		return
	}
	row := m.tree[m.treeCursor]
	m.cursorDir = row.path // set on directory rows only
	for _, dl := range m.rows {
		if dl.ID != m.filesFor {
			continue
		}
		fileID := dl.SelectedFileID
		if row.dir == "" {
			fileID = m.files[row.file].ID
		}
		if dl.SelectedFileID == fileID && dl.SelectedDir == m.cursorDir {
			return // already recorded
		}
		dl.SelectedFileID, dl.SelectedDir = fileID, m.cursorDir
		m.app.db.SetSelectedRow(m.filesFor, fileID, m.cursorDir)
		return
	}
}

func (m *downloadsModel) update(msg tea.Msg) tea.Cmd {
	cmd := m.handle(msg)
	m.rememberSelection()
	return cmd
}

func (m *downloadsModel) handle(msg tea.Msg) tea.Cmd {
	if res, ok := msg.(listingMergedMsg); ok {
		m.refreshing = false
		if res.err != nil {
			m.notice = "refresh failed: " + res.err.Error()
		} else {
			m.reload()
			m.notice = fmt.Sprintf("listing refreshed — %d new file(s)", res.added)
		}
		return nil
	}

	if res, ok := msg.(fileOpenedMsg); ok {
		switch {
		case res.err != nil:
			m.notice = "play failed: " + res.err.Error()
		case res.queued > 0:
			m.notice = fmt.Sprintf("playing %s (+%d queued)", res.name, res.queued)
		default:
			m.notice = "playing " + res.name
		}
		return nil
	}

	if mouse, ok := msg.(tea.MouseMsg); ok {
		return m.mouse(mouse)
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if m.confirmRemove {
		m.confirmRemove, m.notice = false, ""
		if key.String() == "y" || key.String() == "Y" {
			if m.cursor < len(m.rows) {
				m.app.db.DeleteDownload(m.rows[m.cursor].ID)
				m.reload()
			}
		}
		return nil
	}

	// esc only silences the detail strip: it never moves the selection, so
	// dropping a message can't cost you your place. Every other key clears
	// the notice as a side effect of acting.
	if key.String() == "esc" {
		m.dismissDetail()
		return nil
	}
	m.notice = ""

	if key.String() == "R" {
		return m.refreshListing()
	}

	if key.String() == "f" {
		m.focusHead()
		return nil
	}

	if m.pane == paneFiles {
		switch key.String() {
		case "up", "k":
			if m.treeCursor > 0 {
				m.treeCursor--
			}
		case "down", "j":
			if m.treeCursor < len(m.tree)-1 {
				m.treeCursor++
			}
		case "K":
			m.treeCursor = siblingRow(m.tree, m.treeCursor, -1)
		case "J":
			m.treeCursor = siblingRow(m.tree, m.treeCursor, 1)
		case "z":
			m.centerTreeCursor()
		case "enter":
			m.toggleRow()
		case "o":
			return m.openSelectedFile()
		case "r":
			return m.startRename()
		case "left", "h":
			m.pane = paneList
		}
		return nil
	}

	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.loadFiles()
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
			m.loadFiles()
		}
	case "enter":
		m.toggleDownload()
	case "o":
		return m.openSelectedDownload()
	case "right", "l":
		if len(m.files) > 0 {
			m.pane = paneFiles
		}
	case "r":
		return m.startRename()
	case "d":
		if m.cursor < len(m.rows) {
			if m.rows[m.cursor].ID == m.app.eng.ActiveID() {
				m.notice = "stop the download before removing it"
				break
			}
			m.confirmRemove = true
		}
	case "y":
		if m.cursor < len(m.rows) {
			m.copyURL(m.rows[m.cursor].URL)
		}
	}
	return nil
}

// focusHead moves the cursor to what the queue is working on: the download at
// the front, and the file inside it being fetched or held at. A current file
// is selected in the files pane regardless of which pane the jump started in.
func (m *downloadsModel) focusHead() {
	if m.head.dl == nil {
		m.notice = "queue is empty"
		return
	}
	for i, dl := range m.rows {
		if dl.ID != m.head.dl.ID {
			continue
		}
		if i != m.cursor {
			m.cursor = i
			m.loadFiles()
		}
		if m.head.file != nil {
			// the head's file, not the download's remembered one: landing on
			// the running file is the point of the jump
			m.cursorDir = ""
			m.focusRow(m.head.file.ID)
			m.pane = paneFiles
		}
		return
	}
}

// dismissDetail hides what the detail strip is saying right now. A selected
// row's error is left alone: it describes the row rather than the moment, and
// it goes away with the selection.
func (m *downloadsModel) dismissDetail() {
	m.notice = ""
	if m.app == nil || m.app.eng == nil {
		return
	}
	if m.app.eng.Snapshot().QuotaStalled {
		m.quotaDismissed = true
	}
}

// queued reports whether dl is in the download queue, and so whether removing
// it is the meaningful half of the enter toggle for it and its files.
func (m *downloadsModel) queued(dl *db.Download) bool {
	_, ok := m.queuePos[dl.ID]
	return ok
}

// atHead reports whether dl is the front of the queue: the download being
// fetched, or the one a paused queue is holding at.
func (m *downloadsModel) atHead(dl *db.Download) bool {
	pos, ok := m.queuePos[dl.ID]
	return ok && pos == 0
}

// complete reports whether every file of dl is on disk, the ones the user
// never queued included.
func (m *downloadsModel) complete(dl *db.Download) bool {
	return m.fileCounts[dl.ID].Complete()
}

// toggleDownload takes the selected download out of the queue if it is in it,
// and puts it in otherwise.
func (m *downloadsModel) toggleDownload() {
	if m.cursor >= len(m.rows) {
		return
	}
	dl := m.rows[m.cursor]
	switch {
	case m.queued(dl):
		m.app.eng.Dequeue(dl.ID)
	case m.complete(dl):
		m.notice = "download already complete"
	default:
		m.app.eng.Enqueue(dl.ID)
		m.notePaused()
	}
}

// toggleRow acts on what the file pane's cursor is on: one file, or every
// eligible file in the folder under a directory header.
func (m *downloadsModel) toggleRow() {
	if m.treeCursor >= len(m.tree) || m.cursor >= len(m.rows) {
		return
	}
	if m.tree[m.treeCursor].dir != "" {
		m.toggleFolder()
		return
	}
	m.toggleFile()
}

// eligibleFiles returns the files under tree row i the queue can still act on:
// the row's own file, or everything in the folder it heads, minus whatever is
// already on disk — there is nothing left to queue or dequeue for those.
func (m *downloadsModel) eligibleFiles(i int) []db.File {
	var files []db.File
	for _, idx := range subtreeFiles(m.tree, i) {
		f := m.files[idx]
		if f.Status == db.FileDone || f.Status == db.FileSkipped {
			continue
		}
		files = append(files, f)
	}
	return files
}

// allQueued reports whether every one of files is waiting in the queue, which
// is what makes the s toggle on a folder take them out rather than put them in.
func allQueued(files []db.File) bool {
	for _, f := range files {
		if !f.Queued {
			return false
		}
	}
	return len(files) > 0
}

// toggleFolder queues every eligible file in the folder under the cursor, or
// takes them all out again once they are all waiting. It reaches into
// subfolders, so a season with extras under it goes in as one unit.
func (m *downloadsModel) toggleFolder() {
	files := m.eligibleFiles(m.treeCursor)
	if len(files) == 0 {
		m.notice = "folder already downloaded"
		return
	}
	ids := make([]int64, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	if allQueued(files) {
		m.app.eng.DequeueFiles(ids)
	} else {
		m.app.eng.QueueFiles(ids)
		m.notePaused()
	}
	m.reload()
}

// toggleFile takes the selected file out of the queue if it is in it, and puts
// it in otherwise — which puts its download back in the queue too.
func (m *downloadsModel) toggleFile() {
	i := m.cursorFile()
	if i < 0 || m.cursor >= len(m.rows) {
		return
	}
	f := m.files[i]
	switch {
	case f.Status == db.FileDone || f.Status == db.FileSkipped:
		m.notice = "file already downloaded"
	case f.Queued:
		m.app.eng.DequeueFile(f.ID)
		m.reload()
	default:
		m.app.eng.QueueFile(f.ID)
		m.notePaused()
		m.reload()
	}
}

// notePaused explains why a download just added to the queue isn't moving.
// Queueing deliberately doesn't release the pause: holding the queue is the
// user's decision, not a side effect of picking files.
func (m *downloadsModel) notePaused() {
	if m.app.eng.Paused() {
		m.notice = "queued — the download queue is paused, press p/space to resume"
	}
}

// startRename opens the rename prompt for the selected download. An active
// download is refused because the fetch writes through the paths being moved.
func (m *downloadsModel) startRename() tea.Cmd {
	if m.cursor >= len(m.rows) {
		return nil
	}
	dl := m.rows[m.cursor]
	if dl.ID == m.app.eng.ActiveID() {
		m.notice = "stop the download before renaming it"
		return nil
	}
	m.app.rename = newRenameModel(m.app, dl)
	return m.app.rename.init()
}

// mouse routes a click or wheel notch to the pane under the pointer. Its
// coordinates are body-relative, and the pane geometry is the one view
// recorded on the last render.
func (m *downloadsModel) mouse(msg tea.MouseMsg) tea.Cmd {
	if len(m.rows) == 0 || msg.Y < 0 || msg.Y >= m.paneHeight {
		return nil
	}
	inFiles := m.filesW > 0 && msg.X >= m.listW

	if delta := wheelDelta(msg); delta != 0 {
		if inFiles {
			m.selectTreeRow(m.treeCursor + delta)
		} else {
			m.selectRow(m.cursor + delta)
		}
		return nil
	}
	if !leftPress(msg) {
		return nil
	}
	// a click anywhere dismisses a pending removal, like any other key
	m.notice, m.confirmRemove = "", false

	if inFiles {
		return m.clickFile(msg.Y)
	}
	m.clickDownload(msg.Y)
	return nil
}

// clickDownload selects the download on body row y; clicking the selected
// row again moves focus to its files, the way l does.
func (m *downloadsModel) clickDownload(y int) {
	i := m.scroll + y
	if i >= len(m.rows) {
		return
	}
	double := m.clicks.press(clickDownload, i)
	m.selectRow(i)
	if double && len(m.files) > 0 {
		m.pane = paneFiles
	}
}

// clickFile selects the row on body row y of the file pane — a file or a
// directory header, both being focusable — and plays a file on a double click.
func (m *downloadsModel) clickFile(y int) tea.Cmd {
	if y == 0 || m.cursor >= len(m.rows) {
		return nil // pane title
	}
	i := m.treeScroll + y - 1
	if i < 0 || i >= len(m.tree) {
		return nil
	}
	double := m.clicks.press(clickFile, i)
	m.selectTreeRow(i)
	if double && m.tree[i].dir == "" {
		return m.openSelectedFile()
	}
	return nil
}

// selectRow focuses the downloads pane and moves its cursor to i, clamped.
func (m *downloadsModel) selectRow(i int) {
	m.pane = paneList
	i = min(max(i, 0), len(m.rows)-1)
	if i == m.cursor {
		return
	}
	m.cursor = i
	m.loadFiles()
}

// selectTreeRow focuses the file pane and moves its cursor to tree row i,
// clamped.
func (m *downloadsModel) selectTreeRow(i int) {
	if len(m.tree) == 0 {
		return
	}
	m.pane = paneFiles
	m.treeCursor = min(max(i, 0), len(m.tree)-1)
}

// refreshListing re-fetches the remote listing for the selected folder
// download and merges files added remotely since it was enqueued.
func (m *downloadsModel) refreshListing() tea.Cmd {
	if m.cursor >= len(m.rows) {
		return nil
	}
	dl := *m.rows[m.cursor]
	if dl.LinkType != "folder" {
		m.notice = "file links have no folder listing"
		return nil
	}
	if m.refreshing {
		m.notice = "refresh already in progress"
		return nil
	}
	m.refreshing = true
	m.notice = "refreshing remote listing…"

	drv, database := m.app.drv, m.app.db
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		nodes, err := drv.List(ctx, dl.URL)
		if err != nil {
			return listingMergedMsg{err: err}
		}
		added, err := database.MergeFiles(dl.ID, listingFiles(dl.DestPath, nodes))
		return listingMergedMsg{added: added, err: err}
	}
}

// openSelectedFile plays what the file pane's cursor is on: the highlighted
// file — the final path once it is complete, otherwise the `.megatmp.<handle>`
// partial, which already holds decrypted plaintext from the start of the file
// — or, on a directory header, the first playable file inside it. Later media
// files sitting in the same directory are queued behind it so a folder of
// episodes keeps playing; see playlistTail.
func (m *downloadsModel) openSelectedFile() tea.Cmd {
	if i := m.cursorFile(); i >= 0 {
		return m.playFrom(i)
	}
	return m.playFirst(subtreeFiles(m.tree, m.treeCursor))
}

// openSelectedDownload plays the whole selected download: playback starts at
// its first playable file and the rest of that file's folder is queued behind
// it, so o on a list row plays a folder without picking a file first.
func (m *downloadsModel) openSelectedDownload() tea.Cmd {
	all := make([]int, len(m.files))
	for i := range m.files {
		all[i] = i
	}
	return m.playFirst(all)
}

// playFirst starts playback at the first of the given files that is playable.
func (m *downloadsModel) playFirst(idx []int) tea.Cmd {
	for _, i := range idx {
		f := m.files[i]
		if !isMediaFile(f.LocalPath) {
			continue
		}
		// a partial is playable too: it holds plaintext from byte zero
		if f.Status == db.FileDone || f.Status == db.FileSkipped || m.partials[f.ID] > 0 {
			return m.playFrom(i)
		}
	}
	m.notice = "nothing to play is on disk yet"
	return nil
}

// playFrom plays files[i], with the media files after it queued behind; see
// playlistTail.
func (m *downloadsModel) playFrom(i int) tea.Cmd {
	if i < 0 || i >= len(m.files) {
		return nil
	}
	f := m.files[i]
	open := m.openFile
	if open == nil {
		open = openInMPV
	}
	path := f.LocalPath
	name := filepath.Base(f.LocalPath)
	if f.Status != db.FileDone && f.Status != db.FileSkipped {
		path = filepath.Join(filepath.Dir(f.LocalPath), ".megatmp."+f.NodeHandle)
		name += " (partial)"
	}
	queued := playlistTail(m.files, i)
	return func() tea.Msg {
		if _, err := os.Stat(path); err != nil {
			return fileOpenedMsg{err: errors.New(name + " is not on disk yet")}
		}
		return fileOpenedMsg{
			name:   name,
			queued: len(queued),
			err:    open(append([]string{path}, queued...)),
		}
	}
}

// playlistTail returns the paths to autoplay after files[i]. Entries are the
// media files that follow it in listing order and live in the same directory
// — subdirectories like "featurettes" are deliberately left out — and that are
// fully on disk, since a partial would cut playback off mid-file. A file that
// is not yet downloaded is skipped rather than ending the playlist.
func playlistTail(files []db.File, i int) []string {
	if i < 0 || i >= len(files) || !isMediaFile(files[i].LocalPath) {
		return nil
	}
	dir := filepath.Dir(files[i].LocalPath)
	var paths []string
	for _, f := range files[i+1:] {
		if f.Status != db.FileDone && f.Status != db.FileSkipped {
			continue
		}
		if filepath.Dir(f.LocalPath) != dir || !isMediaFile(f.LocalPath) {
			continue
		}
		if _, err := os.Stat(f.LocalPath); err != nil {
			continue
		}
		paths = append(paths, f.LocalPath)
	}
	return paths
}

// mediaExts are the extensions worth autoplaying; it keeps artwork, subtitles
// and .nfo sidecars out of the queue.
var mediaExts = map[string]bool{
	".avi": true, ".flv": true, ".m2ts": true, ".m4v": true, ".mkv": true,
	".mov": true, ".mp4": true, ".mpeg": true, ".mpg": true, ".ogv": true,
	".ts": true, ".webm": true, ".wmv": true,
	".aac": true, ".flac": true, ".m4a": true, ".mp3": true, ".ogg": true,
	".opus": true, ".wav": true, ".wma": true,
}

func isMediaFile(path string) bool {
	return mediaExts[strings.ToLower(filepath.Ext(path))]
}

// openInMPV plays paths in a detached mpv, the first as the current file and
// the rest as queued playlist entries. It executes the binary directly —
// LaunchServices (`open -a`) adds seconds of startup latency — and puts it in
// its own session with no stdio, so the TUI keeps the terminal and playback
// survives megadl exiting or receiving ctrl+c.
func openInMPV(paths []string) error {
	bin := findMPV()
	if bin == "" {
		return errors.New("mpv executable not found")
	}
	cmd := exec.Command(bin, paths...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap so an exited player doesn't linger as a zombie
	return nil
}

func findMPV() string {
	if bin, err := exec.LookPath("mpv"); err == nil {
		return bin
	}
	// GUI-spawned shells can miss Homebrew in PATH; try known locations.
	for _, p := range []string{
		"/opt/homebrew/bin/mpv",
		"/usr/local/bin/mpv",
		"/Applications/mpv.app/Contents/MacOS/mpv",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (m *downloadsModel) copyURL(url string) {
	if runtime.GOOS != "darwin" {
		m.notice = "clipboard copy only implemented on macOS"
		return
	}
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(url)
	if err := cmd.Run(); err != nil {
		m.notice = "pbcopy failed: " + err.Error()
		return
	}
	m.notice = "url copied"
}

func (m *downloadsModel) help() string {
	if m.pane == paneFiles {
		return renderShortcuts(
			shortcut{keys: []string{"j/k"}, label: "move"},
			shortcut{keys: []string{"J/K"}, label: "sibling"},
			shortcut{keys: []string{"⏎"}, label: m.toggleLabel() + " " + m.toggleNoun()},
			shortcut{keys: []string{"o"}, label: "open"},
			shortcut{keys: []string{"p/space"}, label: m.pauseLabel()},
			shortcut{keys: []string{"f"}, label: "focus current"},
			shortcut{keys: []string{"r"}, label: "rename"},
			shortcut{keys: []string{"R"}, label: "refresh listing"},
			shortcut{keys: []string{"h"}, label: "back"},
			shortcut{keys: []string{"q"}, label: "quit"},
			shortcut{keys: []string{"z"}, label: "center"},
		)
	}
	return renderShortcuts(
		shortcut{keys: []string{"a"}, label: "add"},
		shortcut{keys: []string{"⏎"}, label: m.toggleLabel()},
		shortcut{keys: []string{"o"}, label: "open"},
		shortcut{keys: []string{"l"}, label: "files"},
		shortcut{keys: []string{"p/space"}, label: m.pauseLabel()},
		shortcut{keys: []string{"f"}, label: "focus current"},
		shortcut{keys: []string{"r"}, label: "rename"},
		shortcut{keys: []string{"R"}, label: "refresh"},
		shortcut{keys: []string{"d"}, label: "remove"},
		shortcut{keys: []string{"y"}, label: "copy url"},
		shortcut{keys: []string{"q"}, label: "quit"},
	)
}

// pauseLabel names the half of the pause toggle that would happen next. It reads
// the queue, not the cursor, since pausing holds the whole queue.
func (m *downloadsModel) pauseLabel() string {
	if m.app != nil && m.app.eng != nil && m.app.eng.Paused() {
		return "resume"
	}
	return "pause"
}

// toggleLabel names the half of the enter toggle that applies to what the cursor
// is on, so the footer says what pressing it will do. A folder counts as
// queued once everything left to fetch in it is waiting, which is when
// pressing enter would take it back out.
func (m *downloadsModel) toggleLabel() string {
	if m.cursor >= len(m.rows) {
		return "queue"
	}
	queued := m.queued(m.rows[m.cursor])
	if m.pane == paneFiles && m.treeCursor < len(m.tree) {
		if i := m.cursorFile(); i >= 0 {
			queued = m.files[i].Queued
		} else {
			queued = allQueued(m.eligibleFiles(m.treeCursor))
		}
	}
	if queued {
		return "unqueue"
	}
	return "queue"
}

// toggleNoun names what the enter toggle in the file pane would act on.
func (m *downloadsModel) toggleNoun() string {
	if m.treeCursor < len(m.tree) && m.tree[m.treeCursor].dir != "" {
		return "folder"
	}
	return "file"
}

func (m *downloadsModel) view(width, height int) string {
	m.listW, m.filesW, m.paneHeight = width, 0, 0
	if len(m.rows) == 0 {
		return styleDim.Render("\n  no downloads yet — press 'a' to add a mega.nz link")
	}

	// The panes own the whole body: notices, the transfer strip and the
	// shortcuts all live under the footer's divider.
	listW, filesW := downloadPaneWidths(width, len(m.files) > 0)
	if filesW == 0 {
		m.pane = paneList // pane hidden (narrow terminal) — focus can't live there
	}
	m.listW, m.filesW, m.paneHeight = listW, filesW, height

	list := m.listView(listW, height)
	if filesW == 0 {
		return list
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(listW).Height(height).MaxHeight(height).Render(list),
		m.filesView(filesW, height))
}

func downloadPaneWidths(width int, hasFiles bool) (listW, filesW int) {
	listW = width
	if hasFiles && width > 60 {
		listW = min(44, max(28, width*35/100))
		filesW = width - listW - 2 // "│ " gutter
	}
	return listW, filesW
}

// listView renders the downloads column, keeping the cursor visible.
func (m *downloadsModel) listView(width, height int) string {
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+height {
		m.scroll = m.cursor - height + 1
	}

	snap := m.app.eng.Snapshot()
	var lines []string
	for i := m.scroll; i < min(len(m.rows), m.scroll+height); i++ {
		lines = append(lines, m.rowView(m.rows[i], snap, i == m.cursor, width))
	}
	return strings.Join(lines, "\n")
}

func (m *downloadsModel) rowView(dl *db.Download, snap engine.Snapshot, selected bool, width int) string {
	spin := m.app.spinFrame()
	nameW := max(8, width-2-1-1-2)
	name := truncate(dl.Name, nameW)

	line := fmt.Sprintf("%s%s %s", cursorBar(selected, m.pane == paneList),
		dlMarker(m.dlMarkerStateOf(dl, snap), spin), name)
	if selected {
		return tintRow(line, width)
	}
	return line
}

// fileTreeRow is one line of the file pane: a directory header derived
// from the files' local paths, or a file (dir == "").
type fileTreeRow struct {
	dir   string
	path  string // the directory's path under the download, for dir rows
	file  int    // index into the files slice, valid when dir == ""
	depth int
}

// fileTreeRows interleaves directory headers with the files beneath them.
// Files arrive sorted by remote path, so each directory is a contiguous
// block and a header is emitted where the directory prefix changes.
func fileTreeRows(files []db.File, destPath string) []fileTreeRow {
	var rows []fileTreeRow
	var open []string // directory components currently open
	for i, f := range files {
		rel := strings.TrimPrefix(f.LocalPath, destPath)
		rel = strings.TrimPrefix(rel, string(filepath.Separator))
		dirs := strings.Split(rel, string(filepath.Separator))
		dirs = dirs[:len(dirs)-1] // last component is the file itself
		common := 0
		for common < len(open) && common < len(dirs) && open[common] == dirs[common] {
			common++
		}
		open = open[:common]
		for _, d := range dirs[common:] {
			open = append(open, d)
			rows = append(rows, fileTreeRow{
				dir:   d,
				path:  strings.Join(open, string(filepath.Separator)),
				depth: len(open) - 1,
			})
		}
		rows = append(rows, fileTreeRow{file: i, depth: len(open)})
	}
	return rows
}

// subtreeFiles returns the indices into files of everything tree row i covers:
// the file itself, or every file nested under a directory header, subfolders
// included. Directory blocks are contiguous, so the subtree runs until the
// tree comes back out to i's own depth.
func subtreeFiles(rows []fileTreeRow, i int) []int {
	if i < 0 || i >= len(rows) {
		return nil
	}
	if rows[i].dir == "" {
		return []int{rows[i].file}
	}
	var files []int
	for j := i + 1; j < len(rows) && rows[j].depth > rows[i].depth; j++ {
		if rows[j].dir == "" {
			files = append(files, rows[j].file)
		}
	}
	return files
}

// siblingRow is where a J/K move lands from row i: the next (step +1) or
// previous (step -1) row at the level J/K works on, skipping anything nested
// deeper and stopping rather than climbing out past that level. From a folder
// that level is the folder's own, so J on "Season 01" reaches "Season 02"
// rather than the episodes between them. From a file it is the level of the
// folder holding it, so J out of an episode leaves the season entirely and
// lands on "Season 02", and K lands on the season header above it. A file at
// the top level has no folder to step out of and moves among its own level.
func siblingRow(rows []fileTreeRow, i, step int) int {
	if i < 0 || i >= len(rows) {
		return i
	}
	level := rows[i].depth
	if rows[i].dir == "" {
		level = max(level-1, 0)
	}
	for j := i + step; j >= 0 && j < len(rows); j += step {
		if rows[j].depth < level {
			break // left the folder; there is no sibling this way
		}
		if rows[j].depth == level {
			return j
		}
	}
	return i
}

// filesView renders the contents of the selected download as a tree, each
// file marked as downloaded or not.
func (m *downloadsModel) filesView(width, height int) string {
	dl := m.rows[m.cursor]
	var haveBytes, totalBytes int64
	completed := 0
	snap := m.app.eng.Snapshot()
	for _, f := range m.files {
		totalBytes += f.Size
		switch {
		case f.Status == db.FileDone || f.Status == db.FileSkipped:
			completed++
			haveBytes += f.Size
		case isFetching(f, dl, snap):
			haveBytes += max(snap.FileDone, m.partials[f.ID])
		default:
			haveBytes += m.partials[f.ID]
		}
	}
	title := filesTitle(dl, completed, len(m.files), haveBytes, totalBytes, max(0, width-2))
	pausedFile := m.pausedFile(dl, snap)

	rowH := height - 1 // title line
	if rowH < 1 {
		rowH = 1
	}

	rows := m.tree
	cursorRow := min(max(m.treeCursor, 0), max(0, len(rows)-1))
	// scroll in tree-row units; pull ancestor headers directly above the
	// cursor into view so entering a folder shows its name
	top := cursorRow
	for top > 0 && rows[top-1].dir != "" {
		top--
	}
	if top < m.treeScroll {
		m.treeScroll = top
	}
	if cursorRow >= m.treeScroll+rowH {
		m.treeScroll = cursorRow - rowH + 1
	}

	sizeW := 0
	for _, f := range m.files {
		sizeW = max(sizeW, lipgloss.Width(fileBytes(f.Size)))
	}
	lines := []string{title}
	for i := m.treeScroll; i < min(len(rows), m.treeScroll+rowH); i++ {
		r := rows[i]
		if r.dir != "" {
			lines = append(lines, m.dirRowView(r, i == cursorRow, width))
			continue
		}
		lines = append(lines, m.fileRowView(m.files[r.file], dl, snap, pausedFile,
			i == cursorRow, r.depth, width, sizeW))
	}

	gutter := styleDim.Render("│ ")
	for i := range lines {
		lines[i] = gutter + lines[i]
	}
	return strings.Join(lines, "\n")
}

// centerTreeCursor scrolls the file pane so its cursor sits halfway down the
// rows below the title. It deliberately does not clamp the scroll to a full
// final page: near the end of the tree, blank rows below are what let the
// cursor remain centered.
func (m *downloadsModel) centerTreeCursor() {
	rowH := max(1, m.paneHeight-1) // title line
	cursorRow := min(max(m.treeCursor, 0), max(0, len(m.tree)-1))
	m.treeScroll = max(0, cursorRow-rowH/2)
}

// filesTitle keeps the selected folder name and file count on the left, and
// aligns on the right how much of the whole folder is present locally —
// measured against every file in it, queued or not.
func filesTitle(dl *db.Download, completed, total int, haveBytes, totalBytes int64, width int) string {
	if width <= 0 {
		return ""
	}

	count := fmt.Sprintf("  %d/%d files", completed, total)
	if totalBytes <= 0 {
		if lipgloss.Width(count) > width-4 {
			return styleTitle.Render(truncate(dl.Name, width))
		}
		nameW := max(1, width-lipgloss.Width(count))
		return styleTitle.Render(truncate(dl.Name, nameW)) + styleDim.Render(count)
	}

	frac := min(1, max(0, float64(haveBytes)/float64(totalBytes)))

	if width < 14 {
		return styleTitle.Render(truncate(dl.Name, width))
	}
	barW := min(16, max(6, width/3))
	progress := fileHeaderProgressBar(barW, frac) + " " + styleTitle.Render(percentText(frac))
	progressW := lipgloss.Width(progress)
	leftW := max(1, width-progressW-2)

	left := styleTitle.Render(truncate(dl.Name, leftW))
	if lipgloss.Width(dl.Name)+lipgloss.Width(count) <= leftW {
		left = styleTitle.Render(dl.Name) + styleDim.Render(count)
	}
	gap := max(1, width-lipgloss.Width(left)-progressW)
	return left + strings.Repeat(" ", gap) + progress
}

// isFetching reports whether f is the file the engine is downloading now.
func isFetching(f db.File, dl *db.Download, snap engine.Snapshot) bool {
	if dl.ID != snap.ActiveID || snap.CurrentFile == "" {
		return false
	}
	if snap.CurrentPath != "" {
		return f.LocalPath == snap.CurrentPath
	}
	// Snapshots constructed by callers predating CurrentPath can still
	// identify the active row when filenames are unique.
	return filepath.Base(f.LocalPath) == snap.CurrentFile
}

// pausedFile is the file a held queue is stopped at, or 0 when the queue is
// running or holding somewhere else. The engine queues files rather than pausing
// one, so the answer comes from the queue: only its head can be holding a file,
// and the file it holds is the one it would fetch next.
func (m *downloadsModel) pausedFile(dl *db.Download, snap engine.Snapshot) int64 {
	if !snap.Paused || m.head.file == nil || m.head.dl.ID != dl.ID {
		return 0
	}
	return m.head.file.ID
}

// dirRowView renders a directory header. It is focusable like a file row, so
// it carries the same cursor bar and tint when the cursor is on it.
func (m *downloadsModel) dirRowView(r fileTreeRow, selected bool, width int) string {
	name := truncate(r.dir+"/", max(1, width-4-2*r.depth))
	line := cursorBar(selected, m.pane == paneFiles) +
		strings.Repeat("  ", r.depth) + styleDim.Render(name)
	if selected {
		// width less the pane gutter filesView prepends
		return tintRow(line, width-2)
	}
	return line
}

func (m *downloadsModel) fileRowView(f db.File, dl *db.Download, snap engine.Snapshot, pausedFile int64, selected bool, depth, width, sizeW int) string {
	fetching := isFetching(f, dl, snap)
	paused := pausedFile != 0 && f.ID == pausedFile
	active := fetching || paused

	indent := strings.Repeat("  ", depth)
	// the pane gutter (added by filesView) and the cursor column take 2 cells each
	contentW := max(0, width-4-len(indent))

	// marker(2) name progress(gap + bar + gap + percent) gap(2) size.
	// In narrow panes the bar gives way to the name; the bar and percentage
	// disappear together if fewer than two cells remain for the bar.
	const (
		minNameW = 8
		percentW = 4
	)
	nameAndProgressW := max(0, contentW-2-2-sizeW)
	barW := 10
	progressW := 2 + barW + 1 + percentW
	nameW := nameAndProgressW - progressW
	if nameW < minNameW {
		barW -= minNameW - nameW
	}
	if barW < 2 {
		barW = 0
		progressW = 0
	} else {
		progressW = 2 + barW + 1 + percentW
	}
	nameW = max(0, nameAndProgressW-progressW)

	name := fileName(filepath.Base(f.LocalPath), nameW)
	frac := fileProgress(f, snap, fetching, m.partials[f.ID])
	percent := percentText(frac)
	size := fmt.Sprintf("%*s", sizeW, fileBytes(f.Size))
	padW := contentW - 2 - nameW - progressW - 2 - sizeW
	padding := strings.Repeat(" ", max(0, padW))

	bar := ""
	if barW > 0 {
		percentStyle := styleDim
		if active {
			percentStyle = styleActivePercent
		}
		bar = "  " + fileProgressBar(barW, frac, active, paused) + " " + percentStyle.Render(percent)
	}
	st := fileMarkerStateOf(f, fetching, paused, frac)
	line := cursorBar(selected, m.pane == paneFiles) + indent +
		fileMarker(st, m.app.spinFrame()) + " " + name + bar + "  " +
		styleDim.Render(size) + padding
	if selected {
		// width less the pane gutter filesView prepends
		return tintRow(line, width-2)
	}
	return line
}

func fileProgress(f db.File, snap engine.Snapshot, fetching bool, partial int64) float64 {
	if f.Status == db.FileDone || f.Status == db.FileSkipped {
		return 1
	}
	if f.Size <= 0 {
		return 0
	}
	// The on-disk partial keeps stopped files at their last position; while
	// fetching, the live count takes over as soon as it catches up, so a
	// resumed file never dips back to zero.
	have := partial
	if fetching {
		have = max(have, snap.FileDone)
	}
	return min(1, max(0, float64(have)/float64(f.Size)))
}

// fileName truncates and pads a filename to a fixed-width column.
func fileName(name string, width int) string {
	if width <= 0 {
		return ""
	}
	name = ansi.Truncate(name, width, "…")
	return name + strings.Repeat(" ", max(0, width-lipgloss.Width(name)))
}

// fileMarkerState is what a file's marker depends on. paused marks the one
// file a held queue is stopped at; landed means every byte is on disk, whether
// this run fetched it or it was already there.
type fileMarkerState struct {
	fetching bool
	paused   bool
	queued   bool
	landed   bool
	failed   bool
	frac     float64
}

func fileMarkerStateOf(f db.File, fetching, paused bool, frac float64) fileMarkerState {
	return fileMarkerState{
		fetching: fetching,
		paused:   paused,
		queued:   f.Queued,
		landed:   f.Status == db.FileDone || f.Status == db.FileSkipped,
		failed:   f.Status == db.FileError,
		frac:     frac,
	}
}

// fileMarkerText is the file's one-cell marker, drawn from the same set as the
// list column so the two panes read alike. The file being fetched right now
// animates with the shared spinner frame.
func fileMarkerText(st fileMarkerState, spin string) string {
	switch {
	case st.fetching:
		return spin
	case st.landed:
		return "✓"
	case st.failed:
		return "✗"
	case st.paused:
		return pausedGlyph
	case st.queued:
		return queuedGlyph
	case st.frac > 0:
		return partialGlyph
	}
	return emptyGlyph
}

func fileMarker(st fileMarkerState, spin string) string {
	text := fileMarkerText(st, spin)
	switch {
	case st.fetching:
		return styleSpinner.Render(text)
	case st.landed:
		return styleOK.Render(text)
	case st.failed:
		return styleError.Render(text)
	case st.paused:
		return styleWarn.Render(text)
	case st.queued:
		return styleDim.Render(text)
	case st.frac > 0:
		return stylePartial.Render(text)
	}
	return styleDim.Render(text)
}

// detailView is the strip below the footer's divider for selected-row errors,
// confirmations and notices.
func (m *downloadsModel) detailView(width int) string {
	var lines []string
	snap := m.app.eng.Snapshot()

	if snap.QuotaStalled && !m.quotaDismissed {
		lines = append(lines, " "+styleError.Render("QUOTA — mega is throttling, retrying"))
	}

	if snap.Paused {
		label := "PAUSED"
		if snap.PauseReason != "" {
			// a pause the engine imposed, so say what stopped the queue
			label += " — " + snap.PauseReason
		}
		lines = append(lines, " "+styleWarn.Render(label))
	}

	if m.cursor < len(m.rows) {
		dl := m.rows[m.cursor]
		if dl.Status == db.StatusError && dl.Error != "" {
			lines = append(lines, " "+styleError.Render(truncate(dl.Error, width-2)))
		}
	}

	if m.confirmRemove && m.cursor < len(m.rows) {
		lines = append(lines, " "+styleWarn.Render(fmt.Sprintf(
			"remove %q from the list? files on disk are kept (y/n)", m.rows[m.cursor].Name)))
	}
	if m.notice != "" {
		lines = append(lines, " "+styleNotice.Render(m.notice))
	}
	return strings.Join(lines, "\n")
}

// Markers for both panes. Every glyph must stay one cell wide so the name
// columns line up. They describe what is on disk and what the queue is going
// to do about it — not a status field, so there is nothing to leave stale.
const (
	pausedGlyph  = "⏸︎" // the queue is held here
	queuedGlyph  = "↓"  // waiting its turn
	partialGlyph = "◔"  // some of it is on disk
	emptyGlyph   = "•"  // none of it is
)

// dlMarkerState is what a download's list marker depends on. head is the front
// of the queue, which is where a pause holds; complete means the whole folder
// is on disk, including files the user never queued, since a check mark that
// only covered the queued ones would read as "all of it is here".
type dlMarkerState struct {
	active   bool
	paused   bool
	head     bool
	queued   bool
	complete bool
	anyBytes bool
	failed   bool
}

func (m *downloadsModel) dlMarkerStateOf(dl *db.Download, snap engine.Snapshot) dlMarkerState {
	counts := m.fileCounts[dl.ID]
	return dlMarkerState{
		active:   dl.ID == snap.ActiveID,
		paused:   snap.Paused,
		head:     m.atHead(dl),
		queued:   m.queued(dl),
		complete: counts.Complete(),
		anyBytes: counts.Landed > 0 || dl.DoneBytes > 0,
		failed:   dl.Status == db.StatusError,
	}
}

// dlMarkerText is the download's one-cell marker in the list column. A queued
// download says so rather than showing how far along it is: the progress it has
// made matters less than whether it is waiting, and the file pane has the
// detail either way.
func dlMarkerText(st dlMarkerState, spin string) string {
	switch {
	case st.active:
		return spin
	case st.queued && st.head && st.paused:
		return pausedGlyph
	case st.queued:
		return queuedGlyph
	case st.complete:
		return "✓"
	case st.anyBytes:
		return partialGlyph
	case st.failed:
		return "✗"
	}
	return emptyGlyph
}

func dlMarker(st dlMarkerState, spin string) string {
	text := dlMarkerText(st, spin)
	switch {
	case st.active:
		return styleSpinner.Render(text)
	case st.queued && st.head && st.paused:
		return styleWarn.Render(text)
	case st.queued:
		return styleDim.Render(text)
	case st.complete:
		return styleOK.Render(text)
	case st.anyBytes:
		return stylePartial.Render(text)
	case st.failed:
		return styleError.Render(text)
	}
	return styleDim.Render(text)
}
