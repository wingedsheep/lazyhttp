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

// version is the build version, overridden at release time via
// `-ldflags "-X main.version=..."` (see .goreleaser.yaml). Dev builds report "dev".
var version = "dev"

func main() {
	// `lazyhttp version` / `--version` / `-v` reports the build version, so a user
	// can confirm what they're running (and bug reports can cite it).
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("lazyhttp", version)
		return
	}

	// `lazyhttp run <plan.http>` executes a plan headlessly with a CI-friendly
	// exit code; bare `lazyhttp <plan.http>` keeps launching the TUI.
	if len(os.Args) > 1 && os.Args[1] == "run" {
		os.Exit(runCommand(os.Args[2:], os.Stdout, os.Stderr))
	}

	env := flag.String("env", "", "environment name from http-client.env.json")
	theme := flag.String("theme", "", "colour theme: "+strings.Join(ui.ThemeNames(), ", ")+" (cycle with `t`)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: lazyhttp [--env NAME] [--theme NAME] <plan.http>   (open the TUI)\n")
		fmt.Fprintf(os.Stderr, "       lazyhttp run [--env NAME] [--filter SUBSTR] <plan.http>   (headless, for CI)\n")
		fmt.Fprintf(os.Stderr, "       lazyhttp --version                                        (print version)\n\n")
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
