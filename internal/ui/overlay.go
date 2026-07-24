package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// overlayCenter composites fg centered over bg, treating bg as a width×height
// cell region (padded or cropped as needed). Both strings may contain ANSI
// styling; fg cells fully replace the bg cells they cover.
func overlayCenter(bg, fg string, width, height int) string {
	bgLines := strings.Split(bg, "\n")
	if len(bgLines) > height {
		bgLines = bgLines[:height]
	}
	for len(bgLines) < height {
		bgLines = append(bgLines, "")
	}

	fgLines := strings.Split(fg, "\n")
	if len(fgLines) > height {
		fgLines = fgLines[:height]
	}
	fgW := 0
	for _, l := range fgLines {
		fgW = max(fgW, ansi.StringWidth(l))
	}
	fgW = min(fgW, width)
	x := (width - fgW) / 2
	y := (height - len(fgLines)) / 2

	out := make([]string, len(bgLines))
	for i, line := range bgLines {
		j := i - y
		if j < 0 || j >= len(fgLines) {
			out[i] = line
			continue
		}
		if pad := width - ansi.StringWidth(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		fgLine := ansi.Truncate(fgLines[j], fgW, "")
		if pad := fgW - ansi.StringWidth(fgLine); pad > 0 {
			fgLine += strings.Repeat(" ", pad)
		}
		// Resets guard against styles bleeding across the seams; TruncateLeft
		// replays the covered region's escapes so the right side keeps its
		// own styling.
		out[i] = ansi.Truncate(line, x, "") + "\x1b[0m" + fgLine + "\x1b[0m" +
			ansi.TruncateLeft(line, x+fgW, "")
	}
	return strings.Join(out, "\n")
}
