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
	// styleSelected marks the cursor row in the focused pane; styleSelBlur
	// the remembered cursor row in an unfocused pane.
	styleSelected = lipgloss.NewStyle().Background(colorPrimary).
			Foreground(lipgloss.Color("255")).Bold(true)
	styleSelBlur = lipgloss.NewStyle().Background(lipgloss.Color("238")).
			Foreground(lipgloss.Color("255"))

	styleDim         = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleError       = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleOK          = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleWarn        = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stylePartial     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleAccent      = lipgloss.NewStyle().Foreground(colorPrimary)
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
