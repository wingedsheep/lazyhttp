package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/wingedsheep/lazyhttp/internal/exec"
	"github.com/wingedsheep/lazyhttp/internal/httpfile"
)

// App is the root model for folder mode: a view stack over the folder overview
// (browser) and a single open plan (Model), with a k9s-style `:` command bar to
// jump back to the overview. The browser is always present and keeps its cursor
// and filter, so returning to it lands the user exactly where they left. A plan
// is (re)loaded fresh each time one is opened.
type App struct {
	envName string

	browser  browser
	plan     Model
	planOpen bool // a plan has been opened at least once (plan is usable)
	showPlan bool // plan view is foreground (else the overview is)

	// cmdActive is the `:` command bar; cmdInput is its live text and cmdErr a
	// one-line complaint about an unknown command, cleared as the user edits.
	cmdActive bool
	cmdInput  string
	cmdErr    string

	width, height int
}

// NewApp builds the folder-mode root by discovering every plan under root. The
// overview opens first; selecting a plan opens it in the plan view.
func NewApp(root, envName string) App {
	idx := httpfile.DiscoverPlans(root)
	return App{
		envName: envName,
		browser: newBrowser(idx),
	}
}

// Init starts idle: like the plan view, nothing animates until a step runs.
func (a App) Init() tea.Cmd { return nil }

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		// Size both views so whichever is foregrounded is already laid out.
		nb, _ := a.browser.Update(msg)
		a.browser = nb
		if a.planOpen {
			a.plan = a.forwardToPlan(msg)
		}
		return a, nil

	case openPlanMsg:
		return a.openPlan(msg.Path)

	case spinner.TickMsg, exec.ResultMsg:
		// Plan-specific messages: deliver them to the plan even while the overview
		// is foreground, so an in-flight request finishes and the spinner settles.
		if a.planOpen {
			nm, cmd := a.plan.Update(msg)
			a.plan = nm.(Model)
			return a, cmd
		}
		return a, nil

	case tea.KeyMsg:
		return a.onKey(msg)
	}

	// Everything else (mouse, etc.) goes to the foreground view.
	return a.routeToForeground(msg)
}

// onKey handles the command bar, the `:` that opens it, and otherwise routes to
// the foreground view.
func (a App) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.cmdActive {
		return a.cmdKey(msg)
	}
	// `:` opens the command bar from the plan view (the overview has nothing to
	// go "back" to). Suppressed while the plan is editing a filter / env picker.
	if a.showPlan && msg.String() == ":" && !a.plan.capturingInput() {
		a.cmdActive = true
		a.cmdErr = ""
		return a, nil
	}
	// Esc pops one level up: from an open plan back to the overview — but only
	// when the plan itself has no use for it (no env picker, filter editor, or
	// applied filter to clear first).
	if a.showPlan && msg.Type == tea.KeyEsc && !a.plan.escWouldConsume() {
		a.showPlan = false
		return a, nil
	}
	return a.routeToForeground(msg)
}

// routeToForeground sends a message to whichever view is foreground.
func (a App) routeToForeground(msg tea.Msg) (tea.Model, tea.Cmd) {
	if a.showPlan {
		nm, cmd := a.plan.Update(msg)
		a.plan = nm.(Model)
		// Carry an environment switch (the plan's E picker) back up, so the next
		// plan opened defaults to the same environment — pick it once, browse all.
		a.envName = a.plan.envName
		return a, cmd
	}
	nb, cmd := a.browser.Update(msg)
	a.browser = nb
	return a, cmd
}

// openPlan loads the plan at path fresh and brings the plan view to the
// foreground, sized to the current window.
func (a App) openPlan(path string) (tea.Model, tea.Cmd) {
	a.plan = New(path, a.envName)
	// Mark the plan as folder-opened so its help surfaces the `:files` hint.
	a.plan.keys.folderMode = true
	a.planOpen = true
	a.showPlan = true
	a.cmdActive, a.cmdInput, a.cmdErr = false, "", ""
	if a.width > 0 {
		a.plan = a.forwardToPlan(tea.WindowSizeMsg{Width: a.width, Height: a.height})
	}
	return a, nil
}

// forwardToPlan applies a message to the plan model and returns the updated
// value, discarding the command (used for layout-only messages).
func (a App) forwardToPlan(msg tea.Msg) Model {
	nm, _ := a.plan.Update(msg)
	return nm.(Model)
}

// cmdKey edits the `:` command bar: runes/backspace edit the text, Enter runs
// it, Esc cancels.
func (a App) cmdKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return a, tea.Quit
	case tea.KeyEsc:
		a.cmdActive, a.cmdInput, a.cmdErr = false, "", ""
	case tea.KeyEnter:
		return a.runCommand()
	case tea.KeyBackspace:
		if r := []rune(a.cmdInput); len(r) > 0 {
			a.cmdInput = string(r[:len(r)-1])
		}
		a.cmdErr = ""
	case tea.KeySpace:
		a.cmdInput += " "
		a.cmdErr = ""
	case tea.KeyRunes:
		a.cmdInput += string(msg.Runes)
		a.cmdErr = ""
	}
	return a, nil
}

// runCommand executes the typed `:` command. `:files` / `:plans` / `:ls` return
// to the overview; `:q` / `:quit` exit. An unknown command keeps the bar open
// with a complaint so the user can correct it.
func (a App) runCommand() (tea.Model, tea.Cmd) {
	switch strings.TrimSpace(strings.ToLower(a.cmdInput)) {
	case "files", "plans", "ls":
		a.showPlan = false
		a.cmdActive, a.cmdInput, a.cmdErr = false, "", ""
	case "q", "quit":
		return a, tea.Quit
	case "":
		a.cmdActive, a.cmdInput, a.cmdErr = false, "", ""
	default:
		a.cmdErr = "unknown command (try :files)"
	}
	return a, nil
}

func (a App) View() string {
	base := a.browser.View()
	if a.showPlan {
		base = a.plan.View()
	}
	if !a.cmdActive {
		return base
	}
	// Overlay the command bar on the foreground view's bottom row (the footer
	// hint), which is exactly where a k9s command bar lives.
	lines := strings.Split(base, "\n")
	if len(lines) > 0 {
		lines[len(lines)-1] = a.renderCmdBar()
	}
	return strings.Join(lines, "\n")
}

// renderCmdBar draws the `:` prompt with the live input, a caret, and any
// unknown-command complaint.
func (a App) renderCmdBar() string {
	prompt := lipgloss.NewStyle().Foreground(palette.accent).Bold(true).Render(":")
	text := lipgloss.NewStyle().Foreground(palette.fg).Render(a.cmdInput)
	caret := lipgloss.NewStyle().Background(palette.accent).Foreground(palette.crust).Render(" ")
	line := prompt + text + caret
	if a.cmdErr != "" {
		line += lipgloss.NewStyle().Foreground(palette.danger).Render("   " + a.cmdErr)
	}
	return line
}
