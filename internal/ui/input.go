package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// wheelStep is how many wheel events make up one cursor step. Most terminals
// fire ~3 events per notch, so this maps roughly one notch to one step.
const wheelStep = 3

// wheelScroll turns a mouse-wheel event into cursor movement, accumulating
// sub-notch events in *accum so one physical notch (≈wheelStep events) moves the
// cursor by exactly one step. A reversal drops any leftover from the other
// direction so the next notch counts cleanly. move is called once per step with
// -1 (up) or +1 (down). Non-wheel buttons are ignored. Shared by the step list
// and the folder browser, which scroll identically.
func wheelScroll(button tea.MouseButton, accum *int, move func(int)) {
	switch button {
	case tea.MouseButtonWheelUp:
		if *accum > 0 {
			*accum = 0
		}
		*accum--
	case tea.MouseButtonWheelDown:
		if *accum < 0 {
			*accum = 0
		}
		*accum++
	default:
		return
	}
	for *accum <= -wheelStep {
		move(-1)
		*accum += wheelStep
	}
	for *accum >= wheelStep {
		move(1)
		*accum -= wheelStep
	}
}

// onMouse routes mouse input. A left click selects the pane under the cursor
// and, on a step row in the list, runs that step. The scroll wheel scrolls the
// response body when that pane is focused, otherwise it moves through the list.
func (m Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		if m.envPicking || m.filtering {
			return m, nil // a modal owns the screen; ignore stray clicks
		}
		// The list pane occupies the leftmost listW+4 columns (content + padding +
		// border); anything to the right is the result pane.
		if msg.X >= m.listW+4 {
			m.focus = focusResult
			return m, nil
		}
		m.focus = focusList
		if i, ok := m.stepAtRow(msg.Y); ok {
			m.setCursor(i)
			return m, m.run(i)
		}
		return m, nil
	}

	if m.focus == focusResult {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	// Otherwise the wheel moves the list cursor, one step per physical notch.
	wheelScroll(msg.Button, &m.wheelAccum, m.moveCursor)
	return m, nil
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While the env picker is open it owns the keyboard until a choice is made
	// or it's dismissed, so every other binding is bypassed.
	if m.envPicking {
		return m.envKey(msg)
	}

	// While the filter is being typed, keystrokes edit the query (except a few
	// that navigate or dismiss it), so list/run bindings are bypassed.
	if m.filtering {
		return m.filterKey(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	// Esc clears an applied filter when one is active.
	case msg.Type == tea.KeyEsc:
		if m.filter != "" {
			m.filter = ""
			m.refilter()
			m.snapCursor()
			m.refreshResult()
		}
		return m, nil

	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.layout()
		m.refreshResult()
		return m, nil

	case key.Matches(msg, m.keys.Focus):
		m.toggleFocus()
		return m, nil

	case key.Matches(msg, m.keys.Left):
		m.focus = focusList
		return m, nil

	case key.Matches(msg, m.keys.Right):
		m.focus = focusResult
		return m, nil

	case key.Matches(msg, m.keys.Reload):
		m.load()
		m.refreshResult()
		return m, nil

	case key.Matches(msg, m.keys.Stop):
		// Stop a live stream early but keep what has arrived; a no-op when nothing
		// is streaming. Handled here (not in listKey) so it works with either pane
		// focused — the user is usually watching the output.
		m.stopStream()
		return m, nil

	case key.Matches(msg, m.keys.Request):
		m.showRequest = !m.showRequest
		m.keys.requestOn = m.showRequest
		m.refreshResult()
		return m, nil

	case key.Matches(msg, m.keys.Headers):
		m.showHeaders = !m.showHeaders
		m.keys.headersOn = m.showHeaders
		m.refreshResult()
		return m, nil

	case key.Matches(msg, m.keys.Theme):
		m.cycleTheme()
		return m, nil

	case key.Matches(msg, m.keys.Env):
		// Open the picker when there's something to choose from; with no env file
		// explain why (where we searched, or the parse error) instead of no-op'ing.
		if len(m.envNames) > 0 {
			m.envPicking = true
			// Open on the current env; -1 (not in the list) falls back to the
			// first option, the "(none)" entry.
			m.envCursor = max(indexOf(m.envOptions(), m.envName), 0)
		} else {
			m.setNotice(m.envDisc.Summary(), false)
		}
		return m, nil

	case key.Matches(msg, m.keys.Copy):
		return m, m.copyResult(false)

	case key.Matches(msg, m.keys.CopyAll):
		return m, m.copyResult(true)
	}

	// When the result pane is focused, motion keys scroll the body instead.
	if m.focus == focusResult {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m.listKey(msg)
}

// listKey handles navigation and execution while the step list is focused.
func (m Model) listKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.moveCursor(-1)
	case key.Matches(msg, m.keys.Down):
		m.moveCursor(1)
	case key.Matches(msg, m.keys.HalfUp):
		m.moveCursor(-m.pageStep())
	case key.Matches(msg, m.keys.HalfDn):
		m.moveCursor(m.pageStep())
	case key.Matches(msg, m.keys.Top):
		m.setTop()
	case key.Matches(msg, m.keys.Bottom):
		m.setBottom()
	case key.Matches(msg, m.keys.Clear):
		if m.cursor < len(m.plan.Results) {
			// Clearing a still-streaming step doubles as "stop": cancel the
			// request so the cleared result isn't resurrected by late chunks.
			if m.plan.Results[m.cursor].Status == step.Running {
				m.cancelStream()
			}
			m.plan.Results[m.cursor] = step.Result{}
			if m.cursor < len(m.bodyView) {
				m.bodyView[m.cursor] = ""
			}
			m.refreshResult()
		}
	case key.Matches(msg, m.keys.ClearAll):
		m.resetState(-1)
		m.refreshResult()
	case key.Matches(msg, m.keys.Filter):
		m.filtering = true
		return m, nil
	case key.Matches(msg, m.keys.Run):
		return m, m.run(m.cursor)
	case key.Matches(msg, m.keys.RunAll):
		m.runFrom = m.cursor
		return m, m.run(m.cursor)
	}
	return m, nil
}

// filterKey edits the live filter query: most keys append/erase characters,
// while Esc clears it, Enter applies it, and the arrows still move the cursor
// through the matches so you can type-then-pick in one motion.
func (m Model) filterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.filtering = false
		m.filter = ""
		m.refilter()
		m.snapCursor()
		m.refreshResult()
		return m, nil
	case tea.KeyEnter:
		m.filtering = false // keep the query; just leave edit mode
		return m, nil
	case tea.KeyUp:
		m.moveCursor(-1)
		return m, nil
	case tea.KeyDown:
		m.moveCursor(1)
		return m, nil
	case tea.KeyBackspace:
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		m.filter += " "
	case tea.KeyRunes:
		m.filter += string(msg.Runes)
	default:
		return m, nil
	}
	m.refilter()
	m.snapCursor()
	m.refreshResult()
	return m, nil
}

// moveCursor steps delta positions through the currently visible (filtered)
// steps, so navigation skips anything the filter has hidden.
func (m *Model) moveCursor(delta int) {
	vis := m.visible()
	if len(vis) == 0 {
		return
	}
	pos := 0
	for i, idx := range vis {
		if idx == m.cursor {
			pos = i
			break
		}
	}
	m.setCursor(vis[clamp(pos+delta, 0, len(vis)-1)])
}

// setTop / setBottom jump to the first / last visible step.
func (m *Model) setTop() {
	if vis := m.visible(); len(vis) > 0 {
		m.setCursor(vis[0])
	}
}

func (m *Model) setBottom() {
	if vis := m.visible(); len(vis) > 0 {
		m.setCursor(vis[len(vis)-1])
	}
}

func (m *Model) setCursor(i int) {
	if len(m.plan.Steps) == 0 {
		return
	}
	m.cursor = min(max(i, 0), len(m.plan.Steps)-1)
	m.refreshResult()
}

// visible returns the absolute indices of steps that pass the active filter, in
// order. It reads the cache refilter() maintains; with no filter every step is
// visible.
func (m Model) visible() []int {
	return m.vis
}

// refilter rebuilds the cached visible-step set. Call it whenever the filter or
// the display names change (see refreshLabels and the filter keys).
func (m *Model) refilter() {
	m.vis = m.vis[:0]
	q := strings.ToLower(m.filter)
	for i, s := range m.plan.Steps {
		if q == "" {
			m.vis = append(m.vis, i)
			continue
		}
		hay := strings.ToLower(s.Method + " " + m.names[i] + " " + s.Group)
		if strings.Contains(hay, q) {
			m.vis = append(m.vis, i)
		}
	}
}

// snapCursor keeps the cursor on a visible step after the filter changes,
// jumping to the first match when the current step has been hidden.
func (m *Model) snapCursor() {
	vis := m.visible()
	if len(vis) == 0 {
		return
	}
	for _, idx := range vis {
		if idx == m.cursor {
			return
		}
	}
	m.cursor = vis[0]
}

func (m *Model) toggleFocus() {
	if m.focus == focusList {
		m.focus = focusResult
	} else {
		m.focus = focusList
	}
}

// pageStep is the half-page jump distance for ctrl+d / ctrl+u.
func (m Model) pageStep() int {
	return max(1, (m.height-6)/2)
}
