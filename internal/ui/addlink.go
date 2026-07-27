package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"megadl/internal/db"
	"megadl/internal/mega"
	"megadl/internal/naming"
)

type addlinkState int

const (
	stateURL addlinkState = iota
	stateDecoding
	stateListing
	statePicker
	stateName
	stateExisting
	stateFailed
)

const (
	decodeFrames        = 28
	decodeFrameInterval = 40 * time.Millisecond
)

var reFileLink = regexp.MustCompile(`(?i)mega(\.co)?\.nz/(#!|file/)`)
var reFolderLink = regexp.MustCompile(`(?i)mega(\.co)?\.nz/(#F!|folder/)`)

type listResultMsg struct {
	url   string
	nodes []mega.Node
	err   error
}

// decodeFrameMsg advances the decode animation; seq discards ticks from a
// cancelled or restarted animation.
type decodeFrameMsg struct {
	seq int
}

type addlinkModel struct {
	app   *App
	state addlinkState

	urlInput  textinput.Model
	nameInput textinput.Model
	spin      spinner.Model

	linkHistory  []string
	historyIndex int
	historyDraft string

	url      string
	linkType string // "file" | "folder"
	nodes    []mega.Node
	picker   pickerModel
	existing *db.Download
	errMsg   string

	// modal is where the last render placed the dialog, in body
	// coordinates, so clicks can be mapped onto picker rows.
	modal  rect
	clicks clickTracker

	decodeSrc    string // base64 text as pasted
	decodeTarget string // decoded mega.nz link
	decodeFrame  int
	decodeSeq    int
}

func newAddlinkModel(app *App) *addlinkModel {
	url := textinput.New()
	url.Placeholder = "https://mega.nz/..."
	url.Focus()
	url.Width = 70

	name := textinput.New()
	name.Width = 60

	sp := spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(styleSpinner),
	)

	return &addlinkModel{
		app:          app,
		urlInput:     url,
		nameInput:    name,
		spin:         sp,
		linkHistory:  submittedLinkHistory(app.downloads.rows),
		historyIndex: -1,
	}
}

func (m *addlinkModel) init() tea.Cmd {
	return textinput.Blink
}

// submittedLinkHistory returns distinct library URLs, newest first.
func submittedLinkHistory(downloads []*db.Download) []string {
	seen := make(map[string]bool, len(downloads))
	history := make([]string, 0, len(downloads))
	for _, dl := range downloads {
		if dl.URL == "" || seen[dl.URL] {
			continue
		}
		seen[dl.URL] = true
		history = append(history, dl.URL)
	}
	return history
}

func (m *addlinkModel) previousURL() {
	if len(m.linkHistory) == 0 {
		return
	}
	if m.historyIndex == -1 {
		m.historyDraft = m.urlInput.Value()
		m.historyIndex = 0
	} else if m.historyIndex < len(m.linkHistory)-1 {
		m.historyIndex++
	}
	m.urlInput.SetValue(m.linkHistory[m.historyIndex])
	m.urlInput.CursorEnd()
	m.errMsg = ""
	m.refreshLinkHint()
}

func (m *addlinkModel) nextURL() {
	if m.historyIndex == -1 {
		return
	}
	if m.historyIndex > 0 {
		m.historyIndex--
		m.urlInput.SetValue(m.linkHistory[m.historyIndex])
	} else {
		m.historyIndex = -1
		m.urlInput.SetValue(m.historyDraft)
	}
	m.urlInput.CursorEnd()
	m.errMsg = ""
	m.refreshLinkHint()
}

// urlInputView pads the input line to its filled-in width: bubbles renders
// the placeholder Width cells wide but a value prompt+Width+1, so the dialog
// would otherwise widen the moment the input gets content.
func (m *addlinkModel) urlInputView() string {
	view := m.urlInput.View()
	w := lipgloss.Width(m.urlInput.PromptStyle.Render(m.urlInput.Prompt)) + m.urlInput.Width + 1
	if pad := w - lipgloss.Width(view); pad > 0 {
		view += strings.Repeat(" ", pad)
	}
	return view
}

// refreshLinkHint colors the input orange while it holds something enter can
// act on: a valid mega link, or base64 that decodes to one.
func (m *addlinkModel) refreshLinkHint() {
	url := strings.TrimSpace(m.urlInput.Value())
	actionable := reFileLink.MatchString(url) || reFolderLink.MatchString(url)
	if !actionable {
		_, actionable = decodeBase64MegaLink(url)
	}
	if actionable {
		m.urlInput.TextStyle = styleDecode
	} else {
		m.urlInput.TextStyle = lipgloss.NewStyle()
	}
}

func (m *addlinkModel) decodeTickCmd() tea.Cmd {
	seq := m.decodeSeq
	return tea.Tick(decodeFrameInterval, func(time.Time) tea.Msg {
		return decodeFrameMsg{seq: seq}
	})
}

// applyDecoded stops the animation and puts the decoded link in the prompt.
func (m *addlinkModel) applyDecoded() {
	m.decodeSeq++
	m.state = stateURL
	m.urlInput.SetValue(m.decodeTarget)
	m.urlInput.CursorEnd()
	m.refreshLinkHint()
}

// finishDecode ends the animation and hands the decoded link back to the URL
// prompt so it can be reviewed before listing.
func (m *addlinkModel) finishDecode() tea.Cmd {
	m.applyDecoded()
	return textinput.Blink
}

// submitURL acts on the link the user pressed enter on: a mega link starts
// listing, base64 that hides one starts the decode animation instead.
func (m *addlinkModel) submitURL(url string) (*addlinkModel, tea.Cmd) {
	switch {
	case reFileLink.MatchString(url):
		m.linkType = "file"
	case reFolderLink.MatchString(url):
		m.linkType = "folder"
	default:
		if decoded, ok := decodeBase64MegaLink(url); ok {
			m.decodeSrc = url
			m.decodeTarget = decoded
			m.errMsg = ""
			m.state = stateDecoding
			m.decodeSeq++
			m.decodeFrame = 0
			return m, m.decodeTickCmd()
		}
		m.errMsg = "that doesn't look like a mega.nz file or folder link"
		return m, nil
	}
	m.url = url
	m.errMsg = ""
	m.state = stateListing
	return m, tea.Batch(m.spin.Tick, m.listCmd(url))
}

func (m *addlinkModel) listCmd(url string) tea.Cmd {
	drv := m.app.drv
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		nodes, err := drv.List(ctx, url)
		return listResultMsg{url: url, nodes: nodes, err: err}
	}
}

// update returns nil to close the modal.
func (m *addlinkModel) update(msg tea.Msg) (*addlinkModel, tea.Cmd) {
	switch msg := msg.(type) {
	case listResultMsg:
		if msg.url != m.url || m.state != stateListing {
			return m, nil
		}
		if msg.err != nil {
			m.state = stateFailed
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.nodes = msg.nodes
		existing, err := m.app.db.FindByResource(m.linkType, msg.nodes[0].Handle)
		if err != nil {
			m.state = stateFailed
			m.errMsg = "check library: " + err.Error()
			return m, nil
		}
		if existing != nil {
			m.existing = existing
			m.state = stateExisting
			return m, nil
		}
		if m.linkType == "folder" && !(len(msg.nodes) == 1 && !msg.nodes[0].IsDir()) {
			m.picker = newPicker(msg.nodes)
			m.state = statePicker
			return m, nil
		}
		return m.submit()

	case decodeFrameMsg:
		if m.state != stateDecoding || msg.seq != m.decodeSeq {
			return m, nil
		}
		m.decodeFrame++
		if m.decodeFrame >= decodeFrames {
			return m, m.finishDecode()
		}
		return m, m.decodeTickCmd()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.MouseMsg:
		return m.updateMouse(msg)

	case tea.KeyMsg:
		return m.updateKey(msg)
	}

	// Clipboard reads (ctrl+v) and cursor blinks come back as messages
	// bubbles keeps to itself, so hand anything left over to the focused
	// prompt rather than dropping it.
	switch m.state {
	case stateURL:
		var cmd tea.Cmd
		m.urlInput, cmd = m.urlInput.Update(msg)
		m.refreshLinkHint()
		return m, cmd
	case stateName:
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// pickerVisible is how many picker rows the modal shows; the scroll math and
// the renderer have to agree on it or the cursor can drift out of view.
func (m *addlinkModel) pickerVisible() int {
	return max(3, m.app.height-12)
}

// pickerRowsTop is the body row the picker's first entry renders on: the
// modal's border and padding, then the title and the blank line under it.
func (m *addlinkModel) pickerRowsTop() int {
	return m.modal.y + styleModal.GetBorderTopSize() + styleModal.GetPaddingTop() + 2
}

// pickerRowAt maps body coordinates onto a picker row index, or -1 when the
// pointer isn't over one.
func (m *addlinkModel) pickerRowAt(x, y int) int {
	if !m.modal.contains(x, y) {
		return -1
	}
	row := y - m.pickerRowsTop()
	if row < 0 || row >= m.pickerVisible() {
		return -1
	}
	if i := m.picker.offset + row; i < len(m.picker.rows) {
		return i
	}
	return -1
}

// updateMouse drives the file picker; the other states are text prompts with
// nothing to aim at.
func (m *addlinkModel) updateMouse(msg tea.MouseMsg) (*addlinkModel, tea.Cmd) {
	if m.state != statePicker {
		return m, nil
	}
	if delta := wheelDelta(msg); delta != 0 {
		m.picker.move(delta, m.pickerVisible())
		return m, nil
	}
	if !leftPress(msg) {
		return m, nil
	}
	row := m.pickerRowAt(msg.X, msg.Y)
	if row < 0 {
		return m, nil
	}
	m.errMsg = ""
	m.picker.move(row-m.picker.cursor, m.pickerVisible())
	if m.clicks.press(clickPicker, row) {
		m.picker.toggle(row) // double click checks the row, like space
	}
	return m, nil
}

func (m *addlinkModel) updateKey(key tea.KeyMsg) (*addlinkModel, tea.Cmd) {
	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}

	switch m.state {
	case stateURL:
		switch key.String() {
		case "esc":
			return nil, nil
		case "up", "ctrl+p":
			m.previousURL()
			return m, nil
		case "down", "ctrl+n":
			m.nextURL()
			return m, nil
		case "enter":
			return m.submitURL(strings.TrimSpace(m.urlInput.Value()))
		}
		before := m.urlInput.Value()
		var cmd tea.Cmd
		m.urlInput, cmd = m.urlInput.Update(key)
		if m.urlInput.Value() != before {
			m.errMsg = ""
		}
		m.refreshLinkHint()
		return m, cmd

	case stateDecoding:
		// esc cuts the reveal short and leaves the link in the prompt,
		// enter cuts it short and acts on it; anything else waits it out
		switch key.String() {
		case "esc":
			return m, m.finishDecode()
		case "enter":
			m.applyDecoded()
			return m.submitURL(m.decodeTarget)
		}

	case stateListing:
		if key.String() == "esc" {
			return nil, nil
		}

	case statePicker:
		visible := m.pickerVisible()
		switch key.String() {
		case "esc":
			return nil, nil
		case "up", "k":
			m.picker.move(-1, visible)
		case "down", "j":
			m.picker.move(1, visible)
		case "pgup":
			m.picker.move(-visible, visible)
		case "pgdown":
			m.picker.move(visible, visible)
		case " ":
			m.picker.toggle(m.picker.cursor)
		case "A":
			m.picker.setAll(true)
		case "n":
			m.picker.setAll(false)
		case "enter":
			if count, _ := m.picker.totals(); count == 0 {
				m.errMsg = "nothing selected"
				return m, nil
			}
			m.errMsg = ""
			return m.submit()
		}
		return m, nil

	case stateName:
		switch key.String() {
		case "esc":
			if m.linkType == "folder" && len(m.picker.rows) > 0 {
				m.state = statePicker
				return m, nil
			}
			return nil, nil
		case "enter":
			if err := m.enqueue(m.nameInput.Value()); err != nil {
				m.errMsg = err.Error()
				return m, nil
			}
			return nil, nil
		}
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(key)
		return m, cmd

	case stateExisting:
		switch key.String() {
		case "esc":
			return nil, nil
		case "enter":
			m.app.downloads.selectDownload(m.existing.ID)
			if m.existing.LinkType == "folder" {
				m.app.downloads.notice = "reusing existing download — press R to refresh its listing"
			} else {
				m.app.downloads.notice = "reusing existing download"
			}
			return nil, nil
		}

	case stateFailed:
		switch key.String() {
		case "esc", "q":
			return nil, nil
		default:
			m.state = stateURL
			m.errMsg = ""
			return m, textinput.Blink
		}
	}
	return m, nil
}

// submit enqueues the download under the name derived from the link, which
// needs no confirmation when nothing else claims it. A collision (or an
// enqueue that fails outright) falls back to the name prompt so the user can
// settle it. A nil model closes the modal, as everywhere else in update.
func (m *addlinkModel) submit() (*addlinkModel, tea.Cmd) {
	root := m.nodes[0]
	name := naming.ForNode(root.Name, root.Handle)
	unique := naming.EnsureUnique(m.app.cfg.DownloadDir, name, m.pendingNames())
	if unique == name {
		err := m.enqueue(name)
		if err == nil {
			return nil, nil
		}
		m.errMsg = err.Error()
	}
	return m, m.promptName(unique)
}

// promptName asks the user to confirm a name that couldn't be used as-is; the
// prompt starts on the deduplicated suggestion.
func (m *addlinkModel) promptName(name string) tea.Cmd {
	m.nameInput.SetValue(name)
	m.nameInput.CursorEnd()
	m.state = stateName
	m.urlInput.Blur()
	return tea.Batch(m.nameInput.Focus(), textinput.Blink)
}

// pendingNames prevents collisions with not-yet-materialized downloads.
func (m *addlinkModel) pendingNames() map[string]bool {
	taken := map[string]bool{}
	if rows, err := m.app.db.Downloads(); err == nil {
		for _, dl := range rows {
			taken[dl.Name] = true
		}
	}
	return taken
}

func (m *addlinkModel) enqueue(rawName string) error {
	root := m.nodes[0]
	existing, err := m.app.db.FindByResource(m.linkType, root.Handle)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("%q is already in the library; reuse the existing download", existing.Name)
	}

	name := naming.Sanitize(rawName)
	if name == "" {
		return fmt.Errorf("name can't be empty")
	}
	dest := filepath.Join(m.app.cfg.DownloadDir, name)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%q already exists in the library", name)
	}

	dl := &db.Download{
		URL:      m.url,
		Handle:   root.Handle,
		LinkType: m.linkType,
		Name:     name,
		DestPath: dest,
	}

	var files []db.File
	switch {
	case m.linkType == "file":
		dl.TotalBytes = root.Size
		files = append(files, db.File{
			NodeHandle: root.Handle,
			RemotePath: root.Path,
			LocalPath:  dest,
			Size:       root.Size,
			Queued:     true,
		})

	case len(m.picker.rows) == 0:
		// folder link whose root is a single file (deep link)
		dl.TotalBytes = root.Size
		dl.Selection = root.Handle
		files = append(files, db.File{
			NodeHandle: root.Handle,
			RemotePath: root.Path,
			LocalPath:  filepath.Join(dest, naming.Sanitize(root.Name)),
			Size:       root.Size,
			Queued:     true,
		})
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}

	default:
		// every remote file is recorded; the ones left unselected stay
		// visible outside the queue and can be added later from the file pane
		dl.Selection = strings.Join(m.picker.minimalHandles(), ",")
		files = listingFiles(dl.DestPath, m.nodes)
		for i := range files {
			files[i].Queued = m.picker.selected[files[i].NodeHandle]
			if files[i].Queued {
				dl.TotalBytes += files[i].Size
			}
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
	}

	if _, err := m.app.db.InsertDownload(dl, files); err != nil {
		return err
	}
	m.app.eng.Kick()
	m.app.downloads.reload()
	return nil
}

// listingFiles maps a link listing onto db.File rows rooted at destPath.
// The refresh flow reuses it so merged rows land at the same local paths
// enqueue would have chosen.
func listingFiles(destPath string, nodes []mega.Node) []db.File {
	rootPath := nodes[0].Path
	var out []db.File
	for _, n := range nodes {
		if n.IsDir() {
			continue
		}
		rel := strings.TrimPrefix(n.Path, rootPath)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			rel = naming.Sanitize(n.Name)
		}
		out = append(out, db.File{
			NodeHandle: n.Handle,
			RemotePath: n.Path,
			LocalPath:  filepath.Join(destPath, filepath.FromSlash(rel)),
			Size:       n.Size,
		})
	}
	return out
}

func (m *addlinkModel) help() string {
	switch m.state {
	case stateURL:
		return renderShortcuts(
			shortcut{keys: []string{"↑/↓", "ctrl+p/n"}, label: "history"},
			shortcut{keys: []string{"enter"}, label: "submit"},
			shortcut{keys: []string{"esc"}, label: "cancel"},
		)
	case stateDecoding:
		return renderShortcuts(
			shortcut{keys: []string{"enter"}, label: "submit"},
			shortcut{keys: []string{"esc"}, label: "skip"},
		)
	case stateListing:
		return renderShortcuts(shortcut{keys: []string{"esc"}, label: "cancel"})
	case statePicker:
		return renderShortcuts(
			shortcut{keys: []string{"space"}, label: "toggle"},
			shortcut{keys: []string{"A"}, label: "all"},
			shortcut{keys: []string{"n"}, label: "none"},
			shortcut{keys: []string{"enter"}, label: "continue"},
			shortcut{keys: []string{"esc"}, label: "cancel"},
		)
	case stateName:
		return renderShortcuts(
			shortcut{keys: []string{"enter"}, label: "start download"},
			shortcut{keys: []string{"esc"}, label: "back"},
		)
	case stateExisting:
		return renderShortcuts(
			shortcut{keys: []string{"enter"}, label: "reuse existing"},
			shortcut{keys: []string{"esc"}, label: "cancel"},
		)
	}
	return renderShortcuts(shortcut{keys: []string{"esc"}, label: "close"})
}

func (m *addlinkModel) view() string {
	var body string
	switch m.state {
	case stateURL:
		body = styleTitle.Render("Add mega.nz link") + "\n\n" + m.urlInputView()
		if m.errMsg != "" {
			body += "\n\n" + styleError.Render(m.errMsg)
		}
	case stateDecoding:
		// the animation stands in for the input's value: same title and
		// prompt, padded to the input's rendered width so nothing shifts
		prompt := m.urlInput.PromptStyle.Render(m.urlInput.Prompt)
		width := m.urlInput.Width + 1
		frame := m.decodeFrameView(width)
		body = styleTitle.Render("Add mega.nz link") + "\n\n" +
			prompt + lipgloss.NewStyle().Width(width).Render(frame)
	case stateListing:
		body = styleTitle.Render("Add mega.nz link") + "\n\n" +
			m.spin.View() + " fetching listing…\n" + styleDim.Render(truncateMiddle(m.url, 70))
	case statePicker:
		body = styleTitle.Render("Choose files") + "\n\n" +
			m.picker.view(m.app.width-8, m.pickerVisible())
		if m.errMsg != "" {
			body += "\n" + styleError.Render(m.errMsg)
		}
	case stateName:
		count, bytes := 1, m.nodes[0].Size
		if len(m.picker.rows) > 0 {
			count, bytes = m.picker.totals()
		}
		body = styleTitle.Render("Name already taken") + "\n\n" +
			m.nameInput.View() + "\n\n" +
			styleDim.Render(fmt.Sprintf("%d file(s), %s → %s/", count, humanBytes(bytes),
				truncateMiddle(m.app.cfg.DownloadDir, 50)))
		if m.errMsg != "" {
			body += "\n\n" + styleError.Render(m.errMsg)
		}
	case stateExisting:
		body = styleTitle.Render("Already in library") + "\n\n" +
			fmt.Sprintf("%q already represents this MEGA %s.\n\n", m.existing.Name, m.existing.LinkType) +
			styleDim.Render(fmt.Sprintf("%s  %s\n%s",
				m.existing.Status,
				humanBytes(m.existing.TotalBytes),
				truncateMiddle(m.existing.DestPath, 70))) +
			"\n\nReuse this download instead of creating another copy?"
	case stateFailed:
		body = styleTitle.Render("Listing failed") + "\n\n" +
			styleError.Render(truncate(m.errMsg, 80)) + "\n\n" +
			styleDim.Render("press any key to edit the link, esc to close")
	}
	return styleModal.Render(body)
}

func (m *addlinkModel) decodeFrameView(width int) string {
	frame := truncate(
		decodeAnimFrame(m.decodeSrc, m.decodeTarget, m.decodeFrame, decodeFrames),
		width,
	)
	return styleDecode.Render(frame)
}
