// Package exec runs steps as Bubble Tea commands. Each runner returns a
// tea.Cmd that performs the work off the UI thread and delivers a ResultMsg
// when it finishes, so the interface never blocks.
package exec

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// ResultMsg carries the outcome of executing the step at Index back to the UI.
// Highlighted is Result.Body run through the highlight func passed to Run,
// produced off the UI thread so a large response never blocks rendering.
type ResultMsg struct {
	Index       int
	Result      step.Result
	Highlighted string
}

// Run dispatches a step to the appropriate runner. The work — the request (or
// shell) and then highlighting the response body with highlight — all happens
// off the UI thread inside the returned command, so the interface never blocks
// even on a large response. highlight may be nil to skip highlighting.
func Run(index int, s step.Step, highlight func(string) string) tea.Cmd {
	inner := runHTTP
	if s.Kind == step.KindShell {
		inner = runShell
	}
	cmd := inner(index, s)
	return func() tea.Msg {
		msg := cmd().(ResultMsg)
		if highlight != nil {
			msg.Highlighted = highlight(msg.Result.Body)
		}
		return msg
	}
}
