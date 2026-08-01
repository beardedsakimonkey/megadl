package ui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"megadl/internal/db"
)

// listFilter narrows the downloads pane to the rows whose name matches what is
// typed into it. focused says the prompt is taking keys; the query outlives
// that, so enter can hand the pane back its shortcuts while the list stays
// narrowed under a prompt that says so.
type listFilter struct {
	input   textinput.Model
	focused bool
}

// query is what the list is narrowed by, whether or not the prompt still has
// focus. The prompt is the only place it is kept, so there is no second copy to
// fall out of step with what the line shows.
func (m *downloadsModel) query() string {
	return m.filter.input.Value()
}

// filtering reports whether the prompt has focus, and so whether keys are text
// for it rather than shortcuts.
func (m *downloadsModel) filtering() bool {
	return m.filter.focused
}

// filterShown reports whether the prompt is on screen: while it has focus, and
// after that for as long as a query is left narrowing the list.
func (m *downloadsModel) filterShown() bool {
	return m.filter.focused || m.query() != ""
}

// startFilter opens the prompt at the top of the downloads pane. It opens on
// whatever is already narrowing the list, so / edits a query as well as begins
// one.
func (m *downloadsModel) startFilter() tea.Cmd {
	in := textinput.New()
	in.Prompt = "/"
	in.PromptStyle = styleAccent
	// The width is set before the value so bubbles works out which part of a
	// long query is on screen against the width it will be rendered at.
	in.Width = max(8, m.listW-4)
	in.SetValue(m.query())
	in.CursorEnd()
	m.filter = listFilter{input: in, focused: true}
	m.pane = paneList // the filter narrows the list, so that is what it acts on
	return tea.Batch(m.filter.input.Focus(), textinput.Blink)
}

// acceptFilter hands focus back to the list with the query still narrowing it.
func (m *downloadsModel) acceptFilter() {
	m.filter.focused = false
	m.filter.input.Blur()
}

// clearFilter drops the query and closes the prompt, putting the whole library
// back in the pane. This is esc's one job in the downloads view.
func (m *downloadsModel) clearFilter() {
	if !m.filterShown() {
		return
	}
	m.resetFilter()
	m.refilter()
}

// resetFilter empties the prompt without re-narrowing the list, for callers
// that are about to reload it anyway.
func (m *downloadsModel) resetFilter() {
	m.filter = listFilter{}
}

// filterKey is every key the prompt gets while it has focus: the two that close
// it, the ones that reach past it to the list, and text for everything else.
func (m *downloadsModel) filterKey(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "esc":
		m.clearFilter()
		return nil
	case "enter":
		m.acceptFilter()
		return nil
	case "up", "ctrl+p":
		m.moveCursor(-1)
		return nil
	case "down", "ctrl+n":
		m.moveCursor(1)
		return nil
	}
	return m.filterUpdate(key)
}

// filterUpdate gives the prompt a message — a key, a cursor blink, a clipboard
// read — and re-narrows the list whenever that changed the query.
func (m *downloadsModel) filterUpdate(msg tea.Msg) tea.Cmd {
	before := m.query()
	var cmd tea.Cmd
	m.filter.input, cmd = m.filter.input.Update(msg)
	if m.query() != before {
		m.refilter()
	}
	return cmd
}

// refilter re-narrows the list to the current query. The cursor stays on the
// download it was on for as long as that row survives, so typing another
// character doesn't move the selection out from under it; a row the query drops
// takes the cursor to the top match.
func (m *downloadsModel) refilter() {
	var id int64
	if m.cursor < len(m.rows) {
		id = m.rows[m.cursor].ID
	}
	m.rows = filterDownloads(m.all, m.query())
	m.cursor = 0
	for i, dl := range m.rows {
		if dl.ID == id {
			m.cursor = i
			break
		}
	}
	m.loadFiles()
}

// filterHides reports whether the filter is keeping a download that is in the
// library out of the pane.
func (m *downloadsModel) filterHides(id int64) bool {
	if m.query() == "" {
		return false
	}
	for _, dl := range m.rows {
		if dl.ID == id {
			return false
		}
	}
	for _, dl := range m.all {
		if dl.ID == id {
			return true
		}
	}
	return false
}

// filterDownloads keeps the downloads whose name matches query, in library
// order. An empty query keeps every row, so a pane that was never filtered and
// one whose filter was cleared come out of the same code path.
func filterDownloads(rows []*db.Download, query string) []*db.Download {
	if query == "" {
		return rows
	}
	out := make([]*db.Download, 0, len(rows))
	for _, dl := range rows {
		if matchesQuery(dl.Name, query) {
			out = append(out, dl)
		}
	}
	return out
}

// matchesQuery is a plain substring test — nothing fuzzy, so what is typed has
// to actually be in the name — with smart case: an all-lower-case query ignores
// case, and one upper-case character anywhere in it makes the whole match
// case-sensitive. It asks matchRanges rather than testing on its own, so the
// rule that decides which rows survive is the same one that decides what the
// surviving rows highlight and the two can't disagree.
func matchesQuery(name, query string) bool {
	return query == "" || len(matchRanges(name, query)) > 0
}

// matchRanges returns the byte ranges of name that query matched, in order and
// without overlap. An empty query matches nothing rather than everything: it
// leaves the list alone, so there is nothing in it to point at.
func matchRanges(name, query string) [][2]int {
	if query == "" {
		return nil
	}
	hay, needle := []rune(name), []rune(query)
	if !hasUpper(query) {
		hay, needle = foldRunes(hay), foldRunes(needle)
	}
	if len(needle) > len(hay) {
		return nil
	}
	// Where each rune of hay starts in name, plus the end, so a match found in
	// the folded runes can be cut back out of the original bytes.
	offs := make([]int, 0, len(hay)+1)
	for i := range name {
		offs = append(offs, i)
	}
	offs = append(offs, len(name))

	var out [][2]int
	for i := 0; i+len(needle) <= len(hay); {
		if slices.Equal(hay[i:i+len(needle)], needle) {
			out = append(out, [2]int{offs[i], offs[i+len(needle)]})
			i += len(needle)
			continue
		}
		i++
	}
	return out
}

// foldRunes lower-cases rune by rune rather than through strings.ToLower. The
// special-casing ToLower does can return more runes than it was given, which
// would put every offset past it in the wrong place; one rune in, one rune out
// is what keeps a match in the folded text a match in the text on screen.
func foldRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}

// highlightQuery renders name with the query's matches picked out, so a row
// says which part of it is the reason it survived the filter. It takes the name
// already truncated to the column, so the marks land on the letters actually
// drawn rather than on offsets into a string the pane is only showing the
// front of.
func highlightQuery(name, query string) string {
	ranges := matchRanges(name, query)
	if len(ranges) == 0 {
		return name
	}
	var b strings.Builder
	end := 0
	for _, r := range ranges {
		b.WriteString(name[end:r[0]])
		b.WriteString(styleMatch.Render(name[r[0]:r[1]]))
		end = r[1]
	}
	b.WriteString(name[end:])
	return b.String()
}

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// filterView is the prompt line at the top of the downloads pane: the query,
// and how much of the library it is leaving on screen. The count turns red when
// nothing matches, which is the one case where an empty pane needs explaining.
func (m *downloadsModel) filterView(width int) string {
	if width <= 0 {
		return ""
	}
	count := ""
	if m.query() != "" {
		count = fmt.Sprintf("%d/%d ", len(m.rows), len(m.all))
	}
	countW := lipgloss.Width(count)
	// bubbles renders the line as its prompt plus Width+1 cells, so the input
	// gets what is left of the pane once the prompt, the count and the leading
	// space are paid for
	m.filter.input.Width = max(4, width-2-promptWidth(m.filter.input)-countW)
	line := " " + m.filter.input.View()

	style := styleDim
	if len(m.rows) == 0 {
		style = styleError
	}
	gap := max(0, width-lipgloss.Width(line)-countW)
	return line + strings.Repeat(" ", gap) + style.Render(count)
}
