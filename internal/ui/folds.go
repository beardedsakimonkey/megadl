package ui

import tea "github.com/charmbracelet/bubbletea"

type foldsSavedMsg struct {
	folders map[int64][]string
	err     error
}

func (m *downloadsModel) markFoldsDirty() {
	if m.foldDirty == nil {
		m.foldDirty = make(map[int64]bool)
	}
	m.foldDirty[m.filesFor] = true
}

// Only one save runs at a time, so rapid toggles cannot write out of order.
// Commands own a snapshot and never read the live model.
func (m *downloadsModel) saveFolds() tea.Cmd {
	if m.foldSaving || len(m.foldDirty) == 0 || m.app == nil || m.app.db == nil {
		return nil
	}
	folders := make(map[int64][]string, len(m.foldDirty))
	for id := range m.foldDirty {
		folders[id] = nil
		for path := range m.collapsed[id] {
			folders[id] = append(folders[id], path)
		}
	}
	m.foldDirty = nil
	m.foldSaving = true
	database := m.app.db
	return func() tea.Msg {
		return foldsSavedMsg{folders: folders, err: database.SetCollapsedDirs(folders)}
	}
}

func (m *downloadsModel) foldsSaved(msg foldsSavedMsg) tea.Cmd {
	m.foldSaving = false
	if msg.err != nil {
		if m.foldDirty == nil {
			m.foldDirty = make(map[int64]bool)
		}
		for id := range msg.folders {
			m.foldDirty[id] = true
		}
		m.foldQuitting = false
		m.setNoticeErr("save folder state failed: " + msg.err.Error())
		return nil
	}
	if cmd := m.saveFolds(); cmd != nil {
		return cmd
	}
	if m.foldQuitting {
		return tea.Quit
	}
	return nil
}

func (m *downloadsModel) quitAfterFolds() tea.Cmd {
	m.foldQuitting = true
	if cmd := m.saveFolds(); cmd != nil {
		return cmd
	}
	if m.foldSaving {
		return nil
	}
	return tea.Quit
}
