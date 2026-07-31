// Package ui is the Bubble Tea front end: a single downloads view
// over the shared engine and database.
package ui

import (
	"fmt"
	"path/filepath"
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

// retryKey skips the wait between chunk attempts. It is offered by the retry
// line itself rather than the shortcut row, since it is only ever worth
// pressing while that line is up.
const retryKey = "t"

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
	del       *deleteModel

	spinner  spinner.Model
	spinning bool // spinner tick loop in flight
	// shine is where the statusbar sweep's bands stand; its clock runs exactly
	// while the repaint loop is in flight
	shine shineClock

	quota6h int64
	// spark holds quotaWindow's transfer totals bucketed over time, oldest
	// first, for the header sparkline
	spark []int64

	fatal string
}

// The header describes one window of transfers two ways: quota6h is how many
// bytes landed in it, and spark is when. sparkBuckets divides the window into
// equal slices of time, few enough to keep the row narrow enough to sit in the
// header beside the title.
const (
	quotaWindow      = 6 * time.Hour
	quotaWindowLabel = "6h"
	sparkBuckets     = 8
	// sparkFull is the transfer a bar draws at full height. Bars are measured
	// against it rather than against each other, so a bar's height means the
	// same thing from one glance to the next: a busy stretch reads as busy even
	// when the rest of the window was idle, and the first few hundred MiB of a
	// download don't fill the row. One bucket taking the whole window's
	// approximate allowance is as steep as the row needs to go.
	sparkFull = 5 << 30
)

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
	return tea.Batch(a.waitEngine(), tickCmd(), a.spinCmd(), a.shineCmd())
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
	a.quota6h, _ = a.db.BytesSince(time.Now().Add(-quotaWindow))
	a.spark, _ = a.db.TransferBuckets(quotaWindow, sparkBuckets)
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

// sweeping reports whether the statusbar's bar has light travelling along it
// right now, which is the same question as whether a file is being fetched. A
// held queue's bands are lit but standing still, so they need no repaints.
func (a *App) sweeping() bool {
	if a.eng == nil {
		return false
	}
	return sweepRuns(a.eng.Snapshot())
}

// shineCmd starts the sweep's repaint loop when a transfer is in flight; like
// the spinner's, the loop drops itself once the bytes stop. The clock starts
// with the loop and from wherever the bands were left standing, so a resumed
// download picks the pattern up rather than jumping it somewhere new.
func (a *App) shineCmd() tea.Cmd {
	if !a.shine.since.IsZero() || !a.sweeping() {
		return nil
	}
	a.shine = a.shine.start(time.Now())
	return shineTickCmd()
}

// spinFrame is the current spinner glyph without its style, so rows can color
// it themselves or drop it into a fully highlighted line. Between downloads
// the frame simply holds still. An app that never went through NewApp has no
// frames to draw from; a still glyph beats the spinner's "(error)" text.
func (a *App) spinFrame() string {
	if a == nil || len(a.spinner.Spinner.Frames) == 0 {
		return spinner.MiniDot.Frames[0]
	}
	sp := a.spinner
	sp.Style = lipgloss.NewStyle()
	return sp.View()
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
		return a, tea.Batch(a.waitEngine(), a.spinCmd(), a.shineCmd())

	case tickMsg:
		a.refreshQuota()
		return a, tea.Batch(tickCmd(), a.spinCmd(), a.shineCmd())

	case cursorTickMsg:
		// Answered ahead of the modal flows below: a dialog opened while the
		// bars are still travelling would otherwise swallow the tick and strand
		// the loop, leaving the bars stuck at half width behind it.
		return a, a.downloads.cursorTick()

	case shineTickMsg:
		// Answered before the modals below for the same reason the cursor tick
		// is: a dialog opened over a running download would otherwise swallow
		// the tick and leave the bands stranded mid-bar.
		if !a.sweeping() {
			// The bands stop where this frame finds them, which is where the
			// next run will start them from.
			a.shine = a.shine.stop(time.Now())
			return a, nil
		}
		return a, shineTickCmd()

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
		if a.rename != nil || a.del != nil {
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
	if a.del != nil {
		model, cmd := a.del.update(msg)
		a.del = model
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
		case "p":
			// the queue is paused as a whole, so this ignores the cursor
			// and acts on whatever the status bar is showing
			a.eng.SetPaused(!a.eng.Paused())
			a.downloads.setNotice("")
			return a, nil
		case retryKey:
			// Only claimed while there is a wait to skip; with nothing
			// retrying the key falls through to the panes below, which is
			// where it would go if this case didn't exist.
			if a.eng.Snapshot().Retry.Waiting() {
				a.eng.RetryNow()
				a.downloads.setNotice("")
				return a, nil
			}
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
	case a.del != nil:
		body = overlayCenter(body, a.del.view(), a.width, bodyHeight)
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (a *App) headerView() string {
	title := styleLogoMark.Render(" ◣◥◤◢ ") +
		stylePrimaryText.Bold(true).Render("ＭＥＧＡ") +
		styleHelpKey.Bold(true).Render("ＤＬ")
	quota := styleDim.Render("↓ ") +
		quotaStyle(a.quota6h).Bold(true).Render(fmt.Sprintf("%.1f", float64(a.quota6h)/(1<<30))) +
		styleDim.Render(" GiB")
	// The sparkline joins the total whenever the header has room to keep them
	// both clear of the title; on a narrow terminal the number is what matters.
	right := quota
	if spark := a.sparkView(); spark != "" {
		if wide := spark + "  " + quota; a.width-lipgloss.Width(title)-lipgloss.Width(wide)-1 >= 2 {
			right = wide
		}
	}
	gap := max(1, a.width-lipgloss.Width(right)-lipgloss.Width(title)-1)
	line := title + strings.Repeat(" ", gap) + right
	return line + "\n" + a.paneRule("┬")
}

// paneRule closes off the pane region above or below. It carries the split
// between the two panes through as a junction, so the rules top and bottom meet
// the gutter that runs between them.
func (a *App) paneRule(junction string) string {
	listW, filesW := downloadPaneWidths(a.width, len(a.downloads.files) > 0)
	if filesW <= 0 {
		return styleDim.Render(strings.Repeat("─", max(1, a.width)))
	}
	return styleDim.Render(strings.Repeat("─", listW) + junction +
		strings.Repeat("─", a.width-listW-1))
}

// sparkView draws the transfer window as one labelled row of bars, colored
// with the total it sits beside. It is empty until the buckets have been read,
// so a header rendered before the first refresh simply omits it.
func (a *App) sparkView() string {
	if len(a.spark) == 0 {
		return ""
	}
	return styleDim.Render(quotaWindowLabel) + "  " +
		sparkline(a.spark, sparkFull, quotaStyle(a.quota6h))
}

// footerView draws everything under the panes: notices and the transfer strip
// each sit in a band of their own, fenced off by the pane rule above and a rule
// below, with the shortcuts under it. Every rule belongs to the band beneath it,
// so a band that has nothing to say takes its rule with it: with neither notice
// nor strip the shortcuts hang straight off the pane rule.
func (a *App) footerView() string {
	line := a.helpLine()
	if pad := (a.width - lipgloss.Width(line)) / 2; pad > 0 {
		line = strings.Repeat(" ", pad) + line
	}

	rule := styleDim.Render(strings.Repeat("─", max(1, a.width)))
	parts := []string{a.paneRule("┴")}
	if detail := a.downloads.detailView(a.width); detail != "" {
		parts = append(parts, detail)
	}
	if bar := a.statusbarView(); bar != "" {
		// the strip gets its own fence, so a notice above it reads as a
		// separate line rather than another row of the transfer band
		if len(parts) > 1 {
			parts = append(parts, rule)
		}
		parts = append(parts, bar)
	}
	if len(parts) > 1 {
		parts = append(parts, rule)
	}
	return strings.Join(append(parts, line), "\n")
}

// statusbarView draws the strip for the head of the queue: the file being
// fetched right now, or — while the queue is held, or between runs — the one it
// will pick up next. The strip follows the queue rather than remembering
// transfers, so it cannot describe work that is no longer waiting to happen: an
// empty queue leaves an empty footer.
func (a *App) statusbarView() string {
	if a.eng == nil {
		return ""
	}
	snap := a.eng.Snapshot()
	if snap.ActiveID != 0 && snap.CurrentFile != "" {
		// The live count reads zero between a file starting and its transfer
		// reporting how much is left, so floor it at the partial already on
		// disk the same way the file rows do: a resumed file's strip never
		// blinks back to 0% on the way to picking up where it left off.
		if head := a.downloads.head; head.file != nil && head.file.LocalPath == snap.CurrentPath {
			snap.FileDone = max(snap.FileDone, head.partial)
		}
		return statusbarLine(snap, a.spinner.View(), a.width, a.shine.offsetAt(time.Now()))
	}

	head := a.downloads.head
	if head.file == nil {
		return "" // nothing is queued, so there is nothing to draw
	}
	f := head.file
	// No bytes are moving, so the rate column stays blank and the partial on
	// disk says how far the file has already got. The estimate keeps the speed
	// the last run was managing, so a held queue still says what the file it is
	// holding has left to do once it picks back up.
	next := engine.Snapshot{
		ActiveID:    head.dl.ID,
		CurrentFile: filepath.Base(f.LocalPath),
		CurrentPath: f.LocalPath,
		FileSize:    f.Size,
		FileDone:    head.partial,
		AvgRate:     snap.AvgRate,
		Paused:      snap.Paused,
	}
	// the file's own marker, so the strip and its row in the file pane agree:
	// spinning while its download runs between files, held while the queue is
	// paused, waiting its turn otherwise
	fetching := head.dl.ID == snap.ActiveID
	frac := fileProgress(*f, snap, false, head.partial)
	marker := fileMarker(fileMarkerStateOf(*f, fetching, snap.Paused, frac), a.spinFrame())
	// Nothing is being fetched, so the bands stand where the clock stopped them
	// — the same bar a held queue draws, which is why resuming one neither
	// blinks the light off nor jumps it while the engine works back to a file.
	return statusbarLine(next, marker, a.width, a.shine.offsetAt(time.Now()))
}

// bytesPair can reach 1024.0 at a unit boundary because it rounds to one
// decimal place. Reserving that widest representation keeps the progress bar
// in the same column when the queue advances to a file with a different size.
const statusbarBytesW = len("1024.0 / 1024.0 MiB")

// statusbarLine renders the strip above the footer for a file transfer:
// marker, name, progress bar, percent, bytes, estimate, and rate. It stays one
// line: the file being fetched is what the strip is for, and the queue behind
// it is what the library pane already shows. The marker arrives
// already styled, and offset says how far along the bar the sweep's bands have
// travelled. Empty when there is no transfer to draw.
func statusbarLine(snap engine.Snapshot, marker string, width int, offset float64) string {
	if snap.ActiveID == 0 || snap.CurrentFile == "" {
		return ""
	}
	frac := 0.0
	if snap.FileSize > 0 {
		frac = min(1, max(0, float64(snap.FileDone)/float64(snap.FileSize)))
	}
	percent := percentText(frac)
	bytes := fmt.Sprintf("%*s", statusbarBytesW, bytesPair(snap.FileDone, snap.FileSize))
	// The estimate is projected from the smoothed rate, so it counts down
	// instead of jumping about with the speed the column beside it reports.
	eta := fmt.Sprintf("%*s", etaW, etaText(snap.FileSize-snap.FileDone, snap.AvgRate))
	// A zero rate keeps its (blank) column so the line doesn't reflow when the
	// transfer stalls or has just started. A paused queue uses that same
	// column for its state, keeping every field to its left in place.
	const rateW = len("1023.9 KiB/s")
	rate := fmt.Sprintf("%*s", rateW, humanRate(snap.Rate))
	rateStyled := rate
	if snap.Paused {
		rate = fmt.Sprintf("%*s", rateW, "PAUSED")
		rateStyled = styleWarn.Render(rate)
	} else if snap.Rate > 0 {
		rateStyled = rateStyle(snap.Rate).Render(rate)
	}

	// " ⠋ name  ███▌░░ 42%  12.4 / 30.0 MiB  ~2m14s  3.4 MiB/s ", with the ░
	// cells drawn as blocks in the track color. Every field after the bar has a
	// constant width so the bar never shifts as the numbers tick. In narrow
	// terminals they give way to the name in turn — byte counts first, then the
	// estimate, then the rate — and only then does the bar shrink.
	const minNameW = 12
	barW := 20
	stats := []struct {
		text  string // styled, so it is never measured
		width int
	}{
		{styleDim.Render(bytes), statusbarBytesW},
		{etaStyled(eta), etaW},
		{rateStyled, rateW},
	}
	drop := 0 // fields given up, from the widest end
	nameW := func() int {
		w := width - 3 - 2 - barW - 1 - len(percent) - 1
		for _, f := range stats[drop:] {
			w -= 2 + f.width
		}
		return w
	}
	for drop < len(stats) && nameW() < minNameW {
		drop++
	}
	if nameW() < minNameW {
		barW = max(6, barW-(minNameW-nameW()))
	}
	if nameW() < 1 {
		return ""
	}

	bar := shineProgressBar(barW, frac, offset, snap.Paused)
	left := " " + marker + " " + truncate(snap.CurrentFile, nameW())
	tail := bar + " " + styleTitle.Render(percent)
	for _, f := range stats[drop:] {
		tail += "  " + f.text
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
	case a.del != nil:
		return a.del.help()
	}
	return a.downloads.help()
}
