// Package exec runs steps as Bubble Tea commands. Each runner returns a
// tea.Cmd that performs the work off the UI thread and delivers a ResultMsg
// when it finishes, so the interface never blocks.
package exec

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// ResultMsg carries the outcome of executing the step at Index back to the UI.
type ResultMsg struct {
	Index  int
	Result step.Result
}

// Run dispatches a step to the appropriate runner.
func Run(index int, s step.Step) tea.Cmd {
	if s.Kind == step.KindShell {
		return runShell(index, s)
	}
	return runHTTP(index, s)
}
