package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// rect is a region of the terminal grid.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// overlayLayout crops fg to height and reports its lines together with the
// region overlayCenter would place them in.
func overlayLayout(fg string, width, height int) ([]string, rect) {
	lines := strings.Split(fg, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	w := 0
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l))
	}
	w = min(w, width)
	return lines, rect{x: (width - w) / 2, y: (height - len(lines)) / 2, w: w, h: len(lines)}
}

// overlayRect reports where overlayCenter places fg, so clicks can be mapped
// back onto whatever the overlay drew.
func overlayRect(fg string, width, height int) rect {
	_, r := overlayLayout(fg, width, height)
	return r
}

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

	fgLines, area := overlayLayout(fg, width, height)
	fgW, x, y := area.w, area.x, area.y

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
