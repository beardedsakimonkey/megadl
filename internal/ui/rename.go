package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"megadl/internal/db"
	"megadl/internal/naming"
)

// renameModel is the modal prompt behind r: it renames a download along with
// the folder (or, for file links, the file) it occupies on disk.
type renameModel struct {
	app *App
	dl  *db.Download
	// width is the dialog's content width, fixed when it opened so it never
	// starts out wider than the terminal.
	width  int
	input  textinput.Model
	errMsg string
}

func newRenameModel(app *App, dl *db.Download) *renameModel {
	w := modalContentWidth(app.width, modalWidth)
	name := textinput.New()
	// The width is set before the value so bubbles works out which part of a
	// long name is on screen against the width it will be rendered at.
	name.Width = max(8, w-promptWidth(name)-1)
	name.SetValue(dl.Name)
	name.CursorEnd()
	return &renameModel{app: app, dl: dl, width: w, input: name}
}

func (m *renameModel) init() tea.Cmd {
	return tea.Batch(m.input.Focus(), textinput.Blink)
}

// update returns nil to close the modal.
func (m *renameModel) update(msg tea.Msg) (*renameModel, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		// cursor blinks and clipboard reads belong to the prompt
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		return nil, nil
	case "enter":
		name, err := m.apply()
		if err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.app.downloads.reload()
		m.app.downloads.notice = "renamed to " + name
		return nil, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// apply moves the download's destination on disk and repoints its records at
// the new path, returning the name it settled on. The disk move goes first so
// a failure there leaves the database describing what is actually on disk.
func (m *renameModel) apply() (string, error) {
	name := naming.Sanitize(m.input.Value())
	switch {
	case name == "":
		return "", errors.New("name can't be empty")
	case name == m.dl.Name:
		return name, nil
	}
	dest := filepath.Join(filepath.Dir(m.dl.DestPath), name)

	if err := m.checkFree(dest, name); err != nil {
		return "", err
	}
	if _, err := os.Lstat(m.dl.DestPath); err == nil {
		if err := os.Rename(m.dl.DestPath, dest); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := m.app.db.RenameDownload(m.dl.ID, name, m.dl.DestPath, dest); err != nil {
		return "", err
	}
	return name, nil
}

// checkFree reports whether dest is available for this download, on disk and
// in the library. A case-only rename on a case-insensitive filesystem stats as
// an existing entry, but it is the very entry being renamed.
func (m *renameModel) checkFree(dest, name string) error {
	if info, err := os.Lstat(dest); err == nil {
		current, err := os.Lstat(m.dl.DestPath)
		if err != nil || !os.SameFile(info, current) {
			return fmt.Errorf("%q already exists on disk", name)
		}
	}
	other, err := m.app.db.FindByDestPath(dest)
	if err != nil {
		return err
	}
	if other != nil && other.ID != m.dl.ID {
		return fmt.Errorf("%q is already in the library", name)
	}
	return nil
}

func (m *renameModel) help() string {
	return renderShortcuts(
		shortcut{keys: []string{"enter"}, label: "rename"},
		shortcut{keys: []string{"esc"}, label: "cancel"},
	)
}

func (m *renameModel) view() string {
	w := m.width
	body := styleTitle.Render("Rename download") + "\n\n" +
		m.input.View() + "\n\n" +
		styleDim.Render(truncateMiddle(filepath.Dir(m.dl.DestPath), w-1)+string(filepath.Separator))
	if m.errMsg != "" {
		body += "\n\n" + styleError.Render(wrap(m.errMsg, w))
	}
	return styleModal.Render(body)
}
