package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"megadl/internal/db"
)

// deleteModel is the confirmation behind d: it drops a download from the
// library and takes the folder (or, for file links, the file) it occupies on
// disk with it.
type deleteModel struct {
	app    *App
	dl     *db.Download
	errMsg string
}

func newDeleteModel(app *App, dl *db.Download) *deleteModel {
	return &deleteModel{app: app, dl: dl}
}

// update returns nil to close the modal.
func (m *deleteModel) update(msg tea.Msg) (*deleteModel, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter", "y", "Y", "d":
		if err := m.apply(); err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.app.downloads.reload()
		m.app.downloads.notice = "deleted " + m.dl.Name
		return nil, nil
	}
	// anything else cancels: the destructive half needs a deliberate key
	return nil, nil
}

// apply removes the destination from disk and then the download's records.
// The disk goes first so a failure there leaves the database describing what
// is actually on disk.
func (m *deleteModel) apply() error {
	if err := m.deleteFromDisk(); err != nil {
		return err
	}
	return m.app.db.DeleteDownload(m.dl.ID)
}

// deleteFromDisk removes a download's destination, which is its folder for a
// folder link and the file itself for a file link. A folder link's partials
// live inside it, but a file link's sits beside the file, so that one is
// removed by hand.
func (m *deleteModel) deleteFromDisk() error {
	dest := filepath.Clean(strings.TrimSpace(m.dl.DestPath))
	if dest == "" || dest == "." {
		return errors.New("no destination recorded")
	}
	if dest == filepath.Clean(m.app.cfg.DownloadDir) {
		return errors.New("destination is the library root")
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if m.dl.LinkType == "file" {
		tmp := filepath.Join(filepath.Dir(dest), ".megatmp."+m.dl.Handle)
		if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (m *deleteModel) help() string {
	return renderShortcuts(
		shortcut{keys: []string{"y/⏎"}, label: "delete"},
		shortcut{keys: []string{"esc"}, label: "cancel"},
	)
}

func (m *deleteModel) view() string {
	noun := "folder"
	if m.dl.LinkType == "file" {
		noun = "file"
	}
	body := styleTitle.Render("Delete download") + "\n\n" +
		truncateMiddle(m.dl.Name, 60) + "\n\n" +
		styleWarn.Render("this deletes the "+noun+" from disk:") + "\n" +
		styleDim.Render(truncateMiddle(m.dl.DestPath, 60))
	if m.errMsg != "" {
		body += "\n\n" + styleError.Render(m.errMsg)
	}
	return styleModal.Render(body)
}
