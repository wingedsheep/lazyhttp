package ui

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/wingedsheep/lazyhttp/internal/capture"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

// layout sizes the panes and the result viewport for the current window. It is
// called on resize and whenever the footer height changes (help toggle).
func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	m.help.Width = m.width
	footerH := strings.Count(m.help.View(m.keys), "\n") + 1

	// Content height inside the pane borders.
	contentH := m.height - 1 /*title*/ - footerH - 2 /*pane borders*/
	contentH = max(contentH, 3)

	// Give the step list ~42% of the width (more room for long descriptions),
	// but keep it within sensible bounds so the result pane stays usable.
	listW := clamp(m.width*42/100-4, 28, 64)
	resultW := m.width - (listW + 4) - 4
	resultW = max(resultW, 20)

	m.listW, m.resultW, m.contentH = listW, resultW, contentH

	// The viewport occupies the result pane's content area (inside the 1-col
	// padding on each side) minus the header, which is two rows: the label plus
	// the underline drawn by paneHeader's bottom border. Counting it as one row
	// made the result pane a line taller than the list, pushing the whole View
	// past the terminal height so the status bar scrolled off the top.
	m.viewport.Width = max(resultW-2, 1)
	m.viewport.Height = max(contentH-2, 1)
}

// refreshResult renders the selected step's request/response into the viewport.
func (m *Model) refreshResult() {
	if len(m.steps) == 0 {
		m.viewport.SetContent("")
		return
	}
	m.viewport.SetContent(m.formatResult(m.cursor))
	m.viewport.GotoTop()
}

func (m Model) View() string {
	if m.loadErr != nil {
		title := lipgloss.NewStyle().Foreground(palette.danger).Bold(true).
			Render("✗ Could not load " + m.path)
		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(palette.danger).
			Padding(0, 1).
			Render(title + "\n\n" + m.styles.dim.Render(m.loadErr.Error()))
		return m.styles.logo.Render("lazyhttp") + "\n\n" + box + "\n"
	}
	if m.width == 0 {
		return m.styles.dim.Render("loading…")
	}

	// The env picker takes over the screen as a modal: the status bar stays
	// (keeping the env/path/theme context visible) and the picker fills the rest,
	// centred. Sizing it to the exact terminal height — rather than overlaying the
	// step/result panes, whose content can run taller than their box — guarantees
	// the output is never taller than the screen, which the renderer can't draw
	// and would show as a frozen/garbled UI. The clamp is a final safety net.
	if m.envPicking {
		avail := max(m.height-1, 1) // rows below the status bar
		modal := lipgloss.Place(m.width, avail, lipgloss.Center, lipgloss.Center, m.renderEnvPicker(avail))
		out := lipgloss.JoinVertical(lipgloss.Left, m.statusBar(), modal)
		return lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(out)
	}

	listFocused := m.focus == focusList
	list := pane(listFocused, m.listW, m.contentH).Render(m.renderList())
	result := pane(!listFocused, m.resultW, m.contentH).Render(m.renderResult())

	body := lipgloss.JoinHorizontal(lipgloss.Top, list, result)
	footer := m.help.View(m.keys)

	return lipgloss.JoinVertical(lipgloss.Left, m.statusBar(), body, footer)
}

// renderEnvPicker draws the modal environment chooser: a titled, bordered box
// listing the environments with a caret on the highlighted row and a dot on the
// one currently in use. It is bounded to maxH lines (the pane height) and scrolls
// a window of options around the cursor when there are more than fit, so the box
// never overflows the screen no matter how many environments or how short the
// terminal.
func (m Model) renderEnvPicker(maxH int) string {
	opts := m.envOptions()

	// Chrome around the option rows: border (2) + vertical padding (2) +
	// title (1) + hint (1). Whatever height is left holds the visible options.
	const chrome = 6
	visN := clamp(maxH-chrome, 1, len(opts))

	// Scroll a window of visN options so the highlighted one stays in view.
	start := clamp(m.envCursor-visN/2, 0, max(len(opts)-visN, 0))
	end := min(start+visN, len(opts))

	var b strings.Builder
	b.WriteString(m.styles.paneHeader.Render("SELECT ENVIRONMENT") + "\n")
	for i := start; i < end; i++ {
		name := opts[i]
		label := name
		if name == "" {
			label = "(none)"
		}
		caret, st := "  ", m.styles.dim
		if i == m.envCursor {
			caret = lipgloss.NewStyle().Foreground(palette.accent).Render("▸ ")
			st = lipgloss.NewStyle().Foreground(palette.fg).Bold(true)
		}
		row := caret + st.Render(label)
		if name == m.envName {
			row += lipgloss.NewStyle().Foreground(palette.success).Render(" ●")
		}
		b.WriteString(row + "\n")
	}

	// Show a count when the list is scrolled so the hidden entries aren't a
	// surprise; otherwise just the key hints.
	hint := "↑/↓ move · enter apply · esc cancel"
	if start > 0 || end < len(opts) {
		hint = fmt.Sprintf("%d-%d/%d · %s", start+1, end, len(opts), hint)
	}
	b.WriteString(m.styles.dim.Render(hint))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(palette.accent).
		Padding(1, 3).
		Render(b.String())
}

// statusBar is the full-width top bar. The left shows the logo badge, the
// current plan file and the active filter; the right shows the selected
// environment, theme, cursor position and the aggregate assertion badge. Every
// piece of context the user controls — file, env, filter, theme — stays
// visible at once on a single tinted strip spanning the terminal width.
func (m Model) statusBar() string {
	bar := m.styles.barText

	// Right side first: env and theme are always shown (so the current choice
	// is unambiguous even when it's "none"), followed by position and asserts.
	right := m.barEnv() + m.barTheme()
	if len(m.steps) > 0 {
		right += bar.Render(fmt.Sprintf("   %d/%d", m.cursor+1, len(m.steps)))
	}
	if s := m.assertSummary(); s != "" {
		right += bar.Render("   ") + s
	}
	right += bar.Render(" ")

	// Left side: the file fills whatever width the fixed pieces leave, truncated
	// so the bar never grows past the terminal and wraps.
	logo := m.styles.logo.Render("lazyhttp")
	filter := m.barFilter()
	used := lipgloss.Width(logo) + lipgloss.Width(filter) + lipgloss.Width(right)
	left := logo + m.barFile(m.width-used) + filter

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	return left + bar.Render(strings.Repeat(" ", gap)) + right
}

// barFile renders the current plan file as a labelled bar segment, the file
// name in bright text so it reads at a glance, truncated to at most w columns.
// It contributes nothing when there isn't room for a meaningful label.
func (m Model) barFile(w int) string {
	const label = "  file:"
	if w < lipgloss.Width(label)+1 {
		return ""
	}
	name := truncate(m.path, w-lipgloss.Width(label))
	return m.styles.barText.Render(label) +
		lipgloss.NewStyle().Background(palette.crust).Foreground(palette.fg).Render(name)
}

// barEnv renders the selected environment, always present so the current choice
// is visible; an unset environment reads as "(none)". The label and value share
// one Render so the "env:NAME" text stays contiguous.
func (m Model) barEnv() string {
	name := m.envName
	if name == "" {
		name = "(none)"
	}
	return lipgloss.NewStyle().Background(palette.crust).Foreground(palette.fg).
		Render("env:"+name) + m.styles.barText.Render("   ")
}

// barTheme renders the active theme name, prefixed with a contrast glyph so the
// segment reads as the theme indicator.
func (m Model) barTheme() string {
	icon := lipgloss.NewStyle().Background(palette.crust).Foreground(palette.accent).Render("◐ ")
	return icon + lipgloss.NewStyle().Background(palette.crust).Foreground(palette.fg).
		Render(themes[activeTheme].name)
}

// barFilter renders the filter segment: the live editor while typing, or the
// applied query when one is set. It contributes nothing when no filter is
// active — the file segment already anchors the left of the bar.
func (m Model) barFilter() string {
	bar := m.styles.barText
	slash := lipgloss.NewStyle().Background(palette.crust).Foreground(palette.accent)
	if m.filtering {
		caret := lipgloss.NewStyle().Background(palette.accent).Foreground(palette.crust).Render(" ")
		return bar.Render("   ") + slash.Render("/") +
			lipgloss.NewStyle().Background(palette.crust).Foreground(palette.fg).Render(m.filter) + caret
	}
	if m.filter != "" {
		return bar.Render("   ") + slash.Render("/"+m.filter)
	}
	return ""
}

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

	// Render the body as individual lines (group headings plus step rows),
	// remembering the line index of the selected row.
	var lines []string
	cursorLine := 0
	group := ""
	for p, i := range vis {
		if g := m.steps[i].Group; g != group {
			group = g
			if group != "" {
				lines = append(lines, m.styles.group.Render("▌ "+truncate(group, innerW-2)))
			}
		}
		// Tree connector: the last visible step of a group elbows, the rest tee.
		conn := ""
		if group != "" {
			if p == len(vis)-1 || m.steps[vis[p+1]].Group != group {
				conn = "╰"
			} else {
				conn = "├"
			}
		}
		if i == m.cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, m.renderRow(i, conn, innerW))
	}

	// Scroll a window of the body that keeps the cursor in view and never spills
	// past the pane's content area (two rows go to the STEPS header). Without
	// this a long plan grows the pane taller than the terminal, scrolling the
	// status bar off the top of the alt-screen.
	if budget := max(m.contentH-2, 1); len(lines) > budget {
		start := clamp(cursorLine-budget/2, 0, len(lines)-budget)
		lines = lines[start : start+budget]
	}

	return header + "\n" + strings.Join(lines, "\n")
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
	total := len(m.steps)
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
	for i := range m.results {
		if s := m.results[i].Status; s == step.Done || s == step.Failed {
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
	s := m.steps[i]
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
	r := m.results[i]
	if r.Status == step.Running {
		return m.spinner.View() // already styled by the spinner widget
	}
	g, c := "○", lipgloss.TerminalColor(palette.subtle)
	if r.Status == step.Done || r.Status == step.Failed {
		switch {
		case r.Err != nil, !r.AssertsPass():
			g, c = "✗", palette.danger
		default:
			isHTTP := m.steps[i].Kind == step.KindHTTP
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
	r := m.results[i]
	if r.Status != step.Done && r.Status != step.Failed {
		return "", palette.subtle
	}
	if r.Err != nil {
		return "ERR", palette.danger
	}
	if m.steps[i].Kind == step.KindHTTP {
		return strconv.Itoa(r.StatusCode), statusColor(r.StatusCode, true)
	}
	return "exit " + strconv.Itoa(r.ExitCode), statusColor(r.ExitCode, false)
}

// renderResult draws the result pane: a header (with a scroll indicator when
// the body overflows) plus the scrollable viewport.
func (m Model) renderResult() string {
	w := m.viewport.Width
	label := "RESPONSE"
	if ind := m.scrollIndicator(); ind != "" {
		gap := max(w-lipgloss.Width(label)-lipgloss.Width(ind), 1)
		label += strings.Repeat(" ", gap) + ind
	}
	header := m.styles.paneHeader.Width(w).Render(label)
	return header + "\n" + m.viewport.View()
}

// scrollIndicator reports the scroll position of the response body, but only
// when it's taller than the viewport. Arrows dim out at the top/bottom.
func (m Model) scrollIndicator() string {
	if m.viewport.TotalLineCount() <= m.viewport.Height {
		return ""
	}
	up, down := "↑", "↓"
	if m.viewport.AtTop() {
		up = " "
	}
	if m.viewport.AtBottom() {
		down = " "
	}
	return fmt.Sprintf("%s%s %d%%", up, down, int(m.viewport.ScrollPercent()*100))
}

// formatResult builds the full request+response text for step i, with all
// {{vars}} expanded against the current variable set so the preview matches
// what will actually run.
func (m Model) formatResult(i int) string {
	// expand may fail to read a `< file` body; the preview still shows the
	// request line and the file reference, with the error noted below.
	s, expandErr := m.expand(m.steps[i])
	r := m.results[i]
	var b strings.Builder

	// Request preview — optional, toggled with `i`. Off by default so the
	// response output gets the full pane.
	if m.showDetails {
		if s.Kind == step.KindShell {
			b.WriteString(m.styles.dim.Render("$ shell") + "\n")
			b.WriteString(s.Body + "\n")
		} else {
			b.WriteString(m.styles.method.Foreground(palette.accent).
				Render(s.Method) + " " + s.URL + "\n")
			for k, v := range s.Headers {
				b.WriteString(m.styles.dim.Render(k+": "+v) + "\n")
			}
			switch {
			case s.BodyFile != "":
				ref := "< " + s.BodyFile
				if s.BodyFileVars {
					ref = "<@ " + s.BodyFile
				}
				b.WriteString("\n" + m.styles.dim.Render("body from "+ref) + "\n")
				if expandErr != nil {
					b.WriteString(lipgloss.NewStyle().Foreground(palette.danger).
						Render(expandErr.Error()) + "\n")
				} else if s.Body != "" {
					b.WriteString(highlightJSON(s.Body, jsonTheme) + "\n")
				}
			case s.Body != "":
				b.WriteString("\n" + highlightJSON(s.Body, jsonTheme) + "\n")
			}
		}
		b.WriteString(m.styles.dim.Render(strings.Repeat("─", min(m.viewport.Width, 40))) + "\n")
	}

	// Response.
	switch r.Status {
	case step.Pending:
		// Keep the newlines outside Render: lipgloss relocates trailing newlines
		// inside a styled span, which would mis-indent the hint line.
		key := lipgloss.NewStyle().Foreground(palette.accent).Bold(true)
		b.WriteString(m.styles.dim.Render("○ Not run yet.") + "\n\n")
		b.WriteString(key.Render("enter") + m.styles.dim.Render(" run   ·   ") +
			key.Render("a") + m.styles.dim.Render(" run from here"))
	case step.Running:
		b.WriteString(m.spinner.View() + m.styles.dim.Render(" running…"))
	default:
		b.WriteString(m.responseSummary(i) + "\n")
		if r.Err != nil {
			b.WriteString(lipgloss.NewStyle().Foreground(palette.danger).
				Render(r.Err.Error()))
			break
		}
		// Response headers are detail, hidden unless toggled with `i`.
		if m.showDetails && s.Kind == step.KindHTTP {
			for k, v := range r.Header {
				b.WriteString(m.styles.dim.Render(k+": "+strings.Join(v, ", ")) + "\n")
			}
		}
		b.WriteString("\n" + m.cachedBody(i, r))
		b.WriteString(m.assertLines(r))
		b.WriteString(m.capturedLines(s, r))
	}
	return b.String()
}

// assertLines renders the assertion outcomes for a finished step.
func (m Model) assertLines(r step.Result) string {
	if len(r.Asserts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n" + m.styles.paneHeader.Render("ASSERTIONS") + "\n")
	for _, a := range r.Asserts {
		glyph, c := "✓", palette.success
		if !a.Pass {
			glyph, c = "✗", palette.danger
		}
		line := lipgloss.NewStyle().Foreground(c).Render(glyph+" ") + a.Assertion.Raw
		if !a.Pass {
			line += m.styles.dim.Render(fmt.Sprintf("  (got %q)", a.Got))
		}
		b.WriteString(truncate(line, m.viewport.Width) + "\n")
	}
	return b.String()
}

// cachedBody returns the syntax-highlighted response body for step i, reusing
// the value computed when the result arrived and only re-highlighting if the
// cache is cold (e.g. a model built directly in tests).
func (m Model) cachedBody(i int, r step.Result) string {
	if i < len(m.bodyView) && m.bodyView[i] != "" {
		return m.bodyView[i]
	}
	return highlightJSON(r.Body, jsonTheme)
}

// assertSummary is the aggregate pass/fail badge shown in the status bar. It is
// rendered on the bar's background so it blends into the strip.
func (m Model) assertSummary() string {
	var pass, fail int
	for _, r := range m.results {
		for _, a := range r.Asserts {
			if a.Pass {
				pass++
			} else {
				fail++
			}
		}
	}
	if pass+fail == 0 {
		return ""
	}
	out := lipgloss.NewStyle().Background(palette.crust).Foreground(palette.success).
		Render(fmt.Sprintf("✓%d", pass))
	if fail > 0 {
		out += lipgloss.NewStyle().Background(palette.crust).Foreground(palette.danger).
			Render(fmt.Sprintf(" ✗%d", fail))
	}
	return out
}

// capturedLines renders the variables a step captured from its response.
func (m Model) capturedLines(s step.Step, r step.Result) string {
	if len(s.Captures) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n" + m.styles.paneHeader.Render("CAPTURED") + "\n")
	for _, c := range s.Captures {
		val, ok := capture.Eval(c.Expr, r)
		if !ok {
			b.WriteString(lipgloss.NewStyle().Foreground(palette.danger).
				Render(fmt.Sprintf("%s ← %s (no match)", c.Name, c.Expr)) + "\n")
			continue
		}
		b.WriteString(m.styles.method.Foreground(palette.success).Render(c.Name) +
			m.styles.dim.Render(" = ") + truncate(val, m.viewport.Width-len(c.Name)-4) + "\n")
	}
	return b.String()
}

// responseSummary is the bold one-liner above the body: method + status (with
// its reason phrase) for HTTP, or the exit code for shell, plus the duration.
func (m Model) responseSummary(i int) string {
	s := m.steps[i]
	r := m.results[i]
	dur := m.styles.dim.Render("  ·  " + r.Duration.Round(time.Millisecond).String())

	if s.Kind == step.KindHTTP {
		label := fmt.Sprintf("%s  →  %d", s.Method, r.StatusCode)
		if text := http.StatusText(r.StatusCode); text != "" {
			label += " " + text
		}
		return lipgloss.NewStyle().Foreground(statusColor(r.StatusCode, true)).Bold(true).
			Render(label) + dur
	}
	return lipgloss.NewStyle().Foreground(statusColor(r.ExitCode, false)).Bold(true).
		Render(fmt.Sprintf("exit %d", r.ExitCode)) + dur
}
