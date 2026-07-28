package ui

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"megadl/internal/config"
	"megadl/internal/db"
	"megadl/internal/engine"
	"megadl/internal/mega"
)

func openAddlinkTestApp(t *testing.T) (*App, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	app := &App{
		cfg: &config.Config{DownloadDir: dir},
		db:  database,
		eng: engine.New(nil, database),
	}
	app.downloads = newDownloadsModel(app)
	return app, database
}

func TestAddlinkSuggestsExistingResourceInsteadOfDuplicate(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	id, err := database.InsertDownload(&db.Download{
		URL:      "https://mega.nz/folder/AAAAAAAA#old",
		Handle:   "root",
		LinkType: "folder",
		Name:     "Skins",
		DestPath: filepath.Join(app.cfg.DownloadDir, "Skins"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkCompleted(id, db.StatusDone); err != nil {
		t.Fatal(err)
	}
	app.downloads.reload()

	url := "https://mega.nz/folder/AAAAAAAA#new"
	m := newAddlinkModel(app)
	m.url, m.linkType, m.state = url, "folder", stateListing
	model, cmd := m.update(listResultMsg{
		url: url,
		nodes: []mega.Node{{
			Path: "/Skins", Name: "Skins", Type: "folder", Handle: "root",
		}},
	})
	if cmd != nil || model.state != stateExisting || model.existing == nil || model.existing.ID != id {
		t.Fatalf("existing state = %+v, cmd=%v", model, cmd)
	}

	model, cmd = model.update(tea.KeyMsg{Type: tea.KeyEnter})
	if model != nil || cmd != nil {
		t.Fatalf("reuse should close modal: model=%+v, cmd=%v", model, cmd)
	}
	rows, err := database.Downloads()
	if err != nil || len(rows) != 1 {
		t.Fatalf("downloads after reuse = %+v, %v", rows, err)
	}
	if app.downloads.rows[app.downloads.cursor].ID != id {
		t.Fatalf("selected download = %d, want %d",
			app.downloads.rows[app.downloads.cursor].ID, id)
	}
}

func TestAddlinkEnqueuesWithoutNamePromptWhenNameIsFree(t *testing.T) {
	app, database := openAddlinkTestApp(t)

	url := "https://mega.nz/file/AAAAAAAA#key"
	m := newAddlinkModel(app)
	m.url, m.linkType, m.state = url, "file", stateListing
	model, cmd := m.update(listResultMsg{
		url: url,
		nodes: []mega.Node{{
			Path: "/skins.zip", Name: "skins.zip", Type: "file",
			Handle: "root", Size: 10,
		}},
	})
	if model != nil || cmd != nil {
		t.Fatalf("free name should close modal: model=%+v, cmd=%v", model, cmd)
	}

	rows, err := database.Downloads()
	if err != nil || len(rows) != 1 {
		t.Fatalf("downloads = %+v, %v", rows, err)
	}
	if rows[0].Name != "skins.zip" {
		t.Fatalf("name = %q, want %q", rows[0].Name, "skins.zip")
	}
	if want := filepath.Join(app.cfg.DownloadDir, "skins.zip"); rows[0].DestPath != want {
		t.Fatalf("dest = %q, want %q", rows[0].DestPath, want)
	}
}

// A folder link keeps the picker; only the name prompt behind it is skipped.
func TestAddlinkEnqueuesFromPickerWithoutNamePrompt(t *testing.T) {
	app, database := openAddlinkTestApp(t)

	url := "https://mega.nz/folder/AAAAAAAA#key"
	m := newAddlinkModel(app)
	m.url, m.linkType, m.state = url, "folder", stateListing
	model, _ := m.update(listResultMsg{
		url: url,
		nodes: []mega.Node{
			{Path: "/Skins", Name: "Skins", Type: "folder", Handle: "root"},
			{Path: "/Skins/a.zip", Name: "a.zip", Type: "file", Handle: "a", Size: 10},
		},
	})
	if model == nil || model.state != statePicker {
		t.Fatalf("state = %+v, want picker", model)
	}

	model, cmd := model.update(tea.KeyMsg{Type: tea.KeyEnter})
	if model != nil || cmd != nil {
		t.Fatalf("free name should close modal: model=%+v, cmd=%v", model, cmd)
	}
	rows, err := database.Downloads()
	if err != nil || len(rows) != 1 || rows[0].Name != "Skins" {
		t.Fatalf("downloads = %+v, %v", rows, err)
	}
}

func TestAddlinkStillDisambiguatesDifferentResourceWithSameName(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	if _, err := database.InsertDownload(&db.Download{
		URL:      "old",
		Handle:   "old-root",
		LinkType: "file",
		Name:     "skins.zip",
		DestPath: filepath.Join(app.cfg.DownloadDir, "skins.zip"),
	}, nil); err != nil {
		t.Fatal(err)
	}

	url := "https://mega.nz/file/AAAAAAAA#key"
	m := newAddlinkModel(app)
	m.url, m.linkType, m.state = url, "file", stateListing
	model, _ := m.update(listResultMsg{
		url: url,
		nodes: []mega.Node{{
			Path: "/skins.zip", Name: "skins.zip", Type: "file",
			Handle: "new-root", Size: 10,
		}},
	})
	if model.state != stateName {
		t.Fatalf("state = %v, want name", model.state)
	}
	if got := model.nameInput.Value(); got != "skins (2).zip" {
		t.Fatalf("name = %q, want %q", got, "skins (2).zip")
	}
}

func TestEnqueueRejectsResourceAddedAfterListing(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	if _, err := database.InsertDownload(&db.Download{
		URL:      "old",
		Handle:   "root",
		LinkType: "file",
		Name:     "skins.zip",
		DestPath: filepath.Join(app.cfg.DownloadDir, "skins.zip"),
	}, nil); err != nil {
		t.Fatal(err)
	}

	m := newAddlinkModel(app)
	m.url, m.linkType = "new", "file"
	m.nodes = []mega.Node{{
		Path: "/skins.zip", Name: "skins.zip", Type: "file",
		Handle: "root", Size: 10,
	}}

	err := m.enqueue("skins (2).zip")
	if err == nil || !strings.Contains(err.Error(), "reuse the existing download") {
		t.Fatalf("enqueue error = %v", err)
	}
	rows, err := database.Downloads()
	if err != nil || len(rows) != 1 {
		t.Fatalf("downloads after enqueue = %+v, %v", rows, err)
	}
}

func TestDecodeBase64MegaLink(t *testing.T) {
	link := "https://mega.nz/folder/AAAAAAAA#0123456789abcdefghijkl"
	once := base64.StdEncoding.EncodeToString([]byte(link))
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{"single std", once, link, true},
		{"single raw url-safe", base64.RawURLEncoding.EncodeToString([]byte(link)), link, true},
		{"double encoded", base64.StdEncoding.EncodeToString([]byte(once)), link, true},
		{"wrapped paste", once[:20] + "\n " + once[20:], link, true},
		{"decoded padding trimmed", base64.StdEncoding.EncodeToString([]byte("  " + link + "\n")), link, true},
		{"plain link", link, "", false},
		{"free text", "not a link at all", "", false},
		{"base64 of garbage", base64.StdEncoding.EncodeToString([]byte("some perfectly ordinary text")), "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		got, ok := decodeBase64MegaLink(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("%s: decodeBase64MegaLink(%q) = %q, %v; want %q, %v",
				tt.name, tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestAddlinkDecodesBase64LinkAndAnimatesReveal(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	link := "https://mega.nz/file/BBBBBBBB#0123456789abcdefghijkl"
	encoded := base64.StdEncoding.EncodeToString([]byte(link))

	m := newAddlinkModel(app)
	m.urlInput.SetValue(encoded)
	_, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateDecoding || m.decodeTarget != link || cmd == nil {
		t.Fatalf("after submit: state=%v target=%q cmd=%v", m.state, m.decodeTarget, cmd)
	}

	// run the animation to completion
	for i := 0; i < decodeFrames && m.state == stateDecoding; i++ {
		m.update(decodeFrameMsg{seq: m.decodeSeq})
	}
	if m.state != stateURL || m.urlInput.Value() != link {
		t.Fatalf("after animation: state=%v input=%q", m.state, m.urlInput.Value())
	}
	if got := m.urlInput.TextStyle.GetForeground(); got != colorOrange {
		t.Fatalf("decoded link should stay orange, got %v", got)
	}

	// stale frames from the finished animation are ignored
	m.update(decodeFrameMsg{seq: m.decodeSeq - 1})
	if m.state != stateURL || m.urlInput.Value() != link {
		t.Fatalf("after stale frame: state=%v input=%q", m.state, m.urlInput.Value())
	}
}

func TestAddlinkColorsBase64InputOrange(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	link := "https://mega.nz/file/EEEEEEEE#0123456789abcdefghijkl"
	encoded := base64.StdEncoding.EncodeToString([]byte(link))

	m := newAddlinkModel(app)
	m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(encoded)})
	if got := m.urlInput.TextStyle.GetForeground(); got != colorOrange {
		t.Fatalf("foreground after paste = %v, want %v", got, colorOrange)
	}

	// breaking the base64 clears the hint
	m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := m.urlInput.TextStyle.GetForeground(); got == colorOrange {
		t.Fatalf("foreground after edit = %v, want default", got)
	}

	// plain mega links are orange too
	m = newAddlinkModel(app)
	m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(link)})
	if got := m.urlInput.TextStyle.GetForeground(); got != colorOrange {
		t.Fatalf("foreground after link paste = %v, want %v", got, colorOrange)
	}
}

func TestAddlinkClearsInvalidLinkErrorWhenInputChanges(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	m := newAddlinkModel(app)
	m.urlInput.SetValue("invalid")

	m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.errMsg == "" {
		t.Fatal("expected invalid link error after submit")
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" link")})
	if m.errMsg != "" {
		t.Fatalf("error after typing = %q, want empty", m.errMsg)
	}
}

// addlinkStates puts m through every state the dialog renders, so a layout
// check can walk all of them.
func addlinkStates(m *addlinkModel) []struct {
	name  string
	setup func()
} {
	longURL := "https://mega.nz/folder/EEEEEEEE#" + strings.Repeat("k", 60)
	longErr := "list: Get \"" + longURL + "\": dial tcp: lookup g.api.mega.co.nz: no such host"
	nodes := []mega.Node{
		{Path: "/Album", Name: "Album", Type: "folder", Handle: "root"},
		{Path: "/Album/" + strings.Repeat("long-name-", 8) + ".mkv",
			Name: strings.Repeat("long-name-", 8) + ".mkv",
			Type: "file", Handle: "f1", Parent: "root", Size: 1 << 30},
	}
	return []struct {
		name  string
		setup func()
	}{
		{"url", func() { m.state, m.errMsg = stateURL, "" }},
		{"url error", func() { m.state, m.errMsg = stateURL, longErr }},
		{"url value", func() {
			m.state, m.errMsg = stateURL, ""
			m.urlInput.SetValue(longURL)
			m.urlInput.CursorEnd()
		}},
		{"decoding", func() {
			m.state, m.decodeSrc, m.decodeTarget = stateDecoding, longURL, longURL
			m.decodeFrame = decodeFrames / 2
		}},
		{"listing", func() { m.state, m.url = stateListing, longURL }},
		{"picker", func() {
			m.state, m.errMsg = statePicker, "nothing selected"
			m.picker = newPicker(nodes)
		}},
		{"name", func() {
			m.state, m.errMsg, m.nodes = stateName, longErr, nodes
			m.nameInput.SetValue(strings.Repeat("name-", 12))
			m.nameInput.CursorEnd()
		}},
		{"existing", func() {
			m.state = stateExisting
			m.existing = &db.Download{
				Name:     strings.Repeat("album-", 12),
				LinkType: "folder",
				Status:   db.StatusDone,
				DestPath: "/Users/someone/Downloads/" + strings.Repeat("album-", 12),
			}
		}},
		{"failed", func() { m.state, m.errMsg = stateFailed, longErr }},
	}
}

func TestAddlinkDialogOpensNoWiderThanTheTerminal(t *testing.T) {
	app, _ := openAddlinkTestApp(t)

	for _, width := range []int{40, 60, 79, 100} {
		app.width, app.height = width, 24
		m := newAddlinkModel(app)
		for _, state := range addlinkStates(m) {
			state.setup()
			assertFitsWidth(t, m.view(), width, "state "+state.name)
		}
	}
}

func TestAddlinkDialogStopsGrowingOnWideTerminal(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	app.width, app.height = 300, 40
	m := newAddlinkModel(app)

	want := modalWidth + styleModal.GetHorizontalFrameSize()
	for _, state := range addlinkStates(m) {
		if state.name == "picker" {
			continue // the file list is allowed the wider cap
		}
		state.setup()
		if got := lipgloss.Width(m.view()); got > want {
			t.Fatalf("state %s: dialog width = %d, want at most %d", state.name, got, want)
		}
	}
}

func TestAddlinkDecodeAnimationKeepsDialogWidthStable(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	link := "https://mega.nz/file/DDDDDDDD#0123456789abcdefghijkl"
	encoded := base64.StdEncoding.EncodeToString([]byte(link))

	m := newAddlinkModel(app)
	want := lipgloss.Width(m.view()) // empty input showing the placeholder

	m.urlInput.SetValue(encoded)
	if got := lipgloss.Width(m.view()); got != want {
		t.Fatalf("after paste: dialog width = %d, want %d", got, want)
	}

	m.decodeSrc, m.decodeTarget = encoded, link
	m.state = stateDecoding
	for m.decodeFrame = 0; m.decodeFrame <= decodeFrames; m.decodeFrame++ {
		if got := lipgloss.Width(m.view()); got != want {
			t.Fatalf("frame %d: dialog width = %d, want %d", m.decodeFrame, got, want)
		}
	}
}

func TestAddlinkEscSkipsDecodeAnimation(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	link := "https://mega.nz/folder/CCCCCCCC#0123456789abcdefghijkl"
	// double-encoded still resolves to the plain link
	encoded := base64.StdEncoding.EncodeToString(
		[]byte(base64.StdEncoding.EncodeToString([]byte(link))))

	m := newAddlinkModel(app)
	m.urlInput.SetValue(encoded)
	m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateDecoding || m.decodeTarget != link {
		t.Fatalf("after submit: state=%v target=%q", m.state, m.decodeTarget)
	}
	m.updateKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != stateURL || m.urlInput.Value() != link {
		t.Fatalf("after skip: state=%v input=%q", m.state, m.urlInput.Value())
	}
}

func TestAddlinkEnterDuringDecodeAnimationListsLink(t *testing.T) {
	app, _ := openAddlinkTestApp(t)
	link := "https://mega.nz/folder/CCCCCCCC#0123456789abcdefghijkl"
	encoded := base64.StdEncoding.EncodeToString([]byte(link))

	m := newAddlinkModel(app)
	m.urlInput.SetValue(encoded)
	m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.update(decodeFrameMsg{seq: m.decodeSeq})
	if m.state != stateDecoding {
		t.Fatalf("mid-animation state = %v, want stateDecoding", m.state)
	}

	// enter cuts the animation short and acts on the decoded link
	m, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateListing || m.url != link || m.linkType != "folder" {
		t.Fatalf("after enter: state=%v url=%q type=%q", m.state, m.url, m.linkType)
	}
	if m.urlInput.Value() != link {
		t.Fatalf("prompt = %q, want the decoded link", m.urlInput.Value())
	}
	if cmd == nil {
		t.Fatal("expected a listing command")
	}

	// frames left over from the cancelled animation don't reopen the prompt
	m.update(decodeFrameMsg{seq: m.decodeSeq - 1})
	if m.state != stateListing {
		t.Fatalf("after stale frame: state=%v, want stateListing", m.state)
	}
}

func TestAddlinkNavigatesSubmittedLinkHistory(t *testing.T) {
	app, database := openAddlinkTestApp(t)
	oldURL := "https://mega.nz/file/AAAAAAAA#old"
	newURL := "https://mega.nz/folder/BBBBBBBB#new"
	for i, url := range []string{oldURL, newURL, newURL} {
		if _, err := database.InsertDownload(&db.Download{
			URL:      url,
			Handle:   string(rune('a' + i)),
			LinkType: "file",
			Name:     string(rune('a' + i)),
			DestPath: filepath.Join(app.cfg.DownloadDir, string(rune('a'+i))),
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	app.downloads.reload()

	m := newAddlinkModel(app)
	m.urlInput.SetValue("draft")

	m.updateKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.urlInput.Value(); got != newURL {
		t.Fatalf("up = %q, want newest %q", got, newURL)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyCtrlP})
	if got := m.urlInput.Value(); got != oldURL {
		t.Fatalf("ctrl+p = %q, want older %q", got, oldURL)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.urlInput.Value(); got != oldURL {
		t.Fatalf("up past oldest = %q, want %q", got, oldURL)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyCtrlN})
	if got := m.urlInput.Value(); got != newURL {
		t.Fatalf("ctrl+n = %q, want newer %q", got, newURL)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.urlInput.Value(); got != "draft" {
		t.Fatalf("down past newest = %q, want draft", got)
	}

	m.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.urlInput.Value(); got != "draft" {
		t.Fatalf("down while at draft = %q, want draft", got)
	}
}
