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

	pane       paneID
	files      []db.File
	filesFor   int64 // download ID the file pane is showing
	fileCursor int
	fileScroll int
	// partials caches each unfinished file's on-disk .megatmp size (keyed
	// by file ID) so stopped files keep their progress bar. loadFiles
	// refreshes it so View never touches the filesystem.
	partials map[int64]int64
	// partialDownloads holds the download IDs whose folder is only partly on
	// disk, so a finished row can say "the files you picked are done" rather
	// than "the whole folder is here". Refreshed by reload.
	partialDownloads map[int64]bool
	// savedDownload is the row last written to the database, so repeated
	// events on the same row don't rewrite it.
	savedDownload int64

	confirmRemove bool // pending x confirmation for rows[cursor]
	refreshing    bool // remote listing fetch in flight
	notice        string
	// quotaDismissed is the download whose quota banner esc hid. The engine
	// keeps reporting the stall for the rest of that download's run, so
	// hiding has to be remembered; a stall under any other download is news
	// again and speaks up.
	quotaDismissed int64

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

// restore opens the view on the download the last session left selected; its
// file cursor follows from the download's own recorded selection. An unknown
// or removed download falls back to the top of the list.
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
			return
		}
	}
}

func (m *downloadsModel) reload() {
	rows, err := m.app.db.Downloads()
	if err == nil {
		m.rows = rows
	}
	if partial, err := m.app.db.PartialDownloads(); err == nil {
		m.partialDownloads = partial
	}
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
	m.loadFiles()
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
		m.files, m.filesFor, m.partials = nil, 0, nil
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
		m.fileCursor, m.fileScroll = 0, 0
	}
	for i := range files {
		if files[i].ID == dl.SelectedFileID {
			m.fileCursor = i
			break
		}
	}
	m.filesFor = dl.ID
	m.files = files
	m.partials = partialSizes(files)
	if m.fileCursor >= len(m.files) {
		m.fileCursor = max(0, len(m.files)-1)
	}
	if len(m.files) == 0 {
		m.pane = paneList
	}
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
}

// rememberFileCursor records the file pane's cursor against the download the
// pane is showing, which is not always the one under the list cursor: loadFiles
// calls this to capture the outgoing selection as the panes switch downloads.
func (m *downloadsModel) rememberFileCursor() {
	if m.filesFor == 0 || m.fileCursor < 0 || m.fileCursor >= len(m.files) {
		return
	}
	fileID := m.files[m.fileCursor].ID
	for _, dl := range m.rows {
		if dl.ID != m.filesFor {
			continue
		}
		if dl.SelectedFileID == fileID {
			return // already recorded
		}
		dl.SelectedFileID = fileID
		break
	}
	m.app.db.SetSelectedFile(m.filesFor, fileID)
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

	if m.pane == paneFiles {
		switch key.String() {
		case "up", "k":
			if m.fileCursor > 0 {
				m.fileCursor--
			}
		case "down", "j":
			if m.fileCursor < len(m.files)-1 {
				m.fileCursor++
			}
		case "enter":
			return m.openSelectedFile()
		case "s":
			m.toggleFile()
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
	case "enter", "right", "l":
		if len(m.files) > 0 {
			m.pane = paneFiles
		}
	case "s":
		m.toggleDownload()
	case "r":
		return m.startRename()
	case "x":
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

// dismissDetail hides what the detail strip is saying right now. A selected
// row's error is left alone: it describes the row rather than the moment, and
// it goes away with the selection.
func (m *downloadsModel) dismissDetail() {
	m.notice = ""
	if m.app == nil || m.app.eng == nil {
		return
	}
	if snap := m.app.eng.Snapshot(); snap.QuotaStalled {
		m.quotaDismissed = snap.ActiveID
	}
}

// running reports whether dl is working through its queue right now, and so
// whether stopping is the meaningful half of the toggle for it and its files.
// A download stalled on quota is still active, so it counts as running.
func (m *downloadsModel) running(dl *db.Download) bool {
	return dl.ID == m.app.eng.ActiveID() ||
		dl.Status == db.StatusQueued || dl.Status == db.StatusRunning
}

// toggleDownload stops the selected download if it is running, and starts it
// otherwise.
func (m *downloadsModel) toggleDownload() {
	if m.cursor >= len(m.rows) {
		return
	}
	dl := m.rows[m.cursor]
	switch {
	case m.running(dl):
		m.app.eng.Stop(dl.ID)
	case dl.Status == db.StatusDone:
		m.notice = "download already complete"
	default:
		m.app.eng.Resume(dl.ID)
	}
}

// toggleFile stops the selected file if it is on track to be fetched, and
// starts it otherwise. A file only counts as started when its download is
// running too, so pressing s inside a stopped download starts that download
// as well rather than dropping the file from its wanted set.
func (m *downloadsModel) toggleFile() {
	if m.fileCursor >= len(m.files) || m.cursor >= len(m.rows) {
		return
	}
	f := m.files[m.fileCursor]
	switch {
	case f.Status == db.FileDone || f.Status == db.FileSkipped:
		m.notice = "file already downloaded"
	case f.Wanted && m.running(m.rows[m.cursor]):
		m.app.eng.StopFile(f.ID)
		m.reload()
	default:
		m.app.eng.ResumeFile(f.ID)
		m.reload()
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
			m.selectFile(m.fileCursor + delta)
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
// row again moves focus to its files, the way enter does.
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

// clickFile selects the file on body row y of the file pane, and plays it on
// a double click. Clicking a directory header selects the first file under it.
func (m *downloadsModel) clickFile(y int) tea.Cmd {
	if y == 0 || m.cursor >= len(m.rows) {
		return nil // pane title
	}
	rows := fileTreeRows(m.files, m.rows[m.cursor].DestPath)
	i := m.fileScroll + y - 1
	if i < 0 || i >= len(rows) {
		return nil
	}
	target := rows[i].file
	if rows[i].dir != "" {
		target = -1
		for j := i + 1; j < len(rows); j++ {
			if rows[j].dir == "" {
				target = rows[j].file
				break
			}
		}
		if target < 0 {
			return nil
		}
	}
	double := m.clicks.press(clickFile, target)
	m.selectFile(target)
	if double && rows[i].dir == "" {
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

// selectFile focuses the file pane and moves its cursor to i, clamped.
func (m *downloadsModel) selectFile(i int) {
	if len(m.files) == 0 {
		return
	}
	m.pane = paneFiles
	m.fileCursor = min(max(i, 0), len(m.files)-1)
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

// openSelectedFile plays the highlighted file: the final path once it is
// complete, otherwise the `.megatmp.<handle>` partial, which already holds
// decrypted plaintext from the start of the file. Later media files sitting
// in the same directory are queued behind it so a folder of episodes keeps
// playing; see playlistTail.
func (m *downloadsModel) openSelectedFile() tea.Cmd {
	if m.fileCursor >= len(m.files) {
		return nil
	}
	f := m.files[m.fileCursor]
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
	queued := playlistTail(m.files, m.fileCursor)
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
			shortcut{keys: []string{"⏎"}, label: "play"},
			shortcut{keys: []string{"s"}, label: m.toggleLabel() + " file"},
			shortcut{keys: []string{"r"}, label: "rename"},
			shortcut{keys: []string{"R"}, label: "refresh listing"},
			shortcut{keys: []string{"h"}, label: "back"},
			shortcut{keys: []string{"q"}, label: "quit"},
		)
	}
	return renderShortcuts(
		shortcut{keys: []string{"a"}, label: "add"},
		shortcut{keys: []string{"⏎"}, label: "files"},
		shortcut{keys: []string{"s"}, label: m.toggleLabel()},
		shortcut{keys: []string{"r"}, label: "rename"},
		shortcut{keys: []string{"R"}, label: "refresh"},
		shortcut{keys: []string{"x"}, label: "remove"},
		shortcut{keys: []string{"y"}, label: "copy url"},
		shortcut{keys: []string{"q"}, label: "quit"},
	)
}

// toggleLabel names the half of the s toggle that applies to what the cursor
// is on, so the footer says what pressing it will do.
func (m *downloadsModel) toggleLabel() string {
	if m.cursor >= len(m.rows) {
		return "stop"
	}
	running := m.running(m.rows[m.cursor])
	if m.pane == paneFiles && m.fileCursor < len(m.files) {
		running = running && m.files[m.fileCursor].Wanted
	}
	if running {
		return "stop"
	}
	return "start"
}

func (m *downloadsModel) view(width, height int) string {
	m.listW, m.filesW, m.paneHeight = width, 0, 0
	if len(m.rows) == 0 {
		return styleDim.Render("\n  no downloads yet — press 'a' to add a mega.nz link")
	}

	detail := m.detailView(width)
	paneHeight := height - lipgloss.Height(detail)
	if detail != "" {
		paneHeight = height - lipgloss.Height(detail) - 1 // blank separator
	}
	if paneHeight < 1 {
		paneHeight = 1
	}

	listW := width
	filesW := 0
	if len(m.files) > 0 && width > 60 {
		listW = min(44, max(28, width*35/100))
		filesW = width - listW - 2 // "│ " gutter
	}
	if filesW == 0 {
		m.pane = paneList // pane hidden (narrow terminal) — focus can't live there
	}
	m.listW, m.filesW, m.paneHeight = listW, filesW, paneHeight

	list := m.listView(listW, paneHeight)
	body := list
	if filesW > 0 {
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(listW).Height(paneHeight).MaxHeight(paneHeight).Render(list),
			m.filesView(filesW, paneHeight))
	}

	if detail != "" {
		body = lipgloss.NewStyle().Height(paneHeight).MaxHeight(paneHeight).Render(body) +
			"\n\n" + detail
	}
	return body
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
	active := dl.ID == snap.ActiveID
	extra := ""
	if active && snap.Rate > 0 {
		extra = "  " + humanRate(snap.Rate)
	}
	spin := m.app.spinFrame()
	partial := m.partialDownloads[dl.ID]
	nameW := max(8, width-2-1-1-lipgloss.Width(extra)-2)
	name := truncate(dl.Name, nameW)

	line := fmt.Sprintf("%s%s %s", cursorBar(selected, m.pane == paneList),
		statusIcon(dl.Status, active, partial, spin), name)
	if extra != "" {
		line += styleOK.Render(extra)
	}
	if selected {
		return tintRow(line, width)
	}
	return line
}

// fileTreeRow is one line of the file pane: a directory header derived
// from the files' local paths, or a file (dir == "").
type fileTreeRow struct {
	dir   string
	file  int // index into the files slice, valid when dir == ""
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
			rows = append(rows, fileTreeRow{dir: d, depth: len(open)})
			open = append(open, d)
		}
		rows = append(rows, fileTreeRow{file: i, depth: len(open)})
	}
	return rows
}

// filesView renders the contents of the selected download as a tree, each
// file marked as downloaded or not.
func (m *downloadsModel) filesView(width, height int) string {
	dl := m.rows[m.cursor]
	done := 0
	var haveBytes, totalBytes int64
	snap := m.app.eng.Snapshot()
	for _, f := range m.files {
		totalBytes += f.Size
		if f.Status == db.FileDone {
			done++
		}
		switch {
		case f.Status == db.FileDone || f.Status == db.FileSkipped:
			haveBytes += f.Size
		case isFetching(f, dl, snap):
			haveBytes += max(snap.FileDone, m.partials[f.ID])
		default:
			haveBytes += m.partials[f.ID]
		}
	}
	title := filesTitle(dl, done, len(m.files), haveBytes, totalBytes, max(0, width-2))

	rowH := height - 1 // title line
	if rowH < 1 {
		rowH = 1
	}

	rows := fileTreeRows(m.files, dl.DestPath)
	cursorRow := 0
	for i, r := range rows {
		if r.dir == "" && r.file == m.fileCursor {
			cursorRow = i
			break
		}
	}
	// scroll in tree-row units; pull ancestor headers directly above the
	// cursor into view so entering a folder shows its name
	top := cursorRow
	for top > 0 && rows[top-1].dir != "" {
		top--
	}
	if top < m.fileScroll {
		m.fileScroll = top
	}
	if cursorRow >= m.fileScroll+rowH {
		m.fileScroll = cursorRow - rowH + 1
	}

	sizeW := 0
	for _, f := range m.files {
		sizeW = max(sizeW, lipgloss.Width(fileBytes(f.Size)))
	}
	lines := []string{title}
	for i := m.fileScroll; i < min(len(rows), m.fileScroll+rowH); i++ {
		r := rows[i]
		if r.dir != "" {
			indent := cursorBar(false, false) + strings.Repeat("  ", r.depth)
			lines = append(lines, indent+styleDim.Render(truncate(r.dir+"/", max(1, width-4-2*r.depth))))
			continue
		}
		lines = append(lines, m.fileRowView(m.files[r.file], dl, snap, r.file == m.fileCursor, r.depth, width, sizeW))
	}

	gutter := styleDim.Render("│ ")
	for i := range lines {
		lines[i] = gutter + lines[i]
	}
	return strings.Join(lines, "\n")
}

// filesTitle keeps the selected folder name and file count on the left, and
// aligns on the right how much of the whole folder is present locally —
// measured against every file in it, wanted or not.
func filesTitle(dl *db.Download, done, total int, haveBytes, totalBytes int64, width int) string {
	if width <= 0 {
		return ""
	}

	count := fmt.Sprintf("  %d/%d files", done, total)
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
	progress := progressBar(barW, frac) + " " + styleTitle.Render(percentText(frac))
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

func (m *downloadsModel) fileRowView(f db.File, dl *db.Download, snap engine.Snapshot, selected bool, depth, width, sizeW int) string {
	fetching := isFetching(f, dl, snap)

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
		bar = "  " + fileProgressBar(barW, frac) + " " + styleDim.Render(percent)
	}
	line := cursorBar(selected, m.pane == paneFiles) + indent +
		fileMarker(f, fetching, frac, m.app.spinFrame()) + " " + name + bar + "  " +
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

// fileMarkerText is the file's one-cell marker. The file being fetched right
// now animates with the shared spinner frame, matching the list column's
// active row.
func fileMarkerText(f db.File, fetching bool, frac float64, spin string) string {
	switch {
	case fetching:
		return spin
	case f.Status == db.FileDone:
		return "✓"
	case f.Status == db.FileSkipped:
		return "✓"
	case frac > 0 && f.Status != db.FileError:
		return "◔"
	case !f.Wanted:
		return "○"
	case f.Status == db.FileError:
		return "✗"
	}
	return "·"
}

func fileMarker(f db.File, fetching bool, frac float64, spin string) string {
	text := fileMarkerText(f, fetching, frac, spin)
	switch {
	case fetching:
		return styleSpinner.Render(text)
	case f.Status == db.FileDone || f.Status == db.FileSkipped:
		return styleOK.Render(text)
	case frac > 0 && f.Status != db.FileError:
		return stylePartial.Render(text)
	case !f.Wanted:
		return styleDim.Render(text)
	case f.Status == db.FileError:
		return styleError.Render(text)
	}
	return styleDim.Render(text)
}

// detailView is the strip under the panes for selected-row errors,
// confirmations and notices.
func (m *downloadsModel) detailView(width int) string {
	var lines []string
	snap := m.app.eng.Snapshot()

	if snap.QuotaStalled && m.quotaDismissed != snap.ActiveID {
		lines = append(lines, " "+styleError.Render("QUOTA — mega is throttling, retrying"))
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

// statusIconText is the download's one-cell marker in the list column,
// mirroring the file pane's markers. The engine's current download animates
// with the shared spinner frame; a 'running' row without it is a leftover
// from a previous session. A finished download whose folder is only partly on
// disk gets the partial glyph, since a check would read as "the whole folder
// is here" rather than "the files you picked are here". Every glyph must stay
// one cell wide so the name column lines up.
func statusIconText(status string, active, partial bool, spin string) string {
	if active {
		return spin
	}
	switch status {
	case db.StatusQueued:
		return "○"
	case db.StatusRunning:
		return "◔"
	case db.StatusStopped:
		return "⏸︎"
	case db.StatusDone:
		if partial {
			return "◔"
		}
		return "✓"
	case db.StatusError:
		return "✗"
	case db.StatusQuota:
		return "⚠"
	}
	return "·"
}

func statusIcon(status string, active, partial bool, spin string) string {
	text := statusIconText(status, active, partial, spin)
	if active {
		return styleSpinner.Render(text)
	}
	switch status {
	case db.StatusQueued:
		return styleDim.Render(text)
	case db.StatusRunning:
		return styleAccent.Render(text)
	case db.StatusStopped:
		return styleWarn.Render(text)
	case db.StatusDone:
		if partial {
			return stylePartial.Render(text)
		}
		return styleOK.Render(text)
	case db.StatusError, db.StatusQuota:
		return styleError.Render(text)
	}
	return text
}
