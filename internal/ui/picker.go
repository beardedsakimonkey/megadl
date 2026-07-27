package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"megadl/internal/mega"
)

// pickerModel is the checkbox file tree for folder links. Selection is
// tracked per file; folders derive their state from their files, which
// keeps toggle semantics unambiguous.
type pickerRow struct {
	node  mega.Node
	depth int
}

type pickerModel struct {
	rows     []pickerRow
	selected map[string]bool // file handle -> selected
	cursor   int
	offset   int
}

func newPicker(nodes []mega.Node) pickerModel {
	depth := map[string]int{}
	for _, n := range nodes {
		if n.Parent == "" {
			depth[n.Handle] = 0
		} else {
			depth[n.Handle] = depth[n.Parent] + 1
		}
	}
	p := pickerModel{selected: map[string]bool{}}
	for _, n := range nodes {
		p.rows = append(p.rows, pickerRow{node: n, depth: depth[n.Handle]})
		if !n.IsDir() {
			p.selected[n.Handle] = true // default: everything selected
		}
	}
	return p
}

// subtree returns the row index range [i, end) covered by rows[i].
func (p *pickerModel) subtree(i int) int {
	end := i + 1
	for end < len(p.rows) && p.rows[end].depth > p.rows[i].depth {
		end++
	}
	return end
}

// folderState: 0 = none, 1 = partial, 2 = all files selected.
func (p *pickerModel) folderState(i int) int {
	all, any := true, false
	for j := i + 1; j < p.subtree(i); j++ {
		if p.rows[j].node.IsDir() {
			continue
		}
		if p.selected[p.rows[j].node.Handle] {
			any = true
		} else {
			all = false
		}
	}
	if !any {
		return 0
	}
	if all {
		return 2
	}
	return 1
}

func (p *pickerModel) toggle(i int) {
	row := p.rows[i]
	if !row.node.IsDir() {
		p.selected[row.node.Handle] = !p.selected[row.node.Handle]
		return
	}
	target := p.folderState(i) != 2 // partial/none -> select all, all -> none
	for j := i + 1; j < p.subtree(i); j++ {
		if !p.rows[j].node.IsDir() {
			p.selected[p.rows[j].node.Handle] = target
		}
	}
}

func (p *pickerModel) setAll(v bool) {
	for _, r := range p.rows {
		if !r.node.IsDir() {
			p.selected[r.node.Handle] = v
		}
	}
}

// selectedFiles returns every selected file node.
func (p *pickerModel) selectedFiles() []mega.Node {
	var out []mega.Node
	for _, r := range p.rows {
		if !r.node.IsDir() && p.selected[r.node.Handle] {
			out = append(out, r.node)
		}
	}
	return out
}

// minimalHandles collapses fully-selected folders into a single handle
// for the native driver's selected-handle pruning.
func (p *pickerModel) minimalHandles() []string {
	var out []string
	i := 0
	for i < len(p.rows) {
		row := p.rows[i]
		if row.node.IsDir() {
			if p.folderState(i) == 2 && p.hasFiles(i) {
				out = append(out, row.node.Handle)
				i = p.subtree(i)
				continue
			}
			i++
			continue
		}
		if p.selected[row.node.Handle] {
			out = append(out, row.node.Handle)
		}
		i++
	}
	return out
}

func (p *pickerModel) hasFiles(i int) bool {
	for j := i + 1; j < p.subtree(i); j++ {
		if !p.rows[j].node.IsDir() {
			return true
		}
	}
	return false
}

func (p *pickerModel) totals() (count int, bytes int64) {
	for _, r := range p.rows {
		if !r.node.IsDir() && p.selected[r.node.Handle] {
			count++
			bytes += r.node.Size
		}
	}
	return
}

func (p *pickerModel) move(delta, visible int) {
	p.cursor += delta
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.rows) {
		p.cursor = len(p.rows) - 1
	}
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+visible {
		p.offset = p.cursor - visible + 1
	}
}

func (p *pickerModel) view(width, height int) string {
	var b strings.Builder
	visible := max(3, height)
	endRow := min(len(p.rows), p.offset+visible)

	// The cursor's band spans the widest visible row rather than the whole
	// modal, so the modal keeps sizing itself to its content.
	lines := make([]string, 0, endRow-p.offset)
	bandW := 0
	for i := p.offset; i < endRow; i++ {
		row := p.rows[i]
		var box string
		if row.node.IsDir() {
			switch p.folderState(i) {
			case 2:
				box = "[x]"
			case 1:
				box = "[~]"
			default:
				box = "[ ]"
			}
		} else if p.selected[row.node.Handle] {
			box = "[x]"
		} else {
			box = "[ ]"
		}

		name := row.node.Name
		if row.node.IsDir() {
			name += "/"
		}
		line := fmt.Sprintf("%s%s%s %s", cursorBar(i == p.cursor, true),
			strings.Repeat("  ", row.depth), box,
			truncate(name, max(10, width-22-2*row.depth)))
		if !row.node.IsDir() {
			line += styleDim.Render("  " + humanBytes(row.node.Size))
		}
		bandW = max(bandW, lipgloss.Width(line))
		lines = append(lines, line)
	}

	for i, line := range lines {
		if p.offset+i == p.cursor {
			line = tintRow(line, bandW)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}

	count, bytes := p.totals()
	b.WriteString(styleAccent.Render(fmt.Sprintf("\n%d files selected, %s", count, humanBytes(bytes))))
	return b.String()
}
