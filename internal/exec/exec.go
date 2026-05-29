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

// AuthResolver substitutes OAuth2 token placeholders in a step just before it
// is sent. It is consulted off the UI thread (a token fetch is a network call),
// so a slow token endpoint never blocks rendering. A nil resolver is a no-op.
type AuthResolver interface {
	Resolve(s *step.Step) error
}

// Run dispatches a step to the appropriate runner. The work — resolving any auth
// token, the request (or shell), then highlighting the response body with
// highlight — all happens off the UI thread inside the returned command, so the
// interface never blocks even on a large response or a slow token endpoint.
// auth may be nil (no OAuth2 helper); highlight may be nil to skip highlighting.
func Run(index int, s step.Step, auth AuthResolver, highlight func(string) string) tea.Cmd {
	var cmd tea.Cmd
	if s.Kind == step.KindShell {
		cmd = runShell(index, s)
	} else {
		cmd = runHTTP(index, s, auth)
	}
	return func() tea.Msg {
		msg := cmd().(ResultMsg)
		if highlight != nil {
			msg.Highlighted = highlight(msg.Result.Body)
		}
		return msg
	}
}
