package ui

import (
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The cursor bar marks two things at once: which row the cursor is on, and
// whether its pane has focus. Moving between rows is its own visible event, so
// the bar simply appears on the new row. Moving between panes is not — nothing
// else on screen changes — so the bars carry it: the pane taking focus grows
// its bar out to a full block while the one losing focus draws its own back to
// a hairline. Both run at once, which is what makes it read as focus crossing
// the gutter rather than as two bars independently changing size.
const (
	// cursorAnimDuration is one bar's whole travel. Long enough to be seen as
	// movement, short enough that a key held down to cross back and forth
	// never falls behind the keystrokes.
	cursorAnimDuration = 140 * time.Millisecond
	// cursorAnimFrame paces the repaints at one per step of width, so the loop
	// never redraws a pane to show the same glyph twice.
	cursorAnimFrame = cursorAnimDuration / time.Duration(cursorFull)
)

// cursorAnim is one bar's travel toward the width its pane currently calls
// for. The zero value is a bar at rest, which is what a pane whose focus has
// not changed since startup wants: it sits at its resting width and asks for
// no repaints.
type cursorAnim struct {
	from  int       // width the bar set out from
	start time.Time // when it set out; zero when the bar has never moved
}

// level is the width to draw the bar at now: from at the outset, target once
// the travel is over, and a linear walk between them in between. Growing and
// thinning are the same walk in opposite directions.
func (a cursorAnim) level(target int, now time.Time) int {
	if !a.running(now) {
		return target
	}
	frac := float64(now.Sub(a.start)) / float64(cursorAnimDuration)
	return a.from + int(math.Round(frac*float64(target-a.from)))
}

// running reports whether the bar is still travelling and so still owes the
// screen a repaint.
func (a cursorAnim) running(now time.Time) bool {
	if a.start.IsZero() {
		return false
	}
	elapsed := now.Sub(a.start)
	return elapsed >= 0 && elapsed < cursorAnimDuration
}

// cursorGutter draws the cursor bar for a row of pane p, at whatever width
// that pane's bar has reached.
func (m *downloadsModel) cursorGutter(p paneID, selected bool) string {
	level := cursorFull
	if selected {
		level = m.cursorLevel(p, time.Now())
	}
	return cursorBar(selected, m.pane == p, level)
}

// cursorTickMsg drives the repaint loop while any bar is travelling.
type cursorTickMsg struct{}

func cursorTickCmd() tea.Cmd {
	return tea.Tick(cursorAnimFrame, func(time.Time) tea.Msg { return cursorTickMsg{} })
}

// cursorRestLevel is the width a pane's bar settles at: a full block for the
// focused pane, a hairline for the other.
func (m *downloadsModel) cursorRestLevel(p paneID) int {
	if m.pane == p {
		return cursorFull
	}
	return cursorThin
}

// cursorLevel is the width pane p's bar is drawn at at time now.
func (m *downloadsModel) cursorLevel(p paneID, now time.Time) int {
	return m.cursorAnims[p].level(m.cursorRestLevel(p), now)
}

// cursorWidths reads every bar's current width. update takes this before
// handling a message, because a bar redirected mid-travel has to set out again
// from the width it had reached rather than snap back to the one it left.
func (m *downloadsModel) cursorWidths(now time.Time) [paneCount]int {
	var levels [paneCount]int
	for p := range levels {
		levels[p] = m.cursorLevel(paneID(p), now)
	}
	return levels
}

// startCursorAnim sets both bars travelling from the widths they were showing
// toward the ones the new focus calls for. It returns the repaint loop's first
// tick, or nil when a loop is already running — a focus change that lands
// mid-travel joins that loop instead of starting a second one.
func (m *downloadsModel) startCursorAnim(from [paneCount]int, now time.Time) tea.Cmd {
	for p := range m.cursorAnims {
		m.cursorAnims[p] = cursorAnim{from: from[p], start: now}
	}
	if m.cursorTicking {
		return nil
	}
	m.cursorTicking = true
	return cursorTickCmd()
}

// cursorTick keeps the repaint loop alive while a bar is still travelling. Once
// none are it drops the loop and clears the animations, so bars at rest cost
// nothing to draw and the next focus change starts from a clean slate.
func (m *downloadsModel) cursorTick() tea.Cmd {
	now := time.Now()
	for _, anim := range m.cursorAnims {
		if anim.running(now) {
			return cursorTickCmd()
		}
	}
	m.cursorTicking = false
	m.cursorAnims = [paneCount]cursorAnim{}
	return nil
}
