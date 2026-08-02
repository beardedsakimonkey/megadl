package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// doubleClickInterval is how close two presses on the same row must be to
// count as a double click.
const doubleClickInterval = 400 * time.Millisecond

// clickKind names the list a click landed in so presses in different lists
// never pair up into a double click.
type clickKind int

const (
	clickNone clickKind = iota
	clickDownload
	clickFile
)

// clickTracker folds a stream of left presses into single and double clicks.
// now is a test seam; nil means time.Now.
type clickTracker struct {
	kind  clickKind
	index int
	at    time.Time
	now   func() time.Time
}

// press records a press on a row and reports whether it completes a double
// click on the row the previous press hit.
func (c *clickTracker) press(kind clickKind, index int) bool {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	at := now()
	double := c.kind == kind && c.index == index && at.Sub(c.at) <= doubleClickInterval
	c.kind, c.index = kind, index
	if double {
		// a third press starts a new pair rather than firing again
		c.at = time.Time{}
	} else {
		c.at = at
	}
	return double
}

// leftPress reports whether msg is the button going down, ignoring the
// release and the drag motions that cell-motion tracking also reports.
func leftPress(msg tea.MouseMsg) bool {
	return msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft
}

// wheelDelta returns -1 for one notch up, +1 for one notch down, 0 otherwise.
func wheelDelta(msg tea.MouseMsg) int {
	if msg.Action != tea.MouseActionPress {
		return 0
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return -1
	case tea.MouseButtonWheelDown:
		return 1
	}
	return 0
}
