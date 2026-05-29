package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/wingedsheep/lazyhttp/internal/auth"
	"github.com/wingedsheep/lazyhttp/internal/capture"
	"github.com/wingedsheep/lazyhttp/internal/exec"
	"github.com/wingedsheep/lazyhttp/internal/httpfile"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

type focus int

const (
	focusList focus = iota
	focusResult
)

// Model is the root Bubble Tea model: the parsed plan, per-step results, and
// the widgets that render them.
type Model struct {
	path    string
	envName string

	steps   []step.Step
	results []step.Result
	cursor  int
	focus   focus

	// names holds each step's display name with {{vars}} already expanded, and
	// bodyView holds each response body already syntax-highlighted. Both are
	// rebuilt only when the data changes (load, a result, a reset) so list
	// navigation and redraws stay allocation-light.
	names    []string
	bodyView []string

	// spinning is true while a spinner-tick loop is in flight. It lets us drive
	// the spinner only while a step runs and stay completely idle otherwise.
	spinning bool

	// showDetails toggles the request preview and the response headers on the
	// right; off by default so the response output gets the whole pane.
	showDetails bool

	// filter narrows the visible step list to those matching a case-insensitive
	// substring of "method name group"; filtering is true while it's being typed.
	filter    string
	filtering bool

	// envNames lists the environments declared in http-client.env.json (sorted);
	// envPicking is true while the env picker overlay is open and envCursor marks
	// the highlighted entry. Switching env reloads the plan against the new vars.
	envNames   []string
	envPicking bool
	envCursor  int

	// vars holds env + inline definitions plus values captured from responses
	// as steps run. Placeholders are expanded against it at execution time.
	// baseVars is the env+inline snapshot used to drop captures on a reset.
	vars     httpfile.Vars
	baseVars httpfile.Vars

	// authConfigs are the OAuth2 configurations from the environment's
	// Security.Auth block; authCache holds tokens fetched from them, reused
	// across steps until they expire. Both are rebuilt per load / env switch.
	authConfigs map[string]auth.Config
	authCache   *auth.Cache

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
		path:     path,
		envName:  envName,
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
	for i := range m.results {
		if i < len(m.bodyView) && m.bodyView[i] != "" {
			m.bodyView[i] = highlightJSON(m.results[i].Body, jsonTheme)
		}
	}
	m.refreshResult()
}

// load (re)reads and parses the plan, resetting results.
func (m *Model) load() {
	// The env list drives the picker; ignore errors here so a malformed env
	// file still surfaces through LoadEnv below rather than blanking the list.
	if names, err := httpfile.LoadEnvNames(m.path); err == nil {
		m.envNames = names
	}
	vars, err := httpfile.LoadEnv(m.path, m.envName)
	if err != nil {
		m.loadErr = err
		return
	}
	steps, err := httpfile.ParseFile(m.path, vars)
	if err != nil {
		m.loadErr = err
		return
	}
	// OAuth2 configurations come from the same env file; a fresh token cache per
	// load/env-switch means switching credentials never reuses a stale token.
	// Errors here are non-fatal: a malformed Security block just disables auth.
	m.authConfigs, _ = httpfile.LoadAuth(m.path, m.envName)
	m.authCache = auth.NewCache()

	m.loadErr = nil
	m.vars = vars                // env + inline defs; captures layer on as steps run
	m.baseVars = cloneVars(vars) // pristine copy to restore when state is reset
	m.steps = steps
	m.results = make([]step.Result, len(steps))
	m.bodyView = make([]string, len(steps))
	m.refreshLabels()
	if m.cursor >= len(steps) {
		m.cursor = max(0, len(steps)-1)
	}
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

	case tea.MouseMsg:
		return m.onMouse(msg)

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

// onMouse routes the scroll wheel: it scrolls the response body when that pane
// is focused, otherwise it moves through the step list (k9s-style).
func (m Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
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

// onResult stores a finished result and advances a run-from-here chain.
func (m Model) onResult(msg exec.ResultMsg) (tea.Model, tea.Cmd) {
	if msg.Index < len(m.results) {
		r := m.evaluate(msg.Index, msg.Result)
		m.results[msg.Index] = r
		if msg.Index < len(m.bodyView) {
			// Highlighting was done off the UI thread inside the exec command.
			m.bodyView[msg.Index] = msg.Highlighted
		}
		// Captures from this response may feed later step names; re-expand them.
		m.refreshLabels()
		// A successful @reset step returns the plan to a clean slate: every
		// other step's result is cleared and captured variables are dropped,
		// mirroring the backend reset the step just performed.
		if msg.Index < len(m.steps) && m.steps[msg.Index].Reset && r.OK() {
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
		if msg.Index < len(m.results) && m.results[msg.Index].OK() && next < len(m.steps) {
			m.runFrom = next
			return m, m.run(next)
		}
		m.runFrom = -1
	}
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

	case key.Matches(msg, m.keys.Reload):
		m.load()
		m.refreshResult()
		return m, nil

	case key.Matches(msg, m.keys.Request):
		m.showDetails = !m.showDetails
		m.refreshResult()
		return m, nil

	case key.Matches(msg, m.keys.Theme):
		m.cycleTheme()
		return m, nil

	case key.Matches(msg, m.keys.Env):
		// Open the picker only when there's something to choose from; with no
		// env file the key is a no-op rather than an empty modal.
		if len(m.envNames) > 0 {
			m.envPicking = true
			m.envCursor = indexOf(m.envOptions(), m.envName)
		}
		return m, nil
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
		if m.cursor < len(m.results) {
			m.results[m.cursor] = step.Result{}
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
	if i < 0 || i >= len(m.steps) {
		return nil
	}
	// Resolve {{vars}} (and any `< file` body) up front. A read failure fails the
	// step immediately — mirroring a transport error — rather than sending an
	// empty-bodied request, so the silent-drop footgun becomes a visible error.
	s, err := m.expand(m.steps[i])
	if err != nil {
		return func() tea.Msg {
			return exec.ResultMsg{Index: i, Result: step.Result{Status: step.Failed, Err: err}}
		}
	}
	m.results[i] = step.Result{Status: step.Running}
	if i < len(m.bodyView) {
		m.bodyView[i] = ""
	}
	if i == m.cursor {
		m.refreshResult()
	}
	// Snapshot the highlight palette on the UI thread so the off-thread
	// highlighter can't race a theme switch that rebuilds jsonTheme.
	st := jsonTheme
	cmd := exec.Run(i, s, m.authResolver(s), func(body string) string {
		return highlightJSON(body, st)
	})
	// Wake the spinner only if it isn't already animating, so a run-from-here
	// chain doesn't stack duplicate tick loops.
	if !m.spinning {
		m.spinning = true
		return tea.Batch(cmd, m.spinner.Tick)
	}
	return cmd
}

// expand returns a copy of s with its URL, headers and body resolved against
// the current variables. Captures are left untouched (they target the response).
//
// When the step's body comes from a file (`< path` / `<@ path`), the file is
// read here — where the variable set is available — and its contents become the
// body. `<@` additionally expands {{vars}} in those contents; `<` sends them
// verbatim. The path is resolved relative to the plan file's directory. A read
// error is returned so the caller can surface it as a failed result rather than
// silently sending an empty body. BodyFile is kept on the returned step (now
// holding the var-expanded path) for the request preview.
func (m Model) expand(s step.Step) (step.Step, error) {
	expand := func(in string) string { return m.vars.ExpandFunc(in, m.resolveResponseRef) }

	s.URL = expand(s.URL)
	headers := make(map[string]string, len(s.Headers))
	for k, v := range s.Headers {
		headers[k] = expand(v)
	}
	s.Headers = headers

	if s.BodyFile == "" {
		s.Body = expand(s.Body)
		return s, nil
	}

	path := expand(s.BodyFile)
	s.BodyFile = path
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(filepath.Dir(m.path), full)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return s, err
	}
	body := string(data)
	if s.BodyFileVars {
		body = expand(body)
	}
	s.Body = body
	return s, nil
}

// authResolver returns an exec.AuthResolver for the expanded step s, or nil when
// the step has no {{$auth.token(...)}} reference or no Security.Auth
// configurations are defined. Configuration values are expanded here, on the UI
// thread, so a client secret sourced from `{{$processEnv …}}` or another
// variable is resolved against the live var set without racing the request
// goroutine; only the token fetch itself runs off-thread.
func (m Model) authResolver(s step.Step) exec.AuthResolver {
	if len(m.authConfigs) == 0 {
		return nil
	}
	referenced := auth.References(s.URL) || auth.References(s.Body)
	for _, v := range s.Headers {
		if referenced {
			break
		}
		referenced = auth.References(v)
	}
	if !referenced {
		return nil
	}

	expand := func(in string) string { return m.vars.ExpandFunc(in, m.resolveResponseRef) }
	cfgs := make(map[string]auth.Config, len(m.authConfigs))
	for id, c := range m.authConfigs {
		c.TokenURL = expand(c.TokenURL)
		c.AuthURL = expand(c.AuthURL)
		c.ClientID = expand(c.ClientID)
		c.ClientSecret = expand(c.ClientSecret)
		c.Scope = expand(c.Scope)
		c.Username = expand(c.Username)
		c.Password = expand(c.Password)
		cfgs[id] = c
	}
	return auth.NewResolver(cfgs, m.authCache)
}

// resolveResponseRef resolves an inline response reference — VS Code REST Client
// syntax such as {{login.response.body.$.token}} or
// {{login.response.headers.Location}} — against the stored result of an earlier
// named step. It maps the reference onto a capture expression and reuses
// capture.Eval, so JSON paths and header lookups behave exactly as in
// `# @capture`. ok is false for tokens that aren't response references, name an
// unrun step, or can't be resolved, so Expand leaves them untouched.
func (m Model) resolveResponseRef(token string) (string, bool) {
	name, rest, ok := strings.Cut(token, ".response.")
	if !ok {
		return "", false
	}
	r, ok := m.lastResult(name)
	if !ok {
		return "", false
	}
	var expr string
	switch {
	case rest == "body" || rest == "body.*":
		expr = "body"
	case strings.HasPrefix(rest, "body."):
		expr = strings.TrimPrefix(rest, "body.") // e.g. "$.token", "items[0].id"
	case strings.HasPrefix(rest, "headers."):
		expr = "header." + strings.TrimPrefix(rest, "headers.")
	default:
		return "", false
	}
	return capture.Eval(expr, r)
}

// lastResult returns the result of the most recently positioned step named name
// that has already run. Scanning from the bottom means a reference picks up the
// latest result when a name is reused across the plan.
func (m Model) lastResult(name string) (step.Result, bool) {
	for i := len(m.steps) - 1; i >= 0; i-- {
		if m.steps[i].Name == name && i < len(m.results) && m.results[i].Status != step.Pending {
			return m.results[i], true
		}
	}
	return step.Result{}, false
}

// evaluate runs a finished step's captures and assertions, returning the result
// enriched with assertion outcomes. Captures populate the variable set so later
// steps can reference them.
func (m *Model) evaluate(i int, r step.Result) step.Result {
	if r.Err != nil {
		return r
	}
	for _, c := range m.steps[i].Captures {
		if val, ok := capture.Eval(c.Expr, r); ok {
			m.vars[c.Name] = val
		}
	}
	for _, a := range m.steps[i].Asserts {
		r.Asserts = append(r.Asserts, capture.Check(a, r))
	}
	return r
}

// resetState clears every step's result (except keepIdx, pass -1 to clear all)
// and drops captured variables back to the env+inline baseline. It also stops
// any active run-from-here chain.
func (m *Model) resetState(keepIdx int) {
	for i := range m.results {
		if i != keepIdx {
			m.results[i] = step.Result{}
			if i < len(m.bodyView) {
				m.bodyView[i] = ""
			}
		}
	}
	m.vars = cloneVars(m.baseVars)
	m.refreshLabels() // names may reference now-dropped captures
	m.runFrom = -1
}

// anyRunning reports whether at least one step is mid-flight.
func (m Model) anyRunning() bool {
	for i := range m.results {
		if m.results[i].Status == step.Running {
			return true
		}
	}
	return false
}

// refreshLabels recomputes each step's display name with the current variables
// expanded, so the list can render without re-running the regex per frame.
func (m *Model) refreshLabels() {
	m.names = make([]string, len(m.steps))
	for i, s := range m.steps {
		name := s.Name
		if s.Kind != step.KindShell {
			name = m.vars.Expand(s.Name)
		}
		if s.Reset {
			name = "⟲ " + name
		}
		m.names[i] = name
	}
}

// cloneVars returns an independent copy of a variable set.
func cloneVars(v httpfile.Vars) httpfile.Vars {
	out := make(httpfile.Vars, len(v))
	for k, val := range v {
		out[k] = val
	}
	return out
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
	if len(m.steps) == 0 {
		return
	}
	m.cursor = min(max(i, 0), len(m.steps)-1)
	m.refreshResult()
}

// visible returns the absolute indices of steps that pass the active filter, in
// order. With no filter every step is visible.
func (m Model) visible() []int {
	out := make([]int, 0, len(m.steps))
	q := strings.ToLower(m.filter)
	for i, s := range m.steps {
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
