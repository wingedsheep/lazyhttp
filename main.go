// Command lazyhttp is a terminal UI for running .http test plans step by step.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/config"
	"github.com/wingedsheep/lazyhttp/internal/ui"
)

func main() {
	env := flag.String("env", "", "environment name from http-client.env.json")
	theme := flag.String("theme", "", "colour theme: "+strings.Join(ui.ThemeNames(), ", ")+" (cycle with `t`)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: lazyhttp [--env NAME] [--theme NAME] <plan.http>\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	// Theme precedence: the saved preference is the baseline, an explicit --theme
	// flag overrides it for this run. The flag must name a real theme; a bad
	// saved value is ignored (SetTheme leaves the default in place).
	cfg := config.Load()
	if cfg.Theme != "" {
		ui.SetTheme(cfg.Theme)
	}
	if *theme != "" && !ui.SetTheme(*theme) {
		fmt.Fprintf(os.Stderr, "unknown theme %q; valid: %s\n", *theme, strings.Join(ui.ThemeNames(), ", "))
		os.Exit(2)
	}

	model := ui.New(flag.Arg(0), *env)
	// AltScreen keeps lazyhttp full-screen; mouse capture means the wheel
	// scrolls within the TUI instead of the terminal's scrollback.
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Persist the theme the user ended on so the next launch matches. Best-effort:
	// a failed write shouldn't fail the command.
	cfg.Theme = ui.ActiveThemeName()
	_ = cfg.Save()
}
