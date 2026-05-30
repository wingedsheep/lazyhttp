package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/wingedsheep/lazyhttp/internal/capture"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

// refreshResult renders the selected step's request/response into the viewport.
func (m *Model) refreshResult() {
	if len(m.plan.Steps) == 0 {
		m.viewport.SetContent("")
		return
	}
	// When the live-stream step is what's on screen, recompute its cached prefix
	// (a width change or `i` toggle may have changed it) so the chunk re-renders
	// that follow can reuse it without re-expanding the step.
	if m.cursor == m.streamIndex && m.streamIndex < len(m.plan.Results) &&
		m.plan.Results[m.streamIndex].Status == step.Running {
		m.refreshStreamHead()
	}
	m.viewport.SetContent(m.formatResult(m.cursor))
	m.viewport.GotoTop()
}

// refreshStreamHead recomputes streamHead — the request-preview prefix shown
// above a live stream's body. The request can't change mid-stream, so chunk
// re-renders reuse the cached value rather than re-expanding the step (and
// re-reading any `< file` body) on every chunk.
func (m *Model) refreshStreamHead() {
	if !m.showRequest {
		m.streamHead = ""
		return
	}
	s, expandErr := m.plan.Expand(m.plan.Steps[m.streamIndex])
	m.streamHead = m.requestPreview(s, expandErr)
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

// requestOpts renders the per-request directives (`# @timeout`, `# @no-redirect`)
// as a single dim line for the request preview, or "" when neither is set.
func requestOpts(s step.Step) string {
	var opts []string
	if s.Timeout > 0 {
		opts = append(opts, "timeout "+s.Timeout.String())
	}
	if s.NoRedirect {
		opts = append(opts, "no-redirect")
	}
	switch {
	case s.StreamThrough != "":
		opts = append(opts, "stream-through "+s.StreamThrough)
	case s.StreamExtract != "":
		opts = append(opts, "stream "+s.StreamExtract)
	case s.Stream:
		opts = append(opts, "stream")
	}
	if len(opts) == 0 {
		return ""
	}
	return "⚙ " + strings.Join(opts, " · ")
}

// sortedKeys returns a map's keys in alphabetical order. Headers live in maps
// (request: map[string]string, response: http.Header), whose iteration order is
// randomised, so the request preview and response headers shuffled on every
// re-render. Sorting gives a stable, scannable display.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// requestPreview renders the method/URL/headers/body block shown above the
// response when the request preview is toggled on (`i`). s is the already-
// expanded step; expandErr is a failed `< file` body read, reported inline.
func (m Model) requestPreview(s step.Step, expandErr error) string {
	var b strings.Builder
	if s.Kind == step.KindShell {
		b.WriteString(m.styles.dim.Render("$ shell") + "\n")
		b.WriteString(s.Body + "\n")
	} else {
		b.WriteString(m.styles.method.Foreground(palette.accent).
			Render(s.Method) + " " + s.URL + "\n")
		for _, k := range sortedKeys(s.Headers) {
			b.WriteString(m.styles.dim.Render(k+": "+s.Headers[k]) + "\n")
		}
		if opts := requestOpts(s); opts != "" {
			b.WriteString(m.styles.dim.Render(opts) + "\n")
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
				b.WriteString(m.highlightRequestBody(s.Body) + "\n")
			}
		case s.Body != "":
			b.WriteString("\n" + m.highlightRequestBody(s.Body) + "\n")
		}
	}
	b.WriteString(m.styles.dim.Render(strings.Repeat("─", min(m.viewport.Width, 40))) + "\n")
	return b.String()
}

// streamView renders the live `# @stream` response for the active stream: the
// cached request-preview prefix (streamHead) followed by a live indicator and the
// body accumulated in streamBody. It deliberately bypasses formatResult's
// per-call Expand (and its `< file` disk read) — over a long token stream that
// would run once per chunk to rebuild output that, apart from the appended slice,
// never changes. The chunk handler pins the viewport to the bottom so new text
// scrolls into view; raw, not highlighted, since a partial body isn't valid JSON
// and SSE/NDJSON framing should read as-is (the terminal result re-highlights).
func (m Model) streamView() string {
	var b strings.Builder
	b.WriteString(m.streamHead)
	if m.streamBody.Len() > 0 {
		b.WriteString(m.spinner.View() + m.styles.dim.Render(" streaming…") + "\n\n")
		b.WriteString(m.streamBody.String())
	} else {
		b.WriteString(m.spinner.View() + m.styles.dim.Render(" running…"))
	}
	return b.String()
}

// formatResult builds the full request+response text for step i, with all
// {{vars}} expanded against the current variable set so the preview matches
// what will actually run.
func (m Model) formatResult(i int) string {
	// A `# @stream` step in flight renders through streamView, which reuses the
	// cached request preview and the streamBody builder instead of expanding the
	// step and rebuilding the body on every chunk.
	if i == m.streamIndex && i < len(m.plan.Results) &&
		m.plan.Results[i].Status == step.Running {
		return m.streamView()
	}

	// expand may fail to read a `< file` body; the preview still shows the
	// request line and the file reference, with the error noted below.
	s, expandErr := m.plan.Expand(m.plan.Steps[i])
	r := m.plan.Results[i]
	var b strings.Builder

	// Request preview — optional, toggled with `i`. Off by default so the
	// response output gets the full pane.
	if m.showRequest {
		b.WriteString(m.requestPreview(s, expandErr))
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
		// A non-streaming request in flight; a live `# @stream` step is handled
		// above by streamView.
		b.WriteString(m.spinner.View() + m.styles.dim.Render(" running…"))
	default:
		b.WriteString(m.responseSummary(i) + "\n")
		if r.Err != nil {
			b.WriteString(lipgloss.NewStyle().Foreground(palette.danger).
				Render(r.Err.Error()))
			break
		}
		// Response headers are detail, hidden unless toggled with `h`.
		if m.showHeaders && s.Kind == step.KindHTTP {
			for _, k := range sortedKeys(r.Header) {
				b.WriteString(m.styles.dim.Render(k+": "+strings.Join(r.Header[k], ", ")) + "\n")
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
			note := fmt.Sprintf("got %q", a.Got)
			if a.Detail != "" {
				note = a.Detail // a clearer reason than the bare value
			}
			line += m.styles.dim.Render("  (" + note + ")")
		}
		b.WriteString(truncate(line, m.viewport.Width) + "\n")
	}
	return b.String()
}

// highlightRequestBody returns the request-preview body syntax-highlighted,
// served from the per-body memo when one is present. Bare Models built directly
// in tests have no memo and highlight directly — the cache is a pure speedup.
func (m Model) highlightRequestBody(body string) string {
	if m.reqHL == nil {
		return highlightJSON(body, jsonTheme)
	}
	return m.reqHL.render(body)
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

// capturedLines renders the variables a step captured from its response.
func (m Model) capturedLines(s step.Step, r step.Result) string {
	if len(s.Captures) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n" + m.styles.paneHeader.Render("CAPTURED") + "\n")
	eval := capture.For(r)
	for _, c := range s.Captures {
		val, ok := eval.Eval(c.Expr)
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
	s := m.plan.Steps[i]
	r := m.plan.Results[i]
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
