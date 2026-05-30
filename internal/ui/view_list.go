package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// renderList draws the step list: a header, group sub-headings, and one
// status/method/name/code row per visible (filtered) step. Grouped steps get a
// tree connector so the section nesting reads at a glance.
func (m Model) renderList() string {
	innerW := max(m.listW-2, 8) // pane content width, inside its padding
	header := m.stepsHeader(innerW)

	vis := m.visible()
	if len(vis) == 0 {
		msg := "No steps."
		if m.filter != "" {
			msg = "No steps match “" + m.filter + "”."
		}
		return header + "\n" + m.styles.dim.Render(truncate(msg, innerW))
	}

	// Build the windowed (group-heading + step) rows once, then render each:
	// a group-heading row prints its name, a step row defers to renderRow.
	rows := m.windowRows(m.listRows())
	lines := make([]string, len(rows))
	for j, r := range rows {
		if r.step < 0 {
			lines[j] = m.styles.group.Render("▌ " + truncate(r.group, innerW-2))
		} else {
			lines[j] = m.renderRow(r.step, r.conn, innerW)
		}
	}

	return header + "\n" + strings.Join(lines, "\n")
}

// listRow describes one body row of the step list: a group heading (step == -1,
// carrying the heading text) or a step row (step >= 0, carrying its tree
// connector). renderList draws these rows and listLineSteps reads their step
// indices, so both views of the list derive from one source.
type listRow struct {
	step  int    // absolute step index, or -1 for a group-heading row
	group string // heading text, set only on a group-heading row
	conn  string // tree connector for a grouped step row, "" otherwise
}

// listRows builds the full (pre-window) sequence of body rows for the step list
// — group headings interleaved with their step rows — and reports the row index
// of the selected step so the window can keep it in view.
func (m Model) listRows() (rows []listRow, cursorLine int) {
	vis := m.visible()
	rows = make([]listRow, 0, len(vis))
	group := ""
	for p, i := range vis {
		if g := m.plan.Steps[i].Group; g != group {
			group = g
			if group != "" {
				rows = append(rows, listRow{step: -1, group: group})
			}
		}
		// Tree connector: the last visible step of a group elbows, the rest tee.
		conn := ""
		if group != "" {
			if p == len(vis)-1 || m.plan.Steps[vis[p+1]].Group != group {
				conn = "╰"
			} else {
				conn = "├"
			}
		}
		if i == m.cursor {
			cursorLine = len(rows)
		}
		rows = append(rows, listRow{step: i, conn: conn})
	}
	return rows, cursorLine
}

// windowRows scrolls the body rows so the selected one (at cursorLine) stays in
// view and the list never spills past the pane's content area (two rows go to
// the STEPS header). Without this a long plan grows the pane taller than the
// terminal, scrolling the status bar off the top of the alt-screen.
func (m Model) windowRows(rows []listRow, cursorLine int) []listRow {
	if budget := max(m.contentH-2, 1); len(rows) > budget {
		start := clamp(cursorLine-budget/2, 0, len(rows)-budget)
		rows = rows[start : start+budget]
	}
	return rows
}

// listBodyTop is the screen row (0-based) where the step list's first body row
// is drawn: the status bar (1) + the pane's top border (1) + the STEPS header
// and its underline (2). Vertical pane padding is 0, so no extra offset.
const listBodyTop = 4

// listLineSteps returns, for each body row currently drawn in the step list
// (after the same windowing renderList applies), the absolute step index shown
// there, or -1 for a group-heading row, so a mouse click can map a screen row
// back to a step. It shares listRows/windowRows with renderList, so the drawn
// rows and this hit-test can't drift apart.
func (m Model) listLineSteps() []int {
	rows := m.windowRows(m.listRows())
	steps := make([]int, len(rows))
	for j, r := range rows {
		steps[j] = r.step
	}
	return steps
}

// stepAtRow maps a screen row y to the step drawn there in the list pane, if the
// row holds a step (not a group heading or empty space).
func (m Model) stepAtRow(y int) (int, bool) {
	k := y - listBodyTop
	lines := m.listLineSteps()
	if k < 0 || k >= len(lines) || lines[k] < 0 {
		return 0, false
	}
	return lines[k], true
}

// stepsHeader draws the STEPS pane header with a right-aligned progress bar
// reporting how many steps have run out of the total. It sits at the top of the
// (left) list pane so overall progress is obvious at a glance, rather than
// buried in the far corner of the status bar.
func (m Model) stepsHeader(innerW int) string {
	// A bordered box matching paneHeader's underline, but without a foreground so
	// the pre-coloured label/bar/count segments keep their own colours.
	box := lipgloss.NewStyle().Width(innerW).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(palette.border)

	label := m.styles.group.Render("STEPS") // accent + bold, like paneHeader
	total := len(m.plan.Steps)
	if total == 0 {
		return box.Render(label)
	}

	done := m.executedCount()
	count := m.styles.dim.Render(fmt.Sprintf(" %d/%d", done, total))

	// The bar takes the room between the label and the count, bounded so a wide
	// pane doesn't stretch it across the whole header.
	barW := clamp(innerW-lipgloss.Width(label)-lipgloss.Width(count)-1, 0, 18)
	bar := m.progressBar(barW, done, total)

	gap := max(innerW-lipgloss.Width(label)-barW-lipgloss.Width(count), 1)
	return box.Render(label + strings.Repeat(" ", gap) + bar + count)
}

// progressBar renders a w-cell bar with the executed fraction filled. A started
// step always shows at least one filled cell so early progress is visible.
func (m Model) progressBar(w, done, total int) string {
	if w <= 0 {
		return ""
	}
	filled := 0
	if total > 0 {
		filled = done * w / total
		if done > 0 && filled == 0 {
			filled = 1
		}
	}
	filled = clamp(filled, 0, w)
	return lipgloss.NewStyle().Foreground(palette.success).Render(strings.Repeat("█", filled)) +
		m.styles.dim.Render(strings.Repeat("░", w-filled))
}

// executedCount is the number of steps that have finished running (passed or
// failed), used for the progress bar.
func (m Model) executedCount() int {
	n := 0
	for i := range m.plan.Results {
		if s := m.plan.Results[i].Status; s == step.Done || s == step.Failed {
			n++
		}
	}
	return n
}

// renderRow lays out a single step row to exactly innerW columns: an optional
// tree connector, a cursor caret, status glyph, coloured method badge, name,
// then a right-aligned status code. The selected row paints a solid background
// across every segment.
func (m Model) renderRow(i int, conn string, innerW int) string {
	s := m.plan.Steps[i]
	sel := i == m.cursor

	caret, caretColor := " ", lipgloss.TerminalColor(palette.subtle)
	if sel {
		caret, caretColor = "▸", palette.accent
	}

	mb, mc := methodBadge(s)
	code, cc := m.listStatus(i)
	codeW := lipgloss.Width(code)
	connW := lipgloss.Width(conn) // 0 for ungrouped, 1 for grouped

	// Fixed columns before the name: conn + caret + sp + glyph + sp + method(6) + sp.
	fixed := connW + 1 + 1 + 1 + 1 + 6 + 1
	nameMax := max(innerW-fixed-codeW-1, 1)
	name := truncate(m.names[i], nameMax)
	gap := max(innerW-fixed-lipgloss.Width(name)-codeW, 1)

	bg := cell(palette.fg, sel, false) // base style for spaces/padding
	row := ""
	if conn != "" {
		row += cell(palette.border, sel, false).Render(conn)
	}
	row += cell(caretColor, sel, true).Render(caret) +
		bg.Render(" ") +
		m.glyph(i, sel) +
		bg.Render(" ") +
		cell(mc, sel, true).Render(fmt.Sprintf("%-6s", mb)) +
		bg.Render(" ") +
		cell(palette.fg, sel, sel).Render(name) +
		bg.Render(strings.Repeat(" ", gap)) +
		cell(cc, sel, false).Render(code)
	return row
}

// glyph renders the leading status indicator for step i (●/○/✗, or the spinner
// while running), respecting the selected-row background.
func (m Model) glyph(i int, sel bool) string {
	r := m.plan.Results[i]
	if r.Status == step.Running {
		return m.spinner.View() // already styled by the spinner widget
	}
	g, c := "○", lipgloss.TerminalColor(palette.subtle)
	if r.Status == step.Done || r.Status == step.Failed {
		switch {
		case r.Err != nil, !r.AssertsPass():
			g, c = "✗", palette.danger
		default:
			isHTTP := m.plan.Steps[i].Kind == step.KindHTTP
			code := r.StatusCode
			if !isHTTP {
				code = r.ExitCode
			}
			g, c = "●", statusColor(code, isHTTP)
		}
	}
	return cell(c, sel, false).Render(g)
}

// methodBadge returns the short verb label and colour for a step's list row.
func methodBadge(s step.Step) (string, lipgloss.TerminalColor) {
	if s.Kind == step.KindShell {
		return "SH", palette.teal
	}
	return s.Method, methodColor(s.Method)
}

// listStatus returns the right-aligned status text (HTTP code or shell exit
// code) and its colour. Steps that haven't finished show nothing.
func (m Model) listStatus(i int) (string, lipgloss.TerminalColor) {
	r := m.plan.Results[i]
	if r.Status != step.Done && r.Status != step.Failed {
		return "", palette.subtle
	}
	if r.Err != nil {
		return "ERR", palette.danger
	}
	if m.plan.Steps[i].Kind == step.KindHTTP {
		return strconv.Itoa(r.StatusCode), statusColor(r.StatusCode, true)
	}
	return "exit " + strconv.Itoa(r.ExitCode), statusColor(r.ExitCode, false)
}
