package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"megadl/internal/engine"
)

// A transfer's numbers can sit still for seconds at a time — a rate that has
// stalled, a size that keeps rounding to the same figure — so the statusbar's
// bar says on its own that the download is alive: bands of light sweep leftward
// along its filled cells, one after another, for as long as bytes are moving.
// They stop the moment the bytes do, which is what keeps the sweep a reading of
// the transfer rather than decoration. What stops is the travelling: the bands
// themselves stay, standing where the light left them, so a bar that is held or
// between files reads as stopped rather than as unlit — and starts again from
// there rather than from somewhere new.
const (
	// shineSpacing is the gap from one band to the next, in cells. It is kept
	// under the statusbar bar's own width so more than one band is on the bar
	// at once and the sweep reads as repeating rather than as a lone pass.
	shineSpacing = 20.0
	// shineHalfWidth is how far a band's light reaches either side of its
	// center. The falloff spends all of it, so consecutive bands are parted by
	// a stretch of the bar's flat green instead of running together.
	shineHalfWidth = 8.5
	// shinePeriod is what a band takes to travel shineSpacing cells, and so
	// also the time between one arriving at a given cell and the next.
	shinePeriod = 1500 * time.Millisecond
	// shineFrame paces the repaints. A band moves under half a cell per frame
	// at this rate, so what the eye follows is its color ramp drifting rather
	// than the steps between frames.
	shineFrame = 50 * time.Millisecond
)

// The sweep is the one place the UI works in RGB instead of terminal colors: a
// gradient is the steps between two colors, and the 256-color palette holds
// almost nothing between the bar's green and a bright one — walking the few
// entries it does have is exactly the banding the ramp exists to avoid.
//
// A palette's base is the color of an unlit cell, matched to the terminal color
// the plain bar fills with so a bar reads the same whether or not the sweep is
// drawing it, and its peak is what a band's center reaches.
type shinePalette struct{ base, peak [3]float64 }

// shineRunning is ANSI 42, styleProgress's green; shineHeld is ANSI 214, the
// orange styleWarn marks a held queue with everywhere else it appears.
var (
	shineRunning = shinePalette{base: [3]float64{0, 215, 135}, peak: [3]float64{125, 240, 194}}
	shineHeld    = shinePalette{base: [3]float64{255, 175, 0}, peak: [3]float64{255, 216, 140}}
)

// shinePaletteFor picks the colors a bar is drawn in: held queues wear the
// orange their marker and their file rows already do, so the strip says it is
// stopped in color as well as in a sweep that has come to rest.
func shinePaletteFor(paused bool) shinePalette {
	if paused {
		return shineHeld
	}
	return shineRunning
}

// sweepRuns reports whether the bands should be travelling: only while a file
// is actually being fetched. Light is not what reports the transfer — movement
// is — so a queue that is held, empty, or between files keeps its bands and
// stops them, rather than drawing a bar the light has left.
func sweepRuns(snap engine.Snapshot) bool {
	return snap.ActiveID != 0 && snap.CurrentFile != "" && !snap.Paused
}

// shineClock is where the bands stand and whether they are travelling. Phase
// carries across the stops: a pattern that took its offset from the wall clock
// would be somewhere else entirely by the time a held queue was resumed, and
// the bands would jump the moment the bar started moving again. Instead the
// clock stops where the light is and starts again from there, so the whole of a
// download reads as one pattern that pauses and picks up.
//
// The zero value is a pattern standing still at the head of its cycle, which is
// what a bar with no transfer behind it yet draws.
type shineClock struct {
	phase float64   // cells the pattern has travelled, wrapped to one band
	since time.Time // when it started travelling; zero while it stands still
}

// offsetAt is how far the pattern has travelled by now: measured from where it
// last started, so any repaint — one the frame loop asked for or not — draws
// the bands where the elapsed time puts them rather than a frame behind.
func (c shineClock) offsetAt(now time.Time) float64 {
	if c.since.IsZero() {
		return c.phase
	}
	travelled := float64(now.Sub(c.since)) / float64(shinePeriod) * shineSpacing
	return math.Mod(c.phase+travelled, shineSpacing)
}

// start sets the pattern travelling from where it stands. A clock already
// running keeps its anchor, so repeated calls cannot rewind the sweep.
func (c shineClock) start(now time.Time) shineClock {
	if !c.since.IsZero() {
		return c
	}
	return shineClock{phase: c.phase, since: now}
}

// stop leaves the bands standing exactly where this instant finds them, which
// is what makes the light continuous across a pause.
func (c shineClock) stop(now time.Time) shineClock {
	return shineClock{phase: c.offsetAt(now)}
}

// shineIntensity is how lit a point on the bar is, 0 in the flat stretch
// between bands and 1 at a band's center. The ramp is a raised cosine: it
// arrives at both ends with no corner, so what runs along the bar is a glow
// with no edge to it rather than a wedge.
func shineIntensity(x float64) float64 {
	d := math.Mod(x, shineSpacing)
	if d < 0 {
		d += shineSpacing
	}
	if d > shineSpacing/2 {
		d = shineSpacing - d // the band before this one is nearer
	}
	if d >= shineHalfWidth {
		return 0
	}
	return 0.5 * (1 + math.Cos(math.Pi*d/shineHalfWidth))
}

// shineColor is the color the bar takes at position x cells along it, the
// pattern having travelled offset.
func shineColor(x, offset float64, pal shinePalette) lipgloss.Color {
	t := shineIntensity(x + offset)
	mix := func(i int) int {
		return int(math.Round(pal.base[i] + (pal.peak[i]-pal.base[i])*t))
	}
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", mix(0), mix(1), mix(2)))
}

// shineProgressBar draws the statusbar's bar with the sweep running over its
// filled cells, in green while the transfer runs and in orange while its queue
// is held. Geometry is the plain bar's, down to the eighths of the leading
// cell, so the sweep starting or stopping never moves the boundary.
//
// Each cell is drawn as a left half block with its own foreground and
// background, which colors the two halves separately: the ramp is sampled twice
// per cell, and the terminal's cell grid stops being the limit on how smooth it
// can be. Where the profile has no color to give, that trick would leave visible
// half blocks, so the plain bar stands in.
func shineProgressBar(width int, frac float64, offset float64, paused bool) string {
	if width < 2 {
		return ""
	}
	if !colorEnabled() {
		return progressBar(width, frac, paused)
	}
	pal := shinePaletteFor(paused)
	filled, rem := barCells(width, frac)
	var b strings.Builder
	for i := range filled {
		x := float64(i)
		b.WriteString(lipgloss.NewStyle().
			Foreground(shineColor(x, offset, pal)).
			Background(shineColor(x+0.5, offset, pal)).
			Render("▌"))
	}
	if rem > 0 {
		// The partial cell owes its background to the track, so this one cell
		// is lit at whole-cell resolution.
		b.WriteString(lipgloss.NewStyle().
			Foreground(shineColor(float64(filled), offset, pal)).
			Background(colorTrack).
			Render(eighthBlocks[rem-1]))
		filled++
	}
	return b.String() + styleProgressTrack.Render(strings.Repeat("█", width-filled))
}

// colorEnabled reports whether the active profile emits color at all. Asking
// the profile through a style keeps the answer in step with whatever renderer
// lipgloss settled on, the way tintRow reads its background sequences.
func colorEnabled() bool {
	const probe = "\x00"
	return styleProgress.Render(probe) != probe
}

// shineTickMsg drives the repaints while the sweep is running.
type shineTickMsg struct{}

func shineTickCmd() tea.Cmd {
	return tea.Tick(shineFrame, func(time.Time) tea.Msg { return shineTickMsg{} })
}
