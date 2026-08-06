package ui

import (
	"net/url"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	// modalWidth is the content width a dialog prefers — room for a mega link
	// beside its prompt — and the cap on any terminal wider than that, so a
	// dialog stays a dialog instead of stretching across the screen.
	modalWidth = 73
	// modalMinWidth is the point below which shrinking stops helping: a
	// terminal narrower than that gets a cropped dialog either way.
	modalMinWidth = 20
)

// modalContentWidth is how many cells a dialog's body may use, at most limit:
// the terminal it opens on less the modal's border and padding, so a dialog
// never opens wider than the screen. Dialogs size themselves once, when they
// open; a zero terminal width means nothing has reported one yet, and the
// preferred width is the best guess.
func modalContentWidth(termWidth, limit int) int {
	if termWidth <= 0 {
		return limit
	}
	return max(modalMinWidth, min(limit, termWidth-styleModal.GetHorizontalFrameSize()))
}

// promptWidth is how many cells a text input spends on its prompt, which is
// what a dialog has to hand back to it when sizing the input to a line.
func promptWidth(in textinput.Model) int {
	return lipgloss.Width(in.PromptStyle.Render(in.Prompt))
}

// inputLineView pads a text input to its filled-in width: bubbles renders the
// placeholder Width cells wide but a value prompt+Width+1, so the dialog would
// otherwise widen the moment the input gets content.
func inputLineView(in textinput.Model) string {
	view := in.View()
	w := promptWidth(in) + in.Width + 1
	if pad := w - lipgloss.Width(view); pad > 0 {
		view += strings.Repeat(" ", pad)
	}
	return view
}

// linkedInputView is inputLineView with the value marked as an OSC 8
// hyperlink whenever the value is one, so a link the app itself can't act on —
// a shortener standing in front of the mega link, say — is still one click
// away in the terminal. The escape occupies no cells, so a terminal that
// doesn't understand it draws exactly the line it drew before.
func linkedInputView(in textinput.Model) string {
	view := inputLineView(in)
	uri, ok := clickableURL(in.Value())
	if !ok {
		return view
	}
	prompt := in.PromptStyle.Render(in.Prompt)
	value, found := strings.CutPrefix(view, prompt)
	if !found {
		return view
	}
	// The input pads the rest of its field with spaces, and colors them along
	// with the value it is padding — so the link ends at the last cell that
	// isn't blank rather than wherever those spaces stop carrying styling.
	cells := lipgloss.Width(strings.TrimRight(ansi.Strip(value), " "))
	return prompt + hyperlink(ansi.Truncate(value, cells, ""), uri) +
		ansi.TruncateLeft(value, cells, "")
}

// hyperlink marks text as a link to uri for terminals that support OSC 8,
// which usually open it on ctrl- or cmd-click.
func hyperlink(text, uri string) string {
	return ansi.SetHyperlink(uri) + text + ansi.ResetHyperlink()
}

// clickableURL reports the http(s) URL s holds, if that is all it holds. The
// URI goes to the terminal inside an escape sequence, so text carrying control
// characters of its own is refused rather than passed along.
func clickableURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	return s, true
}

// rect is a region of the terminal grid.
type rect struct{ x, y, w, h int }

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
