package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/wingedsheep/lazyhttp/internal/clipboard"
	"github.com/wingedsheep/lazyhttp/internal/exec"
	"github.com/wingedsheep/lazyhttp/internal/httpfile"
	"github.com/wingedsheep/lazyhttp/internal/runner"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

type focus int

const (
	focusList focus = iota
	focusResult
)

// Model is the root Bubble Tea model: it drives a runner.Plan (the parsed steps,
// per-step results, and the variable lifecycle) and the widgets that render it.
type Model struct {
	path    string
	envName string

	// plan is the execution engine: parsed steps, per-step results, and the
	// variable lifecycle (expand/evaluate/reset). The Model is a consumer of it —
	// it renders the plan's state and drives it one step at a time. It is never
	// nil (New seeds an empty Plan) so a load error stays renderable.
	plan *runner.Plan

	cursor int
	focus  focus

	// names holds each step's display name with {{vars}} already expanded, and
	// bodyView holds each response body already syntax-highlighted. Both are
	// rebuilt only when the data changes (load, a result, a reset) so list
	// navigation and redraws stay allocation-light.
	names    []string
	bodyView []string

	// spinning is true while a spinner-tick loop is in flight. It lets us drive
	// the spinner only while a step runs and stay completely idle otherwise.
	spinning bool

	// streamSub is the live subscription for a `# @stream` step in flight, or nil
	// when nothing is streaming. The model holds it so a disruptive action
	// (reload, clear, env switch) can Cancel the request mid-stream.
	streamSub *exec.StreamSub

	// showRequest toggles the request preview (method/URL/headers/body) at the
	// top of the right pane; showHeaders toggles the response headers above the
	// body. Both off by default so the response body gets the whole pane.
	showRequest bool
	showHeaders bool

	// filter narrows the visible step list to those matching a case-insensitive
	// substring of "method name group"; filtering is true while it's being typed.
	filter    string
	filtering bool

	// envNames lists the environments declared in http-client.env.json (sorted);
	// envPicking is true while the env picker overlay is open and envCursor marks
	// the highlighted entry. Switching env reloads the plan against the new vars.
	// envDisc holds the full discovery outcome (searched dirs, file, parse error)
	// so the UI can explain an empty list instead of no-op'ing.
	envNames   []string
	envDisc    httpfile.EnvDiscovery
	envPicking bool
	envCursor  int

	// notice is a transient one-line diagnostic shown above the footer — used to
	// explain env discovery (empty list, parse error, an unresolved --env) and to
	// confirm a clipboard copy. It is recomputed on every (re)load and may be
	// overwritten by a key action (E, y/Y). noticeOK flips it from a warning
	// (⚠, amber) to a confirmation (✓, green) for success messages like a copy.
	notice   string
	noticeOK bool

	// runFrom >= 0 means a "run from here" chain is active; it stops on the
	// first failure or the end of the plan.
	runFrom int

	viewport viewport.Model
	spinner  spinner.Model
	help     help.Model
	keys     keyMap
	styles   styles

	width, height            int
	listW, resultW, contentH int
	loadErr                  error

	// wheelAccum smooths the scroll wheel: terminals emit several wheel events
	// per physical notch, so we accumulate them and only step the cursor once
	// |wheelAccum| reaches wheelStep. This keeps list navigation precise.
	wheelAccum int
}

// wheelStep is how many wheel events make up one cursor step. Most terminals
// fire ~3 events per notch, so this maps roughly one notch to one step.
const wheelStep = 3

// New builds a Model by loading and parsing the plan at path with the named
// environment (envName may be "").
func New(path, envName string) Model {
	sp := spinner.New()
	// MiniDot is a single-cell glyph; spinner.Dot's frames carry a trailing space
	// (width 2) that overflows the fixed glyph column and wraps the row downward.
	// The list already adds its own space after the glyph, so MiniDot lines up.
	sp.Spinner = spinner.MiniDot

	m := Model{
		path:    path,
		envName: envName,
		// An empty (non-nil) plan keeps the model renderable if load fails before
		// it can install the real one.
		plan:     &runner.Plan{},
		runFrom:  -1,
		viewport: viewport.New(0, 0),
		spinner:  sp,
		help:     help.New(),
		keys:     newKeyMap(),
	}
	m.applyStyles()
	m.load()
	return m
}

// applyStyles (re)builds every style derived from the active palette: the
// reusable style set plus the spinner and help widgets, which carry their own
// colours. It runs at construction and again whenever the theme changes.
func (m *Model) applyStyles() {
	m.spinner.Style = lipgloss.NewStyle().Foreground(palette.warning)
	m.help.Styles.ShortKey = lipgloss.NewStyle().Foreground(palette.accent)
	m.help.Styles.FullKey = m.help.Styles.ShortKey
	m.help.Styles.ShortDesc = lipgloss.NewStyle().Foreground(palette.subtle)
	m.help.Styles.FullDesc = m.help.Styles.ShortDesc
	m.help.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(palette.border)
	m.help.Styles.FullSeparator = m.help.Styles.ShortSeparator
	m.styles = newStyles()
}

// cycleTheme advances to the next colour theme, rebuilding the cached styles and
// re-highlighting any response bodies so the whole UI recolours at once.
func (m *Model) cycleTheme() {
	applyTheme(activeTheme + 1)
	m.applyStyles()
	for i := range m.plan.Results {
		if i < len(m.bodyView) && m.bodyView[i] != "" {
			m.bodyView[i] = highlightJSON(m.plan.Results[i].Body, jsonTheme)
		}
	}
	m.refreshResult()
}

// load (re)reads and parses the plan, resetting results.
func (m *Model) load() {
	// A reload or env switch replaces the plan wholesale, so abort any stream in
	// flight first — its result would land on a step that no longer exists.
	m.cancelStream()
	// Discover environments up front, keeping the full outcome (not just the
	// names): a parse error or an empty result is then explained to the user via
	// the notice line rather than silently leaving the picker blank.
	m.envDisc = httpfile.DiscoverEnv(m.path)
	m.envNames = m.envDisc.Names
	m.notice = m.envNotice()
	m.noticeOK = false // (re)load notices are diagnostics, not confirmations
	p, err := runner.Load(m.path, m.envName)
	if err != nil {
		m.loadErr = err
		return
	}
	m.loadErr = nil
	m.plan = p
	// Only the TUI may open a browser, so the Authorization Code grant's
	// interactive sign-in runs here (the headless runner leaves it off).
	m.plan.AuthCache.SetInteractive(true)
	m.bodyView = make([]string, len(p.Steps))
	m.refreshLabels()
	if m.cursor >= len(p.Steps) {
		m.cursor = max(0, len(p.Steps)-1)
	}
	// The notice occupies a footer row; re-lay so the panes don't overflow the
	// terminal (a no-op before the first WindowSizeMsg, when width is still 0).
	m.layout()
}

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

// Init starts idle: the spinner only ticks once a step is running (see run),
// so an untouched UI performs zero redraws.
func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.refreshResult()
		return m, nil

	case spinner.TickMsg:
		// Keep the spinner animating only while something is running; once the
		// last step finishes, let the tick loop lapse so the UI goes idle.
		if !m.anyRunning() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case exec.ResultMsg:
		return m.onResult(msg)

	case exec.StreamStartMsg:
		// The stream is live: hold the handle so a later reload/clear can cancel
		// it, then start pulling chunks.
		m.streamSub = msg.Sub
		return m, exec.WaitForChunk(msg.Sub)

	case exec.StreamChunkMsg:
		return m.onStreamChunk(msg)

	case exec.StreamDoneMsg:
		// A cancelled stream finished draining; the result was already handled by
		// whoever cancelled it, so just drop the subscription.
		if m.streamSub == msg.Sub {
			m.streamSub = nil
		}
		return m, nil

	case copiedMsg:
		return m.onCopied(msg)

	case tea.MouseMsg:
		return m.onMouse(msg)

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
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
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.wheelAccum > 0 {
			m.wheelAccum = 0 // direction flipped; drop leftover from the other way
		}
		m.wheelAccum--
	case tea.MouseButtonWheelDown:
		if m.wheelAccum < 0 {
			m.wheelAccum = 0
		}
		m.wheelAccum++
	default:
		return m, nil
	}
	// Only step once a full notch's worth of events has accumulated, so a
	// single physical scroll tick moves the cursor by one.
	for m.wheelAccum <= -wheelStep {
		m.moveCursor(-1)
		m.wheelAccum += wheelStep
	}
	for m.wheelAccum >= wheelStep {
		m.moveCursor(1)
		m.wheelAccum -= wheelStep
	}
	return m, nil
}

// authWaitNotice is shown while an Authorization Code step waits for the user to
// finish the browser sign-in; onResult clears it once the result arrives.
const authWaitNotice = "Waiting for browser sign-in to complete…"

// onResult stores a finished result and advances a run-from-here chain.
func (m Model) onResult(msg exec.ResultMsg) (tea.Model, tea.Cmd) {
	// A terminal ResultMsg ends any active stream (only one runs at a time), so
	// release the subscription. The StreamDoneMsg path covers cancelled streams.
	m.streamSub = nil
	// Clear the "waiting for sign-in" hint now the step has finished (unless a
	// later action already replaced the notice).
	if m.notice == authWaitNotice {
		m.notice = ""
	}
	if msg.Index < len(m.plan.Results) {
		r := m.plan.Evaluate(msg.Index, msg.Result)
		m.plan.Results[msg.Index] = r
		if msg.Index < len(m.bodyView) {
			// Highlighting was done off the UI thread inside the exec command.
			m.bodyView[msg.Index] = msg.Highlighted
		}
		// Captures from this response may feed later step names; re-expand them.
		m.refreshLabels()
		// A successful @reset step returns the plan to a clean slate: every
		// other step's result is cleared and captured variables are dropped,
		// mirroring the backend reset the step just performed.
		if msg.Index < len(m.plan.Steps) && m.plan.Steps[msg.Index].Reset && r.OK() {
			m.resetState(msg.Index)
		}
	}
	if msg.Index == m.cursor {
		m.refreshResult()
	}

	// Chain: continue to the next step unless this one failed (transport, bad
	// status, or a failed assertion) or we're done.
	if m.runFrom >= 0 && msg.Index == m.runFrom {
		next := msg.Index + 1
		if msg.Index < len(m.plan.Results) && m.plan.Results[msg.Index].OK() && next < len(m.plan.Steps) {
			m.runFrom = next
			return m, m.run(next)
		}
		m.runFrom = -1
	}
	return m, nil
}

// onStreamChunk appends a streamed slice to its step's (still Running) result
// and, when that step is selected, re-renders the response pinned to the bottom
// so the output scrolls as it arrives. It always returns WaitForChunk so the
// pump goroutine keeps draining even after a reset flips the step's status —
// the guard only stops us mutating a result that no longer belongs to the
// stream.
func (m Model) onStreamChunk(msg exec.StreamChunkMsg) (tea.Model, tea.Cmd) {
	if msg.Index < len(m.plan.Results) && m.plan.Results[msg.Index].Status == step.Running {
		r := m.plan.Results[msg.Index]
		r.Body += msg.Data
		m.plan.Results[msg.Index] = r
		if msg.Index < len(m.bodyView) {
			// Raw, not highlighted: a partial body isn't valid JSON, and SSE/NDJSON
			// framing should read as-is. The terminal result re-highlights if JSON.
			m.bodyView[msg.Index] = r.Body
		}
		if msg.Index == m.cursor {
			m.viewport.SetContent(m.formatResult(m.cursor))
			m.viewport.GotoBottom()
		}
	}
	return m, exec.WaitForChunk(msg.Sub)
}

// cancelStream aborts an in-flight `# @stream` request, if any. The pump
// goroutine still drains to completion (WaitForChunk keeps reading after a
// cancel), so this never leaks; it just stops the network read and tells the
// wait loop to drop whatever is left. Called before any action that invalidates
// the running step's result: reload, clear, env switch.
func (m *Model) cancelStream() {
	if m.streamSub != nil {
		m.streamSub.Cancel()
		m.streamSub = nil
	}
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
			m.envCursor = indexOf(m.envOptions(), m.envName)
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
	m.snapCursor()
	m.refreshResult()
	return m, nil
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

// run marks a step running and returns the command that executes it, with all
// {{vars}} expanded against the current variable set (including captures).
func (m *Model) run(i int) tea.Cmd {
	if i < 0 || i >= len(m.plan.Steps) {
		return nil
	}
	// One step runs at a time. The off-thread design (and the run-from-here
	// chain, which fires the next step only after the previous result arrives)
	// relies on this: two concurrent goroutines would race the shared auth cache
	// and interleave captures into the var map. Refuse to start a second run
	// — and, crucially, never start a second stream — while one is in flight.
	if m.anyRunning() {
		return nil
	}
	// Resolve {{vars}} (and any `< file` body) up front. A read failure fails the
	// step immediately — mirroring a transport error — rather than sending an
	// empty-bodied request, so the silent-drop footgun becomes a visible error.
	s, err := m.plan.Expand(m.plan.Steps[i])
	if err != nil {
		return func() tea.Msg {
			return exec.ResultMsg{Index: i, Result: step.Result{Status: step.Failed, Err: err}}
		}
	}
	// A variable that never resolved would be sent literally (a URL like
	// "{{api}}/login" fails with a bare "unsupported protocol scheme"). Fail the
	// step with a clear, env-aware message instead — the most common cause is no
	// environment selected, so point at E.
	if s.Kind == step.KindHTTP {
		if missing := runner.Unresolved(s); len(missing) > 0 {
			err := runner.UnresolvedError(missing, m.unresolvedHint())
			return func() tea.Msg {
				return exec.ResultMsg{Index: i, Result: step.Result{Status: step.Failed, Err: err}}
			}
		}
	}
	m.plan.Results[i] = step.Result{Status: step.Running}
	if i < len(m.bodyView) {
		m.bodyView[i] = ""
	}
	if i == m.cursor {
		m.refreshResult()
	}
	// An Authorization Code step with no cached or saved token will open a
	// browser off-thread; flag it so the user knows to complete the sign-in
	// (the spinner alone wouldn't explain the wait). Cleared once the result
	// arrives in onResult.
	if s.Kind == step.KindHTTP && m.plan.NeedsInteractiveLogin(s) {
		m.notice = authWaitNotice
		m.noticeOK = false
	}
	var cmd tea.Cmd
	if s.Stream && s.Kind == step.KindHTTP {
		// Streaming delivers many messages; the body arrives raw (no off-thread
		// highlight pass) and is appended live by onStreamChunk.
		cmd = exec.RunStream(i, s, m.plan.AuthResolver(s))
	} else {
		// Snapshot the highlight palette on the UI thread so the off-thread
		// highlighter can't race a theme switch that rebuilds jsonTheme.
		st := jsonTheme
		cmd = exec.Run(i, s, m.plan.AuthResolver(s), func(body string) string {
			return highlightJSON(body, st)
		})
	}
	// Wake the spinner only if it isn't already animating, so a run-from-here
	// chain doesn't stack duplicate tick loops.
	if !m.spinning {
		m.spinning = true
		return tea.Batch(cmd, m.spinner.Tick)
	}
	return cmd
}

// copiedMsg reports the outcome of a clipboard copy so onResult-style handling
// can show a confirmation (or the failure) in the notice line.
type copiedMsg struct {
	label string // what was copied, e.g. "response body (1.2 KB)"
	err   error
}

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
func (m *Model) setNotice(text string, ok bool) {
	m.notice = text
	m.noticeOK = ok
	m.layout()
}

// errNotRun / errNothingToCopy back the copy notices for steps with no output.
var (
	errNotRun        = fmt.Errorf("step not run — nothing to copy")
	errNothingToCopy = fmt.Errorf("no output to copy")
)

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

// resetState returns the plan to a clean slate after a successful @reset step:
// the engine clears the other results and drops captures back to baseline, then
// the UI clears the matching cached bodies, rebuilds labels (which may reference
// now-dropped captures), and stops any active run-from-here chain. keepIdx is
// the step whose result to preserve, or -1 to clear everything.
func (m *Model) resetState(keepIdx int) {
	// A reset wipes results a stream would write into; stop it first. (When a
	// successful @reset step triggers this, nothing is streaming, so it's a
	// no-op there.)
	m.cancelStream()
	m.plan.Reset(keepIdx)
	for i := range m.bodyView {
		if i != keepIdx {
			m.bodyView[i] = ""
		}
	}
	m.refreshLabels()
	m.runFrom = -1
}

// unresolvedHint explains, in the user's current context, how to resolve a
// missing {{var}}: pick an environment when none is selected (the usual cause),
// note the var is absent from the chosen environment, or point at @defs /
// http-client.env.json when no env file exists at all.
func (m Model) unresolvedHint() string {
	switch {
	case m.envName == "" && len(m.envNames) > 0:
		return "no environment selected; press E to choose one"
	case m.envName != "":
		return fmt.Sprintf("not defined in environment %q (press E to switch)", m.envName)
	default:
		return "not defined — add a @var or an http-client.env.json"
	}
}

// capturingInput reports whether a sub-mode of the plan view currently owns the
// keyboard (the filter editor or the env picker). The App checks this before
// treating `:` as a command, so a colon typed into a filter stays literal.
func (m Model) capturingInput() bool {
	return m.filtering || m.envPicking
}

// escWouldConsume reports whether Esc has a job to do inside the plan view —
// closing the env picker, leaving the filter editor, or clearing an applied
// filter. The App checks this so that, in folder mode, an Esc the plan would
// ignore instead pops back to the overview (one level up).
func (m Model) escWouldConsume() bool {
	return m.envPicking || m.filtering || m.filter != ""
}

// anyRunning reports whether at least one step is mid-flight.
func (m Model) anyRunning() bool {
	for i := range m.plan.Results {
		if m.plan.Results[i].Status == step.Running {
			return true
		}
	}
	return false
}

// refreshLabels recomputes each step's display name with the current variables
// expanded, so the list can render without re-running the regex per frame.
func (m *Model) refreshLabels() {
	m.names = make([]string, len(m.plan.Steps))
	for i, s := range m.plan.Steps {
		name := s.Name
		if s.Kind != step.KindShell {
			name = m.plan.Vars.Expand(s.Name)
		}
		if s.Reset {
			name = "⟲ " + name
		}
		m.names[i] = name
	}
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
// order. With no filter every step is visible.
func (m Model) visible() []int {
	out := make([]int, 0, len(m.plan.Steps))
	q := strings.ToLower(m.filter)
	for i, s := range m.plan.Steps {
		if q == "" {
			out = append(out, i)
			continue
		}
		hay := strings.ToLower(s.Method + " " + m.names[i] + " " + s.Group)
		if strings.Contains(hay, q) {
			out = append(out, i)
		}
	}
	return out
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
