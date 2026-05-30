package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// view.go holds the window chrome: the root View, the status bar and its
// segments, the transient notice line, and the modal environment picker. The two
// panes it frames are rendered in view_list.go (the step list) and
// view_result.go (the response); layout.go sizes them.

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
	if m.notice != "" {
		footer = m.noticeLine() + "\n" + footer
	}

	return lipgloss.JoinVertical(lipgloss.Left, m.statusBar(), body, footer)
}

// noticeLine renders the transient diagnostic above the footer, truncated so it
// never wraps and pushes the status bar off-screen. A confirmation (noticeOK,
// e.g. a clipboard copy) reads green with a ✓; everything else is an amber ⚠.
func (m Model) noticeLine() string {
	glyph, colour := "⚠ ", palette.warning
	if m.noticeOK {
		glyph, colour = "✓ ", palette.success
	}
	return lipgloss.NewStyle().Foreground(colour).
		Render(truncate(glyph+m.notice, m.width))
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
	if len(m.plan.Steps) > 0 {
		right += bar.Render(fmt.Sprintf("   %d/%d", m.cursor+1, len(m.plan.Steps)))
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

// assertSummary is the aggregate pass/fail badge shown in the status bar. It is
// rendered on the bar's background so it blends into the strip.
func (m Model) assertSummary() string {
	var pass, fail int
	for _, r := range m.plan.Results {
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
