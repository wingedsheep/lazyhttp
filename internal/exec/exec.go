// Package exec runs steps. Do executes a step synchronously and returns its
// Result; Run wraps Do as a Bubble Tea command that performs the work off the
// UI thread and delivers a ResultMsg when it finishes, so the interface never
// blocks.
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

// Do executes a single step synchronously and returns its Result: an HTTP
// request (resolving any {{$auth.token(...)}} placeholders via auth first — nil
// for none) or a shell command. This is the UI-independent execution entry
// point; Run wraps it as a tea.Cmd for the TUI, while a headless runner calls
// it directly.
func Do(s step.Step, auth AuthResolver) step.Result {
	if s.Kind == step.KindShell {
		return doShell(s)
	}
	return doHTTP(s, auth)
}

// Run dispatches a step to Do off the UI thread, then highlights the response
// body with highlight (nil to skip) — all inside the returned command, so the
// interface never blocks even on a large response or a slow token endpoint.
func Run(index int, s step.Step, auth AuthResolver, highlight func(string) string) tea.Cmd {
	return func() tea.Msg {
		res := Do(s, auth)
		msg := ResultMsg{Index: index, Result: res}
		if highlight != nil {
			msg.Highlighted = highlight(res.Body)
		}
		return msg
	}
}
