package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/clipboard"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

// copiedMsg reports the outcome of a clipboard copy so onResult-style handling
// can show a confirmation (or the failure) in the notice line.
type copiedMsg struct {
	label string // what was copied, e.g. "response body (1.2 KB)"
	err   error
}

// errNotRun / errNothingToCopy back the copy notices for steps with no output.
var (
	errNotRun        = fmt.Errorf("step not run — nothing to copy")
	errNothingToCopy = fmt.Errorf("no output to copy")
)

// copyResult copies the selected step's output to the system clipboard: the raw
// response body when full is false (the common case — grab the JSON), or the
// whole response pane (ANSI stripped) when full is true. It returns a command so
// the clipboard tool runs off the UI thread. A step that hasn't run, or an empty
// body, yields a notice instead of an empty copy.
func (m Model) copyResult(full bool) tea.Cmd {
	if m.cursor >= len(m.plan.Results) {
		return nil
	}
	r := m.plan.Results[m.cursor]
	if r.Status != step.Done && r.Status != step.Failed {
		return func() tea.Msg { return copiedMsg{err: errNotRun} }
	}

	text, what := r.Body, "response body"
	if full {
		text, what = stripANSI(m.formatResult(m.cursor)), "response pane"
	}
	if text == "" {
		return func() tea.Msg { return copiedMsg{err: errNothingToCopy} }
	}
	label := fmt.Sprintf("%s (%s)", what, humanBytes(len(text)))
	return func() tea.Msg {
		return copiedMsg{label: label, err: clipboard.Copy(text)}
	}
}

// onCopied turns a finished copy into a notice — green confirmation on success,
// amber warning on failure. The "nothing to copy" sentinels read as-is; only a
// real clipboard-tool failure gets the "copy failed" prefix.
func (m Model) onCopied(msg copiedMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.err == errNotRun || msg.err == errNothingToCopy:
		m.setNotice(msg.err.Error(), false)
	case msg.err != nil:
		m.setNotice("copy failed: "+msg.err.Error(), false)
	default:
		m.setNotice("copied "+msg.label, true)
	}
	return m, nil
}

// setNotice sets the transient footer notice (ok = green confirmation, else
// amber warning) and re-lays the panes, since the notice claims a footer row.
// It is the shared entry point for every action that wants to speak up — copy,
// the env picker, and the load-time diagnostics.
func (m *Model) setNotice(text string, ok bool) {
	m.notice = text
	m.noticeOK = ok
	m.layout()
}

// humanBytes renders a byte count as a compact size for the copy confirmation.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
