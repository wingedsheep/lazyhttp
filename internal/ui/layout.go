package ui

import "strings"

// layout sizes the panes and the result viewport for the current window. It is
// called on resize and whenever the footer height changes (help toggle).
func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	m.help.Width = m.width
	footerH := strings.Count(m.help.View(m.keys), "\n") + 1
	if m.notice != "" {
		footerH++ // the notice line sits above the help footer
	}

	// Content height inside the pane borders.
	contentH := m.height - 1 /*title*/ - footerH - 2 /*pane borders*/
	contentH = max(contentH, 3)

	// Give the step list ~50% of the width (more room for long descriptions),
	// but keep it within sensible bounds so the result pane stays usable.
	listW := clamp(m.width*50/100-4, 28, 92)
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
