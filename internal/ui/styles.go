package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"
)

// The app takes its colors from the terminal's own 16-color palette rather than
// naming shades out of the 256-color cube: green is then whatever green the
// user's colorscheme already draws with, and the markers sit beside their shell
// and their editor as part of the same family. A hand-picked palette can only
// ever agree with the themes it was picked against; every other one it fights.
//
// Only the base eight slots are used. The bright eight are nominally the same
// hues lifted, but a scheme is free to spend them however it likes — Solarized
// hands four of them to greys — so slot 10 is not dependably green at all.
const (
	colorRed    = lipgloss.Color("1")
	colorGreen  = lipgloss.Color("2")
	colorYellow = lipgloss.Color("3")
	colorCyan   = lipgloss.Color("6")
	// Slot 8 is the one muted color the schemes agree on: the grey a dark theme
	// writes its comments in, and still dark enough on a light theme to read as
	// text. Slot 0 is no substitute — that is the terminal's black, which on a
	// light background is body text rather than a quieter version of it.
	colorGrey = lipgloss.Color("8")

	// colorPrimary is the accent: the cursor bar, the modal frames, the filter's
	// marks. It is the terminal's red rather than MEGA's own #FF3B30, since an
	// app that borrows the palette's most emphatic color has no business also
	// bringing one of its own.
	colorPrimary = colorRed
)

// colorTrack is the unfilled remainder of any bar drawn as a solid block — the
// sparkline's track and the statusbar's progress bar. Like the cursor tint it
// is a surface sitting just off the terminal's own background, so DetectTheme
// derives it the same way and this pair is only the fallback.
//
// The two surfaces are the one thing not taken from the 16 colors: a surface has
// to sit *just* off the background, and the palette's nearest offer is a grey
// loud enough to read as a block of color laid over the screen.
var colorTrack lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "252", Dark: "237"}

var (
	// styleCursor draws the gutter bar on the cursor row of the focused pane,
	// and styleRowTint the faint band behind that row. Between them they mark
	// the cursor without setting a foreground: a bright full-row highlight
	// would flatten the very row being read.
	//
	// The tint's pair here is only the fallback. DetectTheme replaces it with a
	// shade derived from the terminal's own background whenever the terminal is
	// willing to say what that background is.
	styleCursor  = lipgloss.NewStyle().Foreground(colorPrimary)
	styleRowTint = lipgloss.NewStyle().Background(
		lipgloss.AdaptiveColor{Light: "254", Dark: "237"})

	styleDim    = lipgloss.NewStyle().Foreground(colorGrey)
	styleError  = lipgloss.NewStyle().Foreground(colorRed)
	styleOK     = lipgloss.NewStyle().Foreground(colorGreen)
	styleWarn   = lipgloss.NewStyle().Foreground(colorYellow)
	styleQueued = lipgloss.NewStyle().Foreground(colorCyan)
	// stylePartial shares the warning yellow: a file with bytes on disk and a
	// queue that has been held are both unfinished business, and the markers
	// that carry the two colors are different glyphs anyway.
	stylePartial = lipgloss.NewStyle().Foreground(colorYellow)
	styleAccent  = lipgloss.NewStyle().Foreground(colorPrimary)
	// styleMatch marks the part of a name the filter query matched. It only
	// recolors the letters, leaving the row's own background — the cursor tint
	// included — to show through. The accent is the prompt's own, so the query
	// and its marks are visibly the same thing.
	styleMatch       = lipgloss.NewStyle().Foreground(colorPrimary)
	styleNotice      = lipgloss.NewStyle().Foreground(colorYellow)
	stylePrimaryText = lipgloss.NewStyle().Foreground(colorPrimary)
	styleProgress    = lipgloss.NewStyle().Foreground(colorGreen)
	styleDecode      = lipgloss.NewStyle().Foreground(colorYellow)
	styleSpinner     = lipgloss.NewStyle().Foreground(colorGreen)
	// A key is told from the label beside it by weight rather than by color:
	// both are the palette's grey, so the footer stays a quiet strip and the
	// bold is what picks the key out of it.
	styleHelpKey   = lipgloss.NewStyle().Foreground(colorGrey).Bold(true)
	styleDirectory = lipgloss.NewStyle().Foreground(colorGrey)
	// Headings and the active file's percentage leave the foreground unset, so
	// they follow the terminal's own text color and read as its brightest text.
	// A fixed near-white would be text on a dark theme and nothing at all on a
	// light one.
	styleTitle         = lipgloss.NewStyle().Bold(true)
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

	styleModal = lipgloss.NewStyle().Border(modalBorder).
			BorderForeground(colorPrimary).Padding(1, 2)
)

// modalBorder frames every dialog. modalTopBorder redraws the top edge by hand
// to set the title into it, so the border lives here rather than inline in the
// style: both drawings have to use the same glyphs or the corners won't meet.
var modalBorder = lipgloss.DoubleBorder()

// modalTitleFrame is what a titled top border spends on everything that isn't
// the title: two corners, the dash before the label, a space either side of
// it, and at least one dash after.
const modalTitleFrame = 6

// renderModal frames body in styleModal with title set into the top border
// rather than written on the first body row:
//
//	╔═ Rename download ══════════╗
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
	border := modalBorder
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

// quotaStyle grades the 6h transfer total green, yellow, red as it fills: half
// the allowance is where it stops being comfortable and three quarters is where
// it starts reading as the error it is about to become.
//
// The ramp is three steps rather than four because green, yellow and red is the
// whole of what the palette offers between fine and not. The step it gave up sat
// between yellow and orange, which is the pair a reader was least likely to have
// told apart anyway.
func quotaStyle(bytes int64) lipgloss.Style {
	switch {
	case bytes < quotaLimit/2:
		return styleOK
	case bytes < quotaLimit*3/4:
		return styleWarn
	default:
		return styleError
	}
}

// rateBands are the download speeds the rate ramp steps at, fastest first,
// paired with the style a rate at or above that speed renders in. The steps
// are a factor of four apart rather than evenly spaced: transfer speed is read
// by order of magnitude, so linear bands would spend both of them on the fast
// end and call everything ordinary red.
var rateBands = []struct {
	bps   float64
	style lipgloss.Style
}{
	{1 << 20, styleOK},     // a link doing what it should
	{256 << 10, styleWarn}, // slow, but still moving
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

// The steps the app's two surfaces take off the terminal's own background, in
// perceived lightness. A dark background takes the bigger step either way: the
// same difference reads as less against near-black than it does against a
// bright surface, where the eye picks up a much smaller shift.
//
// A bar's track steps further than the cursor band does, because the two are
// asked for different things. The band sits under a row being read and only has
// to be felt; the track is a surface with glyphs standing on it, and a track
// too close to the background stops reading as one — which is the whole reason
// the bars are drawn against it.
const (
	tintDeltaDark   = 0.10
	tintDeltaLight  = 0.06
	trackDeltaDark  = 0.16
	trackDeltaLight = 0.10
)

// DetectTheme asks the terminal what its background color is and derives the
// app's surfaces from it — the cursor band and the bar track — so both are
// shades of whatever colorscheme is in use rather than fixed grays that only
// ever suit dark themes.
//
// The answer comes back over the tty, so this has to run before Bubble Tea
// takes the input over. It settles lipgloss's own light/dark question from the
// same answer, which would otherwise ask the terminal again from inside the
// render loop, with Bubble Tea already holding the input.
//
// A terminal only answers if it wants to: multiplexers refuse outright, and a
// pipe has nobody to ask. Anything short of a real color leaves the fallback
// pairs in place rather than shading from a background we guessed at — a
// surface derived from an assumed black would come out darker than the real
// background and read as a hole punched in the screen.
func DetectTheme() {
	bg := lipgloss.DefaultRenderer().Output().BackgroundColor()
	_, _, l := termenv.ConvertToRGB(bg).Hsl()
	lipgloss.SetHasDarkBackground(l < 0.5)

	rgb, ok := bg.(termenv.RGBColor)
	if !ok {
		return
	}
	// Below ANSI256 the nearest available color to a small nudge off the
	// background is the background itself, and a surface nobody can see is
	// worse than a fixed one that is a little off-theme.
	if p := lipgloss.ColorProfile(); p != termenv.TrueColor && p != termenv.ANSI256 {
		return
	}
	hex := string(rgb)
	styleRowTint = styleRowTint.Background(
		lipgloss.Color(shadeOff(hex, tintDeltaDark, tintDeltaLight)))

	colorTrack = lipgloss.Color(shadeOff(hex, trackDeltaDark, trackDeltaLight))
	// Both track styles took a copy of the fallback when the package loaded;
	// everywhere else reads colorTrack at render time and needs no help.
	styleSparkTrack = styleSparkTrack.Background(colorTrack)
	styleProgressTrack = styleProgressTrack.Foreground(colorTrack)
}

// shadeOff steps hex one shade off its own lightness: up by dark from a dark
// background, down by light from a light one.
//
// The step is taken in CIELUV, moving lightness while hue and chroma stay put,
// so the shade is the background's own color at a different brightness. Doing
// this in plain HSL instead would hold saturation as a fraction of what the new
// lightness allows, which quietly repaints a cream background yellow on the way
// down. The two ends of the range need clamping back into gamut: not every
// lightness exists at every chroma.
func shadeOff(hex string, dark, light float64) string {
	c, err := colorful.Hex(hex)
	if err != nil {
		return hex
	}
	l, chroma, hue := c.LuvLCh()
	if l < 0.5 {
		l = min(1, l+dark)
	} else {
		l = max(0, l-light)
	}
	return colorful.LuvLCh(l, chroma, hue).Clamped().Hex()
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

// etaW is the widest string etaText returns, so the estimate can hold a column
// of its own without the fields beside it shifting as it counts down.
const etaW = len("~99d23h")

// etaText projects how much longer remaining bytes take at rate. The tilde says
// it is an estimate; it drops off ">99d", which is one already. Empty when there
// is nothing to project — nothing left to fetch, or nothing moving — so a held or
// stalled transfer quotes no finish time rather than one that has stopped being
// true.
//
// Each unit is dropped once the one above it carries the answer: seconds stop
// being quoted past ten minutes, minutes past a day. The estimate is only ever
// as good as the last half minute of transfer, so a digit that would tick every
// second is noise rather than precision.
func etaText(remaining int64, rate float64) string {
	if remaining <= 0 || rate <= 0 {
		return ""
	}
	secs := float64(remaining) / rate
	if secs >= 100*24*3600 {
		return ">99d"
	}
	n := int64(secs + 0.5)
	switch {
	case n < 60:
		return fmt.Sprintf("~%ds", n)
	case n < 10*60:
		return fmt.Sprintf("~%dm%02ds", n/60, n%60)
	case n < 3600:
		return fmt.Sprintf("~%dm", n/60)
	case n < 24*3600:
		return fmt.Sprintf("~%dh%02dm", n/3600, n/60%60)
	}
	return fmt.Sprintf("~%dd%02dh", n/(24*3600), n/3600%24)
}

// etaStyled dims the tilde and leaves the duration itself in the terminal's own
// text color: how long the file has left is worth reading, while the mark that
// says it is an estimate is only a qualifier on it.
func etaStyled(text string) string {
	before, after, found := strings.Cut(text, "~")
	if !found {
		return text
	}
	return before + styleDim.Render("~") + after
}

// countdownText renders a wait the app is timing rather than projecting, so
// unlike etaText it carries no tilde and keeps its seconds all the way up:
// "14s", "3m 42s", "1h 09m". Part-seconds round up, the way a countdown has
// to: the last second of the wait reads "1s", and "0s" means the wait is over
// rather than nearly.
func countdownText(d time.Duration) string {
	n := int64(math.Ceil(d.Seconds()))
	switch {
	case n <= 0:
		return "0s"
	case n < 60:
		return fmt.Sprintf("%ds", n)
	case n < 3600:
		return fmt.Sprintf("%dm %02ds", n/60, n%60)
	}
	return fmt.Sprintf("%dh %02dm", n/3600, n/60%60)
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

// barCells splits frac over width cells: the whole cells the fill covers, plus
// the eighths of the cell it ends part-way across. Every drawing of the
// statusbar's bar goes through this, so the plain bar and the sweeping one put
// their boundary in exactly the same place.
func barCells(width int, frac float64) (filled, rem int) {
	frac = min(1, max(0, frac))
	eighths := int(frac * float64(width) * 8)
	return eighths / 8, eighths % 8
}

// progressBar renders a fixed-width bar for frac in [0,1], green while the
// queue runs and yellow while it is held — the same yellow the pause marker and
// the file rows use, so the whole screen agrees at a glance. The leading cell is
// drawn at eighth-of-a-cell resolution so a transfer that has just started
// reads as moving rather than sitting empty until it has earned a whole cell.
//
// Fill and track are the same glyph in different colors, so the bar is one
// unbroken strip: the partial cell only has to carry the track as its
// background for the boundary between them to land mid-cell.
func progressBar(width int, frac float64, paused bool) string {
	if width < 2 {
		return ""
	}
	fill := styleProgress
	if paused {
		fill = styleWarn
	}
	filled, rem := barCells(width, frac)
	bar := fill.Render(strings.Repeat("█", filled))
	if rem > 0 {
		bar += fill.Background(colorTrack).Render(eighthBlocks[rem-1])
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
