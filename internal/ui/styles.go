package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	colorPrimary     = lipgloss.Color("#A81006")
	colorPrimaryText = lipgloss.Color("#FF3B30")
	colorOrange      = lipgloss.Color("214")
)

var (
	// styleCursor draws the gutter bar on the cursor row of the focused pane,
	// and styleRowTint the faint band behind that row. Between them they mark
	// the cursor without setting a foreground: a bright full-row highlight
	// would flatten the very row being read.
	styleCursor  = lipgloss.NewStyle().Foreground(colorPrimaryText)
	styleRowTint = lipgloss.NewStyle().Background(lipgloss.Color("236"))

	styleDim         = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleError       = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleOK          = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleWarn        = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stylePartial     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleAccent      = lipgloss.NewStyle().Foreground(colorPrimary)
	styleNotice      = lipgloss.NewStyle().Foreground(colorOrange)
	stylePrimaryText = lipgloss.NewStyle().Foreground(colorPrimaryText)
	styleProgress    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleDecode      = lipgloss.NewStyle().Foreground(colorOrange)
	styleSpinner     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleHelpKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Bold(true)
	styleTitle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	styleLogo        = stylePrimaryText
	styleLogoBold    = styleLogo.Bold(true)

	styleModal = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).Padding(1, 2)
)

// cursorBar is the two-cell left gutter that marks the cursor row: an accent
// bar in the focused pane, dim for the remembered row of an unfocused one,
// blank elsewhere. Its width is constant so rows never shift.
func cursorBar(selected, focused bool) string {
	if !selected {
		return "  "
	}
	if focused {
		return styleCursor.Render("▌") + " "
	}
	return styleDim.Render("▌") + " "
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

// progressBar renders a fixed-width bar for frac in [0,1].
func progressBar(width int, frac float64) string {
	if width < 2 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * float64(width))
	return styleProgress.Render(strings.Repeat("█", filled)) +
		styleDim.Render(strings.Repeat("░", width-filled))
}

// fileProgressBar uses centered line glyphs so bars on adjacent file rows
// remain visually separate.
func fileProgressBar(width int, frac float64) string {
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
			keys = append(keys, styleHelpKey.Render(key))
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
