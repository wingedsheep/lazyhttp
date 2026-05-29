package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/wingedsheep/lazyhttp/internal/httpfile"
)

// countPending marks a plan whose step count hasn't been parsed yet; counts are
// resolved lazily as rows scroll into view (see browser.countLabel). A parse
// error from CountSteps shows up as -1 and renders as a dash.
const countPending = -2

// openPlanMsg asks the App to open the plan at Path in the plan view. The
// browser emits it when the user selects a row.
type openPlanMsg struct{ Path string }

// browser is the folder overview: a scrollable, filterable list of the .http /
// .rest plans discovered under a root directory, grouped by subfolder. It mirrors
// the step list's navigation (arrows/g/G/^u^d, `/` filter, windowed scrolling) so
// the two screens feel like one app. Selecting a row emits an openPlanMsg.
type browser struct {
	index  httpfile.PlanIndex
	counts []int // step count per index.Files entry; countPending until parsed

	cursor    int // absolute index into index.Files
	filter    string
	filtering bool

	help   help.Model
	keys   browserKeyMap
	styles styles

	width, height int
	contentH      int

	// notice explains an empty list (no plans, or a walk error) instead of
	// leaving the pane blank.
	notice string

	wheelAccum int
}

// newBrowser builds the overview for a discovered plan index, seeding every
// step count as pending so they fill in lazily.
func newBrowser(index httpfile.PlanIndex) browser {
	h := help.New()
	b := browser{
		index:  index,
		counts: make([]int, len(index.Files)),
		help:   h,
		keys:   newBrowserKeyMap(),
		notice: browseNotice(index),
	}
	for i := range b.counts {
		b.counts[i] = countPending
	}
	b.applyStyles()
	return b
}

// applyStyles (re)builds the palette-derived styles, mirroring Model.applyStyles
// so a theme switch recolours the overview too.
func (b *browser) applyStyles() {
	b.help.Styles.ShortKey = lipgloss.NewStyle().Foreground(palette.accent)
	b.help.Styles.FullKey = b.help.Styles.ShortKey
	b.help.Styles.ShortDesc = lipgloss.NewStyle().Foreground(palette.subtle)
	b.help.Styles.FullDesc = b.help.Styles.ShortDesc
	b.help.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(palette.border)
	b.help.Styles.FullSeparator = b.help.Styles.ShortSeparator
	b.styles = newStyles()
}

// browseNotice explains an empty overview: a walk error, or simply no plans
// under the root. It returns "" when there's something to list.
func browseNotice(index httpfile.PlanIndex) string {
	if len(index.Files) > 0 {
		return ""
	}
	if index.Err != nil {
		return "could not read " + index.Root + ": " + index.Err.Error()
	}
	return "no .http or .rest files under " + index.Root
}

// cycleTheme advances to the next colour theme and rebuilds the cached styles.
func (b *browser) cycleTheme() {
	applyTheme(activeTheme + 1)
	b.applyStyles()
}

func (b *browser) layout() {
	if b.width == 0 {
		return
	}
	b.help.Width = b.width
	footerH := strings.Count(b.help.View(b.keys), "\n") + 1
	contentH := b.height - 1 /*status bar*/ - footerH - 2 /*pane borders*/
	b.contentH = max(contentH, 3)
}

func (b browser) Update(msg tea.Msg) (browser, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		b.width, b.height = msg.Width, msg.Height
		b.layout()
		return b, nil
	case tea.MouseMsg:
		return b.onMouse(msg)
	case tea.KeyMsg:
		return b.onKey(msg)
	}
	return b, nil
}

// onMouse moves the cursor with the scroll wheel, accumulating events so one
// physical notch is one step (matching the step list).
func (b browser) onMouse(msg tea.MouseMsg) (browser, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if b.wheelAccum > 0 {
			b.wheelAccum = 0
		}
		b.wheelAccum--
	case tea.MouseButtonWheelDown:
		if b.wheelAccum < 0 {
			b.wheelAccum = 0
		}
		b.wheelAccum++
	default:
		return b, nil
	}
	for b.wheelAccum <= -wheelStep {
		b.moveCursor(-1)
		b.wheelAccum += wheelStep
	}
	for b.wheelAccum >= wheelStep {
		b.moveCursor(1)
		b.wheelAccum -= wheelStep
	}
	return b, nil
}

func (b browser) onKey(msg tea.KeyMsg) (browser, tea.Cmd) {
	if b.filtering {
		return b.filterKey(msg)
	}
	switch {
	case key.Matches(msg, b.keys.Quit):
		return b, tea.Quit
	case msg.Type == tea.KeyEsc:
		if b.filter != "" {
			b.filter = ""
			b.snapCursor()
		}
		return b, nil
	case key.Matches(msg, b.keys.Help):
		b.help.ShowAll = !b.help.ShowAll
		b.layout()
		return b, nil
	case key.Matches(msg, b.keys.Theme):
		b.cycleTheme()
		return b, nil
	case key.Matches(msg, b.keys.Filter):
		b.filtering = true
		return b, nil
	case key.Matches(msg, b.keys.Up):
		b.moveCursor(-1)
	case key.Matches(msg, b.keys.Down):
		b.moveCursor(1)
	case key.Matches(msg, b.keys.HalfUp):
		b.moveCursor(-b.pageStep())
	case key.Matches(msg, b.keys.HalfDn):
		b.moveCursor(b.pageStep())
	case key.Matches(msg, b.keys.Top):
		if vis := b.visible(); len(vis) > 0 {
			b.cursor = vis[0]
		}
	case key.Matches(msg, b.keys.Bottom):
		if vis := b.visible(); len(vis) > 0 {
			b.cursor = vis[len(vis)-1]
		}
	case key.Matches(msg, b.keys.Open):
		if f, ok := b.selected(); ok {
			return b, func() tea.Msg { return openPlanMsg{Path: f.Path} }
		}
	}
	return b, nil
}

// filterKey edits the live filter query, mirroring the step list: most keys
// append/erase, Esc clears, Enter applies, and the arrows still move the cursor.
func (b browser) filterKey(msg tea.KeyMsg) (browser, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return b, tea.Quit
	case tea.KeyEsc:
		b.filtering = false
		b.filter = ""
		b.snapCursor()
		return b, nil
	case tea.KeyEnter:
		b.filtering = false
		return b, nil
	case tea.KeyUp:
		b.moveCursor(-1)
		return b, nil
	case tea.KeyDown:
		b.moveCursor(1)
		return b, nil
	case tea.KeyBackspace:
		if r := []rune(b.filter); len(r) > 0 {
			b.filter = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		b.filter += " "
	case tea.KeyRunes:
		b.filter += string(msg.Runes)
	default:
		return b, nil
	}
	b.snapCursor()
	return b, nil
}

// selected returns the plan under the cursor, or false when the list is empty.
func (b browser) selected() (httpfile.PlanFile, bool) {
	if b.cursor < 0 || b.cursor >= len(b.index.Files) {
		return httpfile.PlanFile{}, false
	}
	return b.index.Files[b.cursor], true
}

// visible returns the absolute indices of plans passing the active filter, in
// order. The filter matches a case-insensitive substring of the relative path.
func (b browser) visible() []int {
	out := make([]int, 0, len(b.index.Files))
	q := strings.ToLower(b.filter)
	for i, f := range b.index.Files {
		if q == "" || strings.Contains(strings.ToLower(f.Rel), q) {
			out = append(out, i)
		}
	}
	return out
}

// moveCursor steps delta positions through the filtered view, skipping hidden
// entries.
func (b *browser) moveCursor(delta int) {
	vis := b.visible()
	if len(vis) == 0 {
		return
	}
	pos := 0
	for i, idx := range vis {
		if idx == b.cursor {
			pos = i
			break
		}
	}
	b.cursor = vis[clamp(pos+delta, 0, len(vis)-1)]
}

// snapCursor keeps the cursor on a visible entry after the filter changes.
func (b *browser) snapCursor() {
	vis := b.visible()
	if len(vis) == 0 {
		return
	}
	for _, idx := range vis {
		if idx == b.cursor {
			return
		}
	}
	b.cursor = vis[0]
}

func (b browser) pageStep() int {
	return max(1, (b.height-6)/2)
}

func (b browser) View() string {
	if b.width == 0 {
		return b.styles.dim.Render("loading…")
	}
	pane := pane(true, b.width-2, b.contentH).Render(b.renderList())
	footer := b.help.View(b.keys)
	// The expanded help (`?`) explains the two things the overview can't show as a
	// single keypress: opening a plan from here, and running it headlessly for CI.
	if b.help.ShowAll {
		footer += "\n" + b.headlessHint()
	}
	return lipgloss.JoinVertical(lipgloss.Left, b.statusBar(), pane, footer)
}

// headlessHint shows the CI / headless command for the highlighted plan, using
// its path relative to the root so it's copy-pasteable. With no selection it
// shows the generic form.
func (b browser) headlessHint() string {
	target := "<plan>"
	if f, ok := b.selected(); ok {
		target = f.Rel
	}
	key := lipgloss.NewStyle().Foreground(palette.accent)
	return b.styles.dim.Render("run headless (CI):  ") +
		key.Render("lazyhttp run "+target) +
		b.styles.dim.Render("   ·   enter opens it in the TUI")
}

// statusBar is the overview's top strip: the logo, the root directory, the plan
// count, the active filter, the theme, and the cursor position — the same shape
// as the plan view's bar so the two screens read as one app.
func (b browser) statusBar() string {
	bar := b.styles.barText
	logo := b.styles.logo.Render("lazyhttp")

	right := b.themeSegment()
	if n := len(b.index.Files); n > 0 {
		right += bar.Render(fmt.Sprintf("   %d/%d", b.cursorPos()+1, n))
	}
	right += bar.Render(" ")

	filter := b.filterSegment()
	used := lipgloss.Width(logo) + lipgloss.Width(filter) + lipgloss.Width(right)
	left := logo + b.dirSegment(b.width-used) + filter

	gap := max(b.width-lipgloss.Width(left)-lipgloss.Width(right), 0)
	return left + bar.Render(strings.Repeat(" ", gap)) + right
}

// cursorPos is the cursor's position within the filtered view, for the "N/total"
// status readout.
func (b browser) cursorPos() int {
	for p, idx := range b.visible() {
		if idx == b.cursor {
			return p
		}
	}
	return 0
}

func (b browser) dirSegment(w int) string {
	const label = "  dir:"
	if w < lipgloss.Width(label)+1 {
		return ""
	}
	name := truncate(b.index.Root, w-lipgloss.Width(label))
	return b.styles.barText.Render(label) +
		lipgloss.NewStyle().Background(palette.crust).Foreground(palette.fg).Render(name)
}

func (b browser) themeSegment() string {
	icon := lipgloss.NewStyle().Background(palette.crust).Foreground(palette.accent).Render("◐ ")
	return icon + lipgloss.NewStyle().Background(palette.crust).Foreground(palette.fg).
		Render(themes[activeTheme].name)
}

func (b browser) filterSegment() string {
	bar := b.styles.barText
	slash := lipgloss.NewStyle().Background(palette.crust).Foreground(palette.accent)
	if b.filtering {
		caret := lipgloss.NewStyle().Background(palette.accent).Foreground(palette.crust).Render(" ")
		return bar.Render("   ") + slash.Render("/") +
			lipgloss.NewStyle().Background(palette.crust).Foreground(palette.fg).Render(b.filter) + caret
	}
	if b.filter != "" {
		return bar.Render("   ") + slash.Render("/"+b.filter)
	}
	return ""
}

// renderList draws the overview body: a PLANS header, subfolder group headings,
// and one row per visible plan with a tree connector and step count — reusing the
// step list's layout vocabulary so the screens match.
func (b browser) renderList() string {
	innerW := max(b.width-4, 8) // pane content width, inside its padding
	header := b.styles.group.Render("PLANS")

	vis := b.visible()
	if len(vis) == 0 {
		msg := b.notice
		if msg == "" && b.filter != "" {
			msg = "No plans match “" + b.filter + "”."
		}
		if msg == "" {
			msg = "No plans."
		}
		return header + "\n" + b.styles.dim.Render(truncate(msg, innerW))
	}

	var lines []string
	cursorLine := 0
	group := ""
	for p, i := range vis {
		if g := b.index.Files[i].Dir; g != group {
			group = g
			if group != "" {
				lines = append(lines, b.styles.group.Render("▌ "+truncate(group, innerW-2)))
			}
		}
		conn := ""
		if group != "" {
			if p == len(vis)-1 || b.index.Files[vis[p+1]].Dir != group {
				conn = "╰"
			} else {
				conn = "├"
			}
		}
		if i == b.cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, b.renderRow(i, conn, innerW))
	}

	// Scroll a window that keeps the cursor in view and never spills past the
	// pane (one row goes to the PLANS header), mirroring renderList for steps.
	budget := max(b.contentH-2, 1)
	start := 0
	if len(lines) > budget {
		start = clamp(cursorLine-budget/2, 0, len(lines)-budget)
		lines = lines[start : start+budget]
	}

	return header + "\n" + strings.Join(lines, "\n")
}

// renderRow lays out one plan row to innerW columns: an optional tree connector,
// a cursor caret, the file name, and a right-aligned step count. The selected row
// paints a solid background across every segment.
func (b browser) renderRow(i int, conn string, innerW int) string {
	f := b.index.Files[i]
	sel := i == b.cursor

	caret, caretColor := " ", lipgloss.TerminalColor(palette.subtle)
	if sel {
		caret, caretColor = "▸", palette.accent
	}

	count := b.countLabel(i)
	countW := lipgloss.Width(count)
	connW := lipgloss.Width(conn)

	// Fixed columns before the name: conn + caret + space.
	fixed := connW + 1 + 1
	nameMax := max(innerW-fixed-countW-1, 1)
	name := truncate(f.Name, nameMax)
	gap := max(innerW-fixed-lipgloss.Width(name)-countW, 1)

	bg := cell(palette.fg, sel, false)
	row := ""
	if conn != "" {
		row += cell(palette.border, sel, false).Render(conn)
	}
	row += cell(caretColor, sel, true).Render(caret) +
		bg.Render(" ") +
		cell(palette.fg, sel, sel).Render(name) +
		bg.Render(strings.Repeat(" ", gap)) +
		cell(palette.subtle, sel, false).Render(count)
	return row
}

// countLabel renders the per-file step count for the list. The count is resolved
// lazily here — only rows in the rendered window reach this path, so a large tree
// never parses files the user hasn't scrolled to. The parse result is cached on
// b.counts; the write lands in the shared backing array, so it persists across
// frames even though View runs on a value receiver and each file is parsed once.
func (b browser) countLabel(i int) string {
	if b.counts[i] == countPending {
		b.counts[i] = httpfile.CountSteps(b.index.Files[i].Path)
	}
	switch n := b.counts[i]; {
	case n < 0:
		return "—"
	case n == 1:
		return "1 step"
	default:
		return fmt.Sprintf("%d steps", n)
	}
}

// browserKeyMap is the overview's binding set: list navigation, filter, open,
// theme, help, quit. It deliberately omits the plan-only run/clear/env bindings.
type browserKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Top    key.Binding
	Bottom key.Binding
	HalfUp key.Binding
	HalfDn key.Binding
	Open   key.Binding
	Filter key.Binding
	Theme  key.Binding
	Help   key.Binding
	Quit   key.Binding
}

func newBrowserKeyMap() browserKeyMap {
	return browserKeyMap{
		Up:     key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Down:   key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		Top:    key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "first")),
		Bottom: key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "last")),
		HalfUp: key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("^u", "half-page up")),
		HalfDn: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("^d", "half-page down")),
		Open:   key.NewBinding(key.WithKeys("enter", "l"), key.WithHelp("enter", "open")),
		Filter: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Theme:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k browserKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Open, k.Filter, k.Theme, k.Help, k.Quit}
}

func (k browserKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom, k.HalfUp, k.HalfDn},
		{k.Open, k.Filter, k.Theme, k.Help, k.Quit},
	}
}
