package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"megadl/internal/db"
)

func TestCursorAnimLevel(t *testing.T) {
	now := time.Now()
	anim := cursorAnim{from: cursorThin, start: now}

	if got := anim.level(cursorFull, now); got != cursorThin {
		t.Fatalf("level at the start = %d, want %d", got, cursorThin)
	}
	mid := anim.level(cursorFull, now.Add(cursorAnimDuration/2))
	if mid <= cursorThin || mid >= cursorFull {
		t.Fatalf("level mid-travel = %d, want between %d and %d", mid, cursorThin, cursorFull)
	}
	if got := anim.level(cursorFull, now.Add(cursorAnimDuration)); got != cursorFull {
		t.Fatalf("level once settled = %d, want %d", got, cursorFull)
	}

	// A bar thinning out is the same travel run backwards.
	shrinking := cursorAnim{from: cursorFull, start: now}
	if got := shrinking.level(cursorThin, now); got != cursorFull {
		t.Fatalf("shrinking level at the start = %d, want %d", got, cursorFull)
	}
	if got := shrinking.level(cursorThin, now.Add(cursorAnimDuration)); got != cursorThin {
		t.Fatalf("shrinking level once settled = %d, want %d", got, cursorThin)
	}

	// A bar that has never moved sits at whatever its pane calls for and asks
	// for no repaints.
	var rest cursorAnim
	if got := rest.level(cursorFull, now); got != cursorFull {
		t.Fatalf("resting level = %d, want %d", got, cursorFull)
	}
	if rest.running(now) {
		t.Fatal("a bar at rest should not keep the repaint loop alive")
	}
}

// cursorAnimModel is two downloads of two files each: enough for the cursor to
// move inside either pane and to cross between them.
func cursorAnimModel(t *testing.T) *downloadsModel {
	t.Helper()
	app, database := openAddlinkTestApp(t)
	for _, name := range []string{"Skins", "Other"} {
		files := make([]db.File, 2)
		for i := range files {
			base := fmt.Sprintf("%c.mkv", 'a'+i)
			files[i] = db.File{
				NodeHandle: name + base,
				RemotePath: "/" + name + "/" + base,
				LocalPath:  "/dl/" + name + "/" + base,
				Size:       50,
			}
		}
		if _, err := database.InsertDownload(&db.Download{
			URL: "u" + name, Handle: "h" + name, LinkType: "folder",
			Name: name, DestPath: "/dl/" + name,
		}, files); err != nil {
			t.Fatal(err)
		}
	}
	app.downloads.reload()
	app.downloads.loadFiles()
	return &app.downloads
}

func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// Moving between rows is a visible event of its own — the bar appears on
// another row — so the bar does not animate and the pane costs no repaints.
func TestCursorMoveLeavesBarAlone(t *testing.T) {
	m := cursorAnimModel(t)
	if got := m.cursorLevel(paneList, time.Now()); got != cursorFull {
		t.Fatalf("resting focused level = %d, want %d", got, cursorFull)
	}

	// Both panes, since each moves its cursor by its own path.
	for _, pane := range []paneID{paneList, paneFiles} {
		if pane == paneFiles {
			m.update(runeKey("l")) // crossing panes is the one move that animates
			m.cursorAnims, m.cursorTicking = [paneCount]cursorAnim{}, false
		}
		for _, key := range []string{"j", "j", "k"} {
			if cmd := m.update(runeKey(key)); cmd != nil {
				t.Fatalf("%q moved the cursor within pane %v and asked for a repaint", key, pane)
			}
		}
		if m.cursorTicking || m.cursorAnims != [paneCount]cursorAnim{} {
			t.Fatalf("moving the cursor left animation state behind: %+v", m.cursorAnims)
		}
	}
}

func TestSettledBarsStopTheRepaintLoop(t *testing.T) {
	m := cursorAnimModel(t)
	if cmd := m.update(runeKey("l")); cmd == nil {
		t.Fatal("moving focus should schedule a repaint")
	}
	if !m.cursorTicking {
		t.Fatal("the repaint loop should be running")
	}
	// Still travelling: the loop keeps itself alive.
	if cmd := m.cursorTick(); cmd == nil {
		t.Fatal("the repaint loop should continue while a bar is travelling")
	}

	// Once every bar has settled the loop stops and leaves no state behind.
	for p := range m.cursorAnims {
		m.cursorAnims[p].start = time.Now().Add(-2 * cursorAnimDuration)
	}
	if cmd := m.cursorTick(); cmd != nil {
		t.Fatal("the repaint loop should stop once the bars settle")
	}
	if m.cursorTicking || m.cursorAnims != [paneCount]cursorAnim{} {
		t.Fatalf("settled bars left state behind: %+v", m.cursorAnims)
	}
}

func TestFocusChangeAnimatesBothPanes(t *testing.T) {
	m := cursorAnimModel(t)
	if cmd := m.update(runeKey("l")); cmd == nil {
		t.Fatal("moving focus should schedule a repaint")
	}
	if m.pane != paneFiles {
		t.Fatalf("pane = %v, want the file pane", m.pane)
	}

	// The pane that gained focus grows from the hairline it was resting at;
	// the one that lost it thins out from the full block it was showing.
	start := m.cursorAnims[paneFiles].start
	if got := m.cursorLevel(paneFiles, start); got != cursorThin {
		t.Fatalf("gaining pane starts at %d, want %d", got, cursorThin)
	}
	if got := m.cursorLevel(paneList, start); got != cursorFull {
		t.Fatalf("losing pane starts at %d, want %d", got, cursorFull)
	}
	settled := start.Add(cursorAnimDuration)
	if got := m.cursorLevel(paneFiles, settled); got != cursorFull {
		t.Fatalf("gaining pane settles at %d, want %d", got, cursorFull)
	}
	if got := m.cursorLevel(paneList, settled); got != cursorThin {
		t.Fatalf("losing pane settles at %d, want %d", got, cursorThin)
	}

	// Focus handed back mid-travel picks the bars up where they are rather
	// than snapping them to the width they set out from, and joins the repaint
	// loop already running instead of starting a second one.
	half := time.Now().Add(-cursorAnimDuration / 2)
	m.cursorAnims[paneFiles].start, m.cursorAnims[paneList].start = half, half
	if cmd := m.update(runeKey("h")); cmd != nil {
		t.Fatal("a second focus change should not start a second repaint loop")
	}
	if got := m.cursorAnims[paneFiles].from; got <= cursorThin {
		t.Fatalf("redirected bar restarts from %d, want the width it had reached", got)
	}
	if got := m.cursorAnims[paneList].from; got >= cursorFull {
		t.Fatalf("redirected bar restarts from %d, want the width it had reached", got)
	}
}

func TestBlurredPaneKeepsThinBar(t *testing.T) {
	m := cursorAnimModel(t)
	m.update(runeKey("l")) // focus the file pane
	m.cursorAnims = [paneCount]cursorAnim{}

	list := ansi.Strip(m.listView(40, 4))
	if !strings.HasPrefix(list, cursorLevels[cursorThin-1]+" ") {
		t.Fatalf("blurred list bar should rest thin:\n%s", list)
	}
	files := ansi.Strip(m.filesView(40, 4))
	if !strings.Contains(files, "│ "+cursorLevels[cursorFull-1]+" ") {
		t.Fatalf("focused file bar should rest full:\n%s", files)
	}
}
