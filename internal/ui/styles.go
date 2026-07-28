package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	colorPrimary = lipgloss.Color("#FF3B30")
	colorOrange  = lipgloss.Color("214")
	// colorTrack is the unfilled remainder of any bar drawn as a solid
	// block — the sparkline's track and the statusbar's progress bar.
	colorTrack = lipgloss.Color("237")
)

var (
	// styleCursor draws the gutter bar on the cursor row of the focused pane,
	// and styleRowTint the faint band behind that row. Between them they mark
	// the cursor without setting a foreground: a bright full-row highlight
	// would flatten the very row being read.
	styleCursor  = lipgloss.NewStyle().Foreground(colorPrimary)
	styleRowTint = lipgloss.NewStyle().Background(lipgloss.Color("237"))

	styleDim         = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleError       = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleOK          = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleWarn        = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stylePartial     = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	styleAccent      = lipgloss.NewStyle().Foreground(colorPrimary)
	styleNotice      = lipgloss.NewStyle().Foreground(colorOrange)
	stylePrimaryText = lipgloss.NewStyle().Foreground(colorPrimary)
	styleProgress    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleDecode      = lipgloss.NewStyle().Foreground(colorOrange)
	styleSpinner     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleHelpKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true)
	styleLogoMark    = lipgloss.NewStyle().Foreground(lipgloss.Color("239"))
	styleTitle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	// Leave the foreground unset so the active file's percentage follows the
	// terminal's own text color.
	styleActivePercent = lipgloss.NewStyle().Bold(true)

	// styleSparkTrack colors the sparkline's track. The track is a background,
	// not a glyph, so the part of a cell a bar doesn't reach stays empty and
	// the bars read as bars instead of blending into a texture.
	styleSparkTrack = lipgloss.NewStyle().Background(colorTrack)

	// styleProgressTrack draws the unfilled cells of the statusbar's progress
	// bar. It paints a full block in the track color rather than a shade glyph
	// so the track is a flat surface the fill can end part-way across: a
	// partial cell covers its remainder with the same color as a background,
	// which a dithered ░ neighbour could never match.
	styleProgressTrack = lipgloss.NewStyle().Foreground(colorTrack)

	styleModal = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).
			BorderForeground(colorPrimary).Padding(1, 2)
)

// modalTitleFrame is what a titled top border spends on everything that isn't
// the title: two corners, the dash before the label, a space either side of
// it, and at least one dash after.
const modalTitleFrame = 6

// renderModal frames body in styleModal with title set into the top border
// rather than written on the first body row:
//
//	┌─ Rename download ──────────┐
//
// The frame never comes out narrower than the title needs up there, so a
// dialog with a short body still shows its whole heading.
func renderModal(title, body string) string {
	// Everything modalTitleFrame accounts for except one dash is already paid
	// for by the frame's own border and padding, so only the remainder has to
	// come out of the content width.
	need := lipgloss.Width(title) + modalTitleFrame - styleModal.GetHorizontalFrameSize()
	inner := max(lipgloss.Width(body), need)
	box := styleModal.Width(inner + styleModal.GetHorizontalPadding()).Render(body)

	lines := strings.Split(box, "\n")
	lines[0] = modalTopBorder(title, lipgloss.Width(lines[0]))
	return strings.Join(lines, "\n")
}

// modalTopBorder draws a width-cell top border carrying title. The border
// keeps the modal's color and the title the heading's, so the label reads as
// text sitting in the rule rather than as part of it.
func modalTopBorder(title string, width int) string {
	border := lipgloss.NormalBorder()
	edge := lipgloss.NewStyle().Foreground(colorPrimary)
	dashes := func(n int) string { return strings.Repeat(border.Top, max(0, n)) }
	if title == "" || width < modalTitleFrame {
		return edge.Render(border.TopLeft + dashes(width-2) + border.TopRight)
	}
	title = truncate(title, width-modalTitleFrame)
	fill := width - modalTitleFrame - lipgloss.Width(title) + 1
	return edge.Render(border.TopLeft+border.Top) +
		" " + styleTitle.Render(title) + " " +
		edge.Render(dashes(fill)+border.TopRight)
}

// quotaLimit is roughly what MEGA's free tier allows over the 6h window.
const quotaLimit = 5 << 30

// quotaStyle grades the 6h transfer total from green to red in four steps:
// the bands are quarters of quotaLimit, and everything at or past it renders
// in the same red as an error.
func quotaStyle(bytes int64) lipgloss.Style {
	switch {
	case bytes < quotaLimit/4:
		return styleOK
	case bytes < quotaLimit/2:
		return stylePartial
	case bytes < quotaLimit*3/4:
		return styleWarn
	default:
		return styleError
	}
}

// rateBands are the download speeds the rate ramp steps at, fastest first,
// paired with the style a rate at or above that speed renders in. The steps
// are a factor of four apart rather than evenly spaced: transfer speed is read
// by order of magnitude, so linear bands would spend three of the four colors
// on the fast end and call everything ordinary red.
var rateBands = []struct {
	bps   float64
	style lipgloss.Style
}{
	{4 << 20, styleOK},      // a link running at full tilt
	{1 << 20, stylePartial}, // healthy
	{256 << 10, styleWarn},  // slow, but still moving
}

// rateStyle grades a transfer's speed green to red, the same ramp quotaStyle
// uses in the other direction: here a bigger number is the good one, so the
// bands are matched from the top down and anything below the last one is red.
func rateStyle(bps float64) lipgloss.Style {
	for _, band := range rateBands {
		if bps >= band.bps {
			return band.style
		}
	}
	return styleError
}

// sparkLevels are the bar heights a sparkline draws with, shortest first. All
// of them stand for a transfer: an empty bucket draws no bar at all, so the
// shortest one is free to mean "barely anything" rather than "nothing".
const sparkLevels = "▁▂▃▄▅▆▇█"

// sparkline renders one cell per bucket against a fixed ceiling, so a bar's
// height always stands for the same number of bytes and the row can be read
// without a scale printed beside it. Buckets at or past full fill the cell
// rather than pulling the rest of the row down with them.
//
// The whole row sits on the track background, so an idle cell is a blank one
// and a bar's cell shows the track above it.
func sparkline(buckets []int64, full int64, style lipgloss.Style) string {
	levels := []rune(sparkLevels)
	bar := style.Background(colorTrack)
	var b strings.Builder
	for _, v := range buckets {
		if v <= 0 || full <= 0 {
			b.WriteString(styleSparkTrack.Render(" "))
			continue
		}
		// Positive buckets start at the shortest glyph. The remaining seven
		// steps are spread evenly up to full, so the tallest glyph begins
		// exactly at the ceiling rather than one interval before it.
		level := 1 + int(float64(v)/float64(full)*float64(len(levels)-1))
		level = min(level, len(levels))
		b.WriteString(bar.Render(string(levels[level-1])))
	}
	return b.String()
}

// cursorLevels are the widths the cursor bar is drawn at, thinnest first: the
// eighth blocks the progress bar fills its leading cell with, plus the full
// cell. A pane's bar rests at one end or the other and travels through the
// rest when focus moves.
var cursorLevels = [8]string{"▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"}

const (
	cursorThin = 1
	cursorFull = len(cursorLevels)
)

// cursorBar is the two-cell left gutter that marks the cursor row: an accent
// bar in the focused pane, dim for the remembered row of an unfocused one,
// blank elsewhere. level picks its width. The gutter's own width is constant
// whatever the bar is doing, so rows never shift.
func cursorBar(selected, focused bool, level int) string {
	if !selected {
		return "  "
	}
	level = min(cursorFull, max(cursorThin, level))
	style := styleDim
	if focused {
		style = styleCursor
	}
	return style.Render(cursorLevels[level-1]) + " "
}

// tintRow lays styleRowTint's background under a line that is already styled,
// padded out to width. Each nested style ends in a reset that would drop the
// background too, so the background is re-armed after every one of them —
// that is what lets the cursor row keep its own foreground colors instead of
// being rendered as flat text the way a wrapping style would force.
func tintRow(line string, width int) string {
	if pad := width - lipgloss.Width(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	// Ask the active profile for the sequences rather than writing them out,
	// so the row stays plain when color is off (tests, piped output).
	const probe = "\x00"
	open, reset, ok := strings.Cut(styleRowTint.Render(probe), probe)
	if !ok || open == "" {
		return line
	}
	return armBackground(line, open, reset)
}

// armBackground wraps line in open/reset and re-opens after every reset the
// line already carries, so nested foreground styles don't punch holes in the
// background.
func armBackground(line, open, reset string) string {
	line = strings.ReplaceAll(line, reset, reset+open)
	line = strings.TrimSuffix(line, open) // nothing follows the last reset
	if !strings.HasSuffix(line, reset) {
		line += reset
	}
	return open + line
}

func humanBytes(n int64) string {
	return formatBytes(n, 1)
}

// fileBytes keeps the file-list size column compact: sub-GiB sizes are
// rounded to whole units, while larger files retain useful GiB precision.
func fileBytes(n int64) string {
	const gib = int64(1024 * 1024 * 1024)
	precision := 0
	if n > gib {
		precision = 1
	}
	return formatBytes(n, precision)
}

func formatBytes(n int64, precision int) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	if precision == 0 {
		value = math.Round(value)
	}
	return fmt.Sprintf("%.*f %ciB", precision, value, "KMGTPE"[exp])
}

// bytesPair renders "done / total" with done in the total's unit and padded
// to its width, so the string keeps a constant width while done grows and
// content to its left doesn't shift.
func bytesPair(done, total int64) string {
	if done < 0 || total < 0 {
		return "?"
	}
	done = min(done, total)
	const unit = 1024
	if total < unit {
		t := fmt.Sprintf("%d", total)
		return fmt.Sprintf("%*d / %s B", len(t), done, t)
	}
	div, exp := int64(unit), 0
	for v := total / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	t := fmt.Sprintf("%.1f", float64(total)/float64(div))
	return fmt.Sprintf("%*.1f / %s %ciB", len(t), float64(done)/float64(div), t, "KMGTPE"[exp])
}

func humanRate(bps float64) string {
	if bps <= 0 {
		return ""
	}
	return humanBytes(int64(bps)) + "/s"
}

// percentText renders frac as a fixed-width percentage. It rounds down, the
// way the bars fill cells, so a bar with an empty cell left never sits beside
// "100%": the pair only reads complete when the transfer actually is.
func percentText(frac float64) string {
	frac = min(1, max(0, frac))
	return fmt.Sprintf("%3d%%", int(frac*100))
}

// eighthBlocks covers 1/8 through 7/8 of a cell. Unicode draws these flush
// with the cell's left edge, so a partial cell extends the bar rightward the
// way a full one does instead of floating in the middle of its column.
var eighthBlocks = [7]string{"▏", "▎", "▍", "▌", "▋", "▊", "▉"}

// progressBar renders a fixed-width bar for frac in [0,1]. A paused transfer
// uses the same orange as its pause marker. The leading cell is drawn at
// eighth-of-a-cell resolution so a transfer that has just started reads as
// moving rather than sitting empty until it has earned a whole cell.
//
// Fill and track are the same glyph in different colors, so the bar is one
// unbroken strip: the partial cell only has to carry the track as its
// background for the boundary between them to land mid-cell.
func progressBar(width int, frac float64, paused bool) string {
	if width < 2 {
		return ""
	}
	frac = min(1, max(0, frac))
	eighths := int(frac * float64(width) * 8)
	filled, rem := eighths/8, eighths%8
	filledStyle := styleProgress
	if paused {
		filledStyle = styleWarn
	}
	bar := filledStyle.Render(strings.Repeat("█", filled))
	if rem > 0 {
		bar += filledStyle.Background(colorTrack).Render(eighthBlocks[rem-1])
		filled++
	}
	return bar + styleProgressTrack.Render(strings.Repeat("█", width-filled))
}

// fileProgressBar uses centered line glyphs so bars on adjacent file rows
// remain visually separate. The active row gets a heavier filled segment,
// colored like the pause marker while its queue is held.
func fileProgressBar(width int, frac float64, active, paused bool) string {
	if width < 2 {
		return ""
	}
	frac = min(1, max(0, frac))
	filled := int(frac * float64(width))
	filledGlyph := "─"
	if active {
		filledGlyph = "━"
	}
	filledStyle := styleProgress
	if paused {
		filledStyle = styleWarn
	}
	return filledStyle.Render(strings.Repeat(filledGlyph, filled)) +
		styleDim.Render(strings.Repeat("─", width-filled))
}

// fileHeaderProgressBar gives the file-listing header a heavier filled line
// while keeping the unfilled portion visually light.
func fileHeaderProgressBar(width int, frac float64) string {
	if width < 2 {
		return ""
	}
	frac = min(1, max(0, frac))
	filled := int(frac * float64(width))
	return styleProgress.Render(strings.Repeat("━", filled)) +
		styleDim.Render(strings.Repeat("─", width-filled))
}

type shortcut struct {
	keys  []string
	label string
}

func renderShortcuts(shortcuts ...shortcut) string {
	rendered := make([]string, 0, len(shortcuts))
	for _, shortcut := range shortcuts {
		keys := make([]string, 0, len(shortcut.keys))
		for _, key := range shortcut.keys {
			parts := strings.Split(key, "/")
			for i := range parts {
				parts[i] = styleHelpKey.Render(parts[i])
			}
			keys = append(keys, strings.Join(parts, styleDim.Render("/")))
		}
		rendered = append(rendered,
			strings.Join(keys, styleDim.Render(" or "))+" "+styleDim.Render(shortcut.label))
	}
	return strings.Join(rendered, styleDim.Render("  "))
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w <= 1 {
		return string(r[:w])
	}
	return string(r[:w-1]) + "…"
}

// wrap breaks s onto lines of at most w cells. Words longer than the line are
// broken mid-word rather than allowed to run past it, which is what keeps a
// URL or a bare error string from widening the modal it sits in.
func wrap(s string, w int) string {
	if w <= 0 {
		return s
	}
	return ansi.Wrap(s, w, "")
}

// truncateMiddle keeps both ends of long strings (URLs).
func truncateMiddle(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w <= 3 {
		return truncate(s, w)
	}
	half := (w - 1) / 2
	return string(r[:half]) + "…" + string(r[len(r)-(w-1-half):])
}
