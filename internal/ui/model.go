package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
//
// Its behaviour is split across a few files in this package: input.go owns the
// keyboard/mouse routing and cursor math, env.go the environment picker, copy.go
// the clipboard copy and the shared notice line. This file holds the struct, the
// load/run/result lifecycle, and the small caches the renderer reads.
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

	// reqHL memoizes request-preview body highlighting, keyed by body text, so
	// the preview isn't re-colourised on every cursor move/resize. Dropped on a
	// theme switch (see cycleTheme). nil only on bare test models, which skip the
	// cache and highlight directly.
	reqHL *reqHighlightCache

	// spinning is true while a spinner-tick loop is in flight. It lets us drive
	// the spinner only while a step runs and stay completely idle otherwise.
	spinning bool

	// streamSub is the live subscription for a `# @stream` step in flight, or nil
	// when nothing is streaming. The model holds it so a disruptive action
	// (reload, clear, env switch) can Cancel the request mid-stream.
	streamSub *exec.StreamSub

	// Live-stream rendering state. streamIndex is the step a `# @stream` response
	// is arriving for (−1 when nothing streams); streamBody accumulates that body
	// in a Builder (appending is amortised O(1), where re-concatenating into the
	// step's Result.Body was O(n) per chunk → quadratic over a long token stream);
	// streamHead caches the request-preview prefix shown above it. A chunk renders
	// streamHead + streamBody directly (see streamView), bypassing formatResult's
	// per-chunk Expand and `< file` disk read. A pointer, so copying the Model on
	// each Update doesn't trip strings.Builder's copy check.
	streamIndex int
	streamBody  *strings.Builder
	streamHead  string

	// showRequest toggles the request preview (method/URL/headers/body) at the
	// top of the right pane; showHeaders toggles the response headers above the
	// body. Both off by default so the response body gets the whole pane.
	showRequest bool
	showHeaders bool

	// filter narrows the visible step list to those matching a case-insensitive
	// substring of "method name group"; filtering is true while it's being typed.
	filter    string
	filtering bool

	// vis caches the absolute indices of steps passing the active filter. It is
	// recomputed by refilter() whenever the filter or display names change, so the
	// several visible() reads per keystroke (cursor math, hit-testing, rendering)
	// don't re-scan and re-lowercase every step.
	vis []int

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
		plan:        &runner.Plan{},
		runFrom:     -1,
		streamIndex: -1,
		streamBody:  &strings.Builder{},
		reqHL:       newReqHighlightCache(),
		viewport:    viewport.New(0, 0),
		spinner:     sp,
		help:        help.New(),
		keys:        newKeyMap(),
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
	// The request-body memo holds highlights in the old palette; drop it so the
	// next preview re-colourises against the new theme.
	m.reqHL = newReqHighlightCache()
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

// authWaitNotice is shown while an Authorization Code step waits for the user to
// finish the browser sign-in; onResult clears it once the result arrives.
const authWaitNotice = "Waiting for browser sign-in to complete…"

// onResult stores a finished result and advances a run-from-here chain.
func (m Model) onResult(msg exec.ResultMsg) (tea.Model, tea.Cmd) {
	// A terminal ResultMsg ends any active stream (only one runs at a time), so
	// release the subscription and stop treating this step as the live stream —
	// its Body now comes from the terminal result. The StreamDoneMsg path covers
	// cancelled streams.
	m.streamSub = nil
	m.streamIndex = -1
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

// onStreamChunk appends a streamed slice to the live stream's body builder and,
// when that step is selected, re-renders the response pinned to the bottom so the
// output scrolls in as it arrives. The body lives only in streamBody until the
// terminal ResultMsg replaces it: appending is amortised O(1) and streamView
// reuses the cached prefix, where the old path re-concatenated Result.Body and
// rebuilt the whole pane through formatResult (re-expanding the step and
// re-reading any `< file` body from disk) on every chunk — quadratic over a long
// token stream. It always returns WaitForChunk so the pump goroutine keeps
// draining even after a reset clears streamIndex; the guard only stops us
// appending to a stream that no longer belongs to this step.
func (m Model) onStreamChunk(msg exec.StreamChunkMsg) (tea.Model, tea.Cmd) {
	if msg.Index == m.streamIndex && msg.Index < len(m.plan.Results) &&
		m.plan.Results[msg.Index].Status == step.Running {
		m.streamBody.WriteString(msg.Data)
		if msg.Index == m.cursor {
			m.viewport.SetContent(m.streamView())
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
	m.streamIndex = -1
}

// stopStream ends an in-flight `# @stream` early but keeps what has arrived: it
// asks the subscription to Stop, so the pump delivers the partial body as a
// normal terminal result (the existing WaitForChunk command picks it up, and
// onResult runs captures/assertions and clears the live-stream state). The
// run-from-here chain is halted, since a manual stop is a deliberate "stop
// here". Contrast cancelStream, which throws the partial result away. A no-op
// when nothing is streaming.
func (m *Model) stopStream() {
	if m.streamSub == nil {
		return
	}
	m.streamSub.Stop()
	m.runFrom = -1
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
	// Mark this step as the live stream before the first render, so refreshResult
	// caches its request-preview prefix and chunk re-renders take the cheap
	// streamView path. A fresh builder collects the body off the step's Result.
	streaming := s.Stream && s.Kind == step.KindHTTP
	if streaming {
		m.streamIndex = i
		m.streamBody = &strings.Builder{}
	} else {
		m.streamIndex = -1
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
	if streaming {
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
	m.refilter() // names feed the filter match, so the cache must follow them
}
