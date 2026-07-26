// Package ui is the Bubble Tea front end: a single downloads view
// over the shared engine and database.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"megadl/internal/config"
	"megadl/internal/db"
	"megadl/internal/engine"
	"megadl/internal/mega"
)

// engineMsg signals that engine/db state changed.
type engineMsg struct{}

// tickMsg refreshes rates and the clock-driven quota window.
type tickMsg time.Time

type App struct {
	cfg *config.Config
	db  *db.DB
	eng *engine.Engine
	drv mega.Driver

	width, height int
	// body geometry from the last render, for translating mouse events
	bodyTop, bodyHeight int

	downloads downloadsModel
	addlink   *addlinkModel
	rename    *renameModel

	spinner  spinner.Model
	spinning bool // spinner tick loop in flight

	quota6h int64

	fatal string
}

func NewApp(cfg *config.Config, database *db.DB, eng *engine.Engine, drv mega.Driver) *App {
	a := &App{cfg: cfg, db: database, eng: eng, drv: drv}
	a.spinner = spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(styleSpinner),
	)
	a.downloads = newDownloadsModel(a)
	return a
}

func (a *App) Init() tea.Cmd {
	a.refreshQuota()
	a.downloads.restore()
	return tea.Batch(a.waitEngine(), tickCmd(), a.spinCmd())
}

func (a *App) waitEngine() tea.Cmd {
	return func() tea.Msg {
		<-a.eng.Notify
		return engineMsg{}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (a *App) refreshQuota() {
	a.quota6h, _ = a.db.BytesSince(time.Now().Add(-6 * time.Hour))
}

// downloading reports whether the engine is fetching a file right now.
func (a *App) downloading() bool {
	if a.eng == nil {
		return false
	}
	snap := a.eng.Snapshot()
	return snap.ActiveID != 0 && snap.CurrentFile != ""
}

// spinCmd starts the statusbar spinner's tick loop when a download is in
// flight; the loop stops itself once the engine goes idle.
func (a *App) spinCmd() tea.Cmd {
	if a.spinning || !a.downloading() {
		return nil
	}
	a.spinning = true
	return a.spinner.Tick
}

// isPaste reports whether a key event carries clipboard text: a bracketed
// paste from the terminal, or ctrl+v, which bubbles' text inputs answer with
// a clipboard read. Bracketed pastes stringify as "[text]", so they never
// collide with the single-key shortcuts.
func isPaste(key tea.KeyMsg) bool {
	return key.Paste || key.Type == tea.KeyCtrlV
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil

	case engineMsg:
		a.refreshQuota()
		a.downloads.reload()
		return a, tea.Batch(a.waitEngine(), a.spinCmd())

	case tickMsg:
		a.refreshQuota()
		return a, tea.Batch(tickCmd(), a.spinCmd())

	case spinner.TickMsg:
		if !a.downloading() {
			a.spinning = false
			return a, nil
		}
		var cmd tea.Cmd
		a.spinner, cmd = a.spinner.Update(msg)
		return a, cmd

	case tea.MouseMsg:
		// header and footer are inert; everything else is addressed in
		// body coordinates
		if msg.Y < a.bodyTop || msg.Y >= a.bodyTop+a.bodyHeight {
			return a, nil
		}
		msg.Y -= a.bodyTop
		if a.rename != nil {
			return a, nil // a bare prompt has nothing to aim at
		}
		if a.addlink != nil {
			model, cmd := a.addlink.update(msg)
			a.addlink = model
			return a, cmd
		}
		return a, a.downloads.update(msg)
	}

	// modal flows capture everything while open, pastes included
	if a.addlink != nil {
		model, cmd := a.addlink.update(msg)
		a.addlink = model
		return a, cmd
	}
	if a.rename != nil {
		model, cmd := a.rename.update(msg)
		a.rename = model
		return a, cmd
	}

	if key, ok := msg.(tea.KeyMsg); ok {
		// pasting anywhere means "add this link": open the dialog and let
		// the paste land in its URL prompt
		if isPaste(key) {
			a.addlink = newAddlinkModel(a)
			initCmd := a.addlink.init()
			model, cmd := a.addlink.update(key)
			a.addlink = model
			return a, tea.Batch(initCmd, cmd)
		}
		switch key.String() {
		case "q", "ctrl+c":
			return a, tea.Quit
		case "a":
			a.addlink = newAddlinkModel(a)
			return a, a.addlink.init()
		}
	}

	return a, a.downloads.update(msg)
}

func (a *App) View() string {
	if a.fatal != "" {
		return styleError.Render("fatal: " + a.fatal)
	}
	if a.width == 0 {
		return "loading..."
	}

	header := a.headerView()
	footer := a.footerView()
	bodyHeight := a.height - lipgloss.Height(header) - lipgloss.Height(footer)
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	a.bodyTop, a.bodyHeight = lipgloss.Height(header), bodyHeight

	body := a.downloads.view(a.width, bodyHeight)
	body = lipgloss.NewStyle().Height(bodyHeight).MaxHeight(bodyHeight).Render(body)
	switch {
	case a.addlink != nil:
		// add-link flow renders as a dialog centered over the downloads view
		dialog := a.addlink.view()
		a.addlink.modal = overlayRect(dialog, a.width, bodyHeight)
		body = overlayCenter(body, dialog, a.width, bodyHeight)
	case a.rename != nil:
		body = overlayCenter(body, a.rename.view(), a.width, bodyHeight)
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (a *App) headerView() string {
	title := styleLogo.Render(" 󰇚 ") +
		styleLogoBold.Render("MEGA") +
		styleLogo.Render(" DL")
	count := styleDim.Render(fmt.Sprintf("%d downloads", len(a.downloads.rows)))
	gap := a.width - lipgloss.Width(title) - lipgloss.Width(count) - 1
	if gap < 1 {
		gap = 1
	}
	line := title + strings.Repeat(" ", gap) + count
	rule := styleDim.Render(strings.Repeat("─", max(1, a.width)))
	return line + "\n" + rule
}

func (a *App) footerView() string {
	quota := styleDim.Render("↓ ") +
		stylePrimaryText.Bold(true).Render(fmt.Sprintf("%.1f", float64(a.quota6h)/(1<<30))) +
		styleDim.Render(" GiB in last 6h")

	help := a.helpLine()
	gap := a.width - lipgloss.Width(quota) - lipgloss.Width(help)
	if gap < 1 {
		gap = 1
	}
	line := help + strings.Repeat(" ", gap) + quota

	if a.eng != nil {
		if bar := statusbarLine(a.eng.Snapshot(), a.spinner.View(), a.width); bar != "" {
			return bar + "\n" + line
		}
	}
	return line
}

// statusbarLine renders the strip above the footer for the file the engine
// is fetching now: spinner, name, progress bar, percent, bytes, and rate.
// Empty when nothing is downloading.
func statusbarLine(snap engine.Snapshot, frame string, width int) string {
	if snap.ActiveID == 0 || snap.CurrentFile == "" {
		return ""
	}
	frac := 0.0
	if snap.FileSize > 0 {
		frac = min(1, max(0, float64(snap.FileDone)/float64(snap.FileSize)))
	}
	percent := fmt.Sprintf("%3.0f%%", frac*100)
	bytes := bytesPair(snap.FileDone, snap.FileSize)
	// A zero rate keeps its (blank) column so the line doesn't reflow
	// when the transfer stalls or has just started.
	const rateW = len("1023.9 KiB/s")
	rate := fmt.Sprintf("%*s", rateW, humanRate(snap.Rate))

	// " ⠋ name  ████░░ 42%  12.4 / 30.0 MiB  3.4 MiB/s ". Every field
	// after the bar has a constant width so the bar never shifts as the
	// numbers tick. In narrow terminals the byte counts give way to the
	// name, then the rate, then the bar shrinks.
	const minNameW = 12
	barW := 20
	stats := bytes + "  " + rate
	nameW := func() int {
		w := width - 3 - 2 - barW - 1 - len(percent) - 1
		if stats != "" {
			w -= 2 + lipgloss.Width(stats)
		}
		return w
	}
	if nameW() < minNameW {
		stats = rate
	}
	if nameW() < minNameW {
		stats = ""
	}
	if nameW() < minNameW {
		barW = max(6, barW-(minNameW-nameW()))
	}
	if nameW() < 1 {
		return ""
	}

	left := " " + styleAccent.Render(frame) + " " + truncate(snap.CurrentFile, nameW())
	tail := progressBar(barW, frac) + " " + styleTitle.Render(percent)
	switch stats {
	case "":
	case rate:
		tail += "  " + styleOK.Render(rate)
	default:
		tail += "  " + styleDim.Render(bytes) + "  " + styleOK.Render(rate)
	}
	gap := max(2, width-lipgloss.Width(left)-lipgloss.Width(tail)-1)
	return left + strings.Repeat(" ", gap) + tail + " "
}

func (a *App) helpLine() string {
	switch {
	case a.addlink != nil:
		return a.addlink.help()
	case a.rename != nil:
		return a.rename.help()
	}
	return a.downloads.help()
}
