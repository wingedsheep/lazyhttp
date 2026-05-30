package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// envNotice builds the load-time diagnostic for the env line: a parse error
// (worth surfacing whether or not an env was requested), or a requested --env
// that isn't available — naming the alternatives, or explaining where discovery
// looked when none turned up. It returns "" when there's nothing to report.
func (m Model) envNotice() string {
	if m.envDisc.Err != nil {
		return m.envDisc.Summary()
	}
	if m.envName == "" || contains(m.envNames, m.envName) {
		return "" // no env requested, or the requested one resolved
	}
	if len(m.envNames) == 0 {
		// Nothing to fall back to — name the requested env, then say why discovery
		// came up empty (search path, or a parse error).
		return fmt.Sprintf("env %q unavailable: %s", m.envName, m.envDisc.Summary())
	}
	return fmt.Sprintf("env %q not found — available: %s", m.envName, strings.Join(m.envNames, ", "))
}

// envKey drives the environment picker: the motion keys move the highlight,
// Enter switches to the chosen environment (reloading the plan against its
// variables), and Esc dismisses it without changing anything.
func (m Model) envKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	opts := m.envOptions()
	switch {
	case msg.Type == tea.KeyCtrlC:
		return m, tea.Quit
	case msg.Type == tea.KeyEsc:
		m.envPicking = false
		return m, nil
	case key.Matches(msg, m.keys.Up):
		m.envCursor = clamp(m.envCursor-1, 0, len(opts)-1)
	case key.Matches(msg, m.keys.Down):
		m.envCursor = clamp(m.envCursor+1, 0, len(opts)-1)
	case key.Matches(msg, m.keys.Run):
		m.envPicking = false
		if name := opts[m.envCursor]; name != m.envName {
			// New environment → new variable set, so reload from scratch. This
			// drops captured values and prior results, which would be stale
			// against the new env anyway.
			m.envName = name
			m.load()
			m.refreshResult()
		}
	}
	return m, nil
}

// envOptions is the picker's selectable list: an explicit "no environment"
// entry (the empty string, rendered as "(none)") followed by every declared
// environment, so the user can fall back to inline-only variables.
func (m Model) envOptions() []string {
	return append([]string{""}, m.envNames...)
}

// contains reports whether s is one of names.
func contains(names []string, s string) bool {
	for _, n := range names {
		if n == s {
			return true
		}
	}
	return false
}

// indexOf returns the position of s in names, or 0 when it isn't present so the
// picker opens on a sensible default.
func indexOf(names []string, s string) int {
	for i, n := range names {
		if n == s {
			return i
		}
	}
	return 0
}
